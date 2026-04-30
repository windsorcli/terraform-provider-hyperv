package connection

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"
)

// SSHOptions configures the SSH backend. The provider's Configure pass
// resolves env vars + provider attributes into this struct (see
// internal/provider/backend_select.go).
type SSHOptions struct {
	Host     string // required
	Port     int    // default 22
	Username string // required

	// Auth methods, in priority order:
	//
	//   1. PrivateKey       (raw key contents -- wins if both PrivateKey and PrivateKeyPath are set)
	//   2. PrivateKeyPath   (path read at Open time)
	//   3. Password         (fallback only -- key auth is preferred)
	//
	// Passphrase decrypts the key when set.
	PrivateKey     []byte
	PrivateKeyPath string
	Passphrase     []byte
	Password       string

	// KnownHostsPath is the file used for host key verification. Default:
	// ~/.ssh/known_hosts. Empty path falls back to the default. A missing
	// file is a hard error -- silently disabling host-key checking would be
	// a security regression.
	KnownHostsPath string

	// Timeout is the dial timeout. Default 30s.
	Timeout time.Duration

	// CommandTimeout bounds an individual RunScript call. Default
	// 5m. A wedged remote cmdlet surfaces as ErrTimeout instead of
	// blocking the whole apply. Set to 0 to disable.
	CommandTimeout time.Duration

	// KeepaliveInterval is how often the backend sends an SSH
	// keepalive request while the persistent client is open.
	// Default 30s. Prevents NAT/firewall mid-apply drops. Set to
	// 0 to disable.
	KeepaliveInterval time.Duration

	// PwshPath is the binary the remote shell invokes per call. Default:
	// "powershell.exe" -- universally available on Windows. Set to "pwsh"
	// or "pwsh.exe" to prefer PS 7+ if installed.
	PwshPath string
}

const (
	defaultSSHPort              = 22
	defaultSSHTimeout           = 30 * time.Second
	defaultSSHCommandTimeout    = 5 * time.Minute
	defaultSSHKeepaliveInterval = 30 * time.Second
	defaultSSHPwshPath          = "powershell.exe"
)

// sshBackend implements Connection over a single persistent ssh.Client. The
// client is established by Open and reused across RunScript calls --
// ssh.Client.NewSession is concurrency-safe, so resources can run scripts
// in parallel through one connection.
type sshBackend struct {
	opts   SSHOptions
	config *ssh.ClientConfig
	addr   string

	mu     sync.Mutex
	client *ssh.Client

	// keepaliveDone is closed by Close() to stop the keepalive loop.
	keepaliveDone chan struct{}

	// alive flips false on a failed keepalive; RunScript checks at
	// entry and lazy-reconnects. Atomic so the keepalive goroutine
	// can update without acquiring mu.
	alive atomic.Bool
}

// Compile-time assertion.
var _ Connection = (*sshBackend)(nil)

// NewSSH builds a Connection backed by ssh.Client. It resolves authentication
// methods and the known_hosts callback up-front so misconfiguration surfaces
// before Open dials. The client itself is established lazily by Open.
func NewSSH(opts SSHOptions) (Connection, error) {
	if opts.Host == "" {
		return nil, errors.New("ssh: host is required")
	}
	if opts.Username == "" {
		return nil, errors.New("ssh: username is required")
	}
	port := opts.Port
	if port == 0 {
		port = defaultSSHPort
	}
	timeout := opts.Timeout
	if timeout == 0 {
		timeout = defaultSSHTimeout
	}
	commandTimeout := opts.CommandTimeout
	if commandTimeout == 0 {
		commandTimeout = defaultSSHCommandTimeout
	}
	keepalive := opts.KeepaliveInterval
	if keepalive == 0 {
		keepalive = defaultSSHKeepaliveInterval
	}

	auths, err := buildSSHAuthMethods(opts)
	if err != nil {
		return nil, err
	}
	if len(auths) == 0 {
		return nil, errors.New("ssh: no auth method configured -- " +
			"set private_key, private_key_path, or password")
	}

	hostKeyCallback, err := loadKnownHostsCallback(opts.KnownHostsPath)
	if err != nil {
		return nil, err
	}

	pwshPath := opts.PwshPath
	if pwshPath == "" {
		pwshPath = defaultSSHPwshPath
	}

	cfg := &ssh.ClientConfig{
		User:            opts.Username,
		Auth:            auths,
		HostKeyCallback: hostKeyCallback,
		Timeout:         timeout,
	}

	return &sshBackend{
		opts: SSHOptions{
			Host:              opts.Host,
			Port:              port,
			Username:          opts.Username,
			PwshPath:          pwshPath,
			Timeout:           timeout,
			CommandTimeout:    commandTimeout,
			KeepaliveInterval: keepalive,
		},
		config: cfg,
		addr:   net.JoinHostPort(opts.Host, strconv.Itoa(port)),
	}, nil
}

// Backend returns the lowercase identifier used for tflog field decoration.
func (b *sshBackend) Backend() string { return "ssh" }

// Open establishes the persistent ssh.Client. Idempotent -- subsequent
// calls return nil if the client is already up.
//
// Both phases honor ctx cancellation: net.Dialer.DialContext makes the TCP
// dial cancelable; ssh.NewClientConn doesn't accept a context, so we race
// it against ctx.Done() in a goroutine and close the underlying TCP conn
// to force the handshake to unblock if ctx fires (otherwise an operator
// Ctrl+C during `terraform apply`'s provider-configure phase would hang
// for many seconds while the OS-level read times out).
func (b *sshBackend) Open(ctx context.Context) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.client != nil {
		return nil
	}

	dialer := &net.Dialer{Timeout: b.config.Timeout}
	tcpConn, err := dialer.DialContext(ctx, "tcp", b.addr)
	if err != nil {
		return fmt.Errorf("ssh: dial %s: %w", b.addr, err)
	}

	type handshakeResult struct {
		sshConn ssh.Conn
		chans   <-chan ssh.NewChannel
		reqs    <-chan *ssh.Request
		err     error
	}
	done := make(chan handshakeResult, 1)
	go func() {
		c, ch, rq, err := ssh.NewClientConn(tcpConn, b.addr, b.config)
		done <- handshakeResult{c, ch, rq, err}
	}()

	select {
	case r := <-done:
		if r.err != nil {
			_ = tcpConn.Close()
			return fmt.Errorf("ssh: handshake with %s: %w", b.addr, r.err)
		}
		b.client = ssh.NewClient(r.sshConn, r.chans, r.reqs)
		b.alive.Store(true)
		// Capture client+done as locals so a Close()+Open() cycle
		// can't leak this loop onto the new client.
		if b.opts.KeepaliveInterval > 0 {
			b.keepaliveDone = make(chan struct{})
			go b.keepaliveLoop(b.client, b.keepaliveDone, b.opts.KeepaliveInterval)
		}
		return nil
	case <-ctx.Done():
		select {
		case r := <-done:
			// Race: handshake completed before we observed ctx-cancel.
			// Close the ssh.Conn to send SSH_MSG_DISCONNECT, not just TCP.
			if r.err == nil && r.sshConn != nil {
				_ = r.sshConn.Close()
			} else {
				_ = tcpConn.Close()
			}
		default:
			_ = tcpConn.Close()
			<-done
		}
		return fmt.Errorf("ssh: handshake canceled: %w", ctx.Err())
	}
}

// Close shuts down the persistent client and stops the keepalive
// goroutine. Idempotent.
func (b *sshBackend) Close() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.client == nil {
		return nil
	}
	if b.keepaliveDone != nil {
		close(b.keepaliveDone)
		b.keepaliveDone = nil
	}
	err := b.client.Close()
	b.client = nil
	b.alive.Store(false)
	return err
}

// keepaliveLoop sends a keepalive on a ticker; first failure marks
// the backend not-alive (RunScript reconnects on the next call) and
// exits. wantReply=true turns the request into a round-trip so we
// notice dead connections.
func (b *sshBackend) keepaliveLoop(client *ssh.Client, done chan struct{}, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-done:
			return
		case <-ticker.C:
			if _, _, err := client.SendRequest("keepalive@openssh.com", true, nil); err != nil {
				b.alive.Store(false)
				return
			}
		}
	}
}

// reconnect tears down and reopens the client. Only called between
// RunScript calls -- never mid-call, because New-VM / Add-VM* side
// effects aren't idempotent and a partial cmdlet must not be retried.
func (b *sshBackend) reconnect(ctx context.Context) error {
	_ = b.Close()
	return b.Open(ctx)
}

// Healthcheck runs a trivial PowerShell round-trip to confirm the dial
// succeeded, the auth worked, and pwsh launches on the remote.
func (b *sshBackend) Healthcheck(ctx context.Context) error {
	res, err := b.RunScript(ctx, `'pong' | ConvertTo-Json -Compress`, nil)
	if err != nil {
		return err
	}
	if res.ExitCode != 0 {
		return fmt.Errorf("ssh healthcheck non-zero exit %d: %s", res.ExitCode, string(res.Stderr))
	}
	if !bytes.Contains(res.Stdout, []byte(`"pong"`)) {
		return fmt.Errorf("ssh healthcheck unexpected stdout: %q", string(res.Stdout))
	}
	return nil
}

// RunScript executes a PowerShell script on the remote host. The script
// body is staged as a temp file via SCP and executed with `powershell.exe
// -File`, leaving stdin free for input JSON. This sidesteps the effective
// command-line length cliff we hit with Windows OpenSSH (~1.3 KB on the
// test host, well below cmd.exe's documented 8191 limit) -- both raw
// `-EncodedCommand` of a typical preamble + verb script and a gzip+base64
// bootstrap variant exceeded that ceiling.
func (b *sshBackend) RunScript(ctx context.Context, script string, stdinJSON []byte) (Result, error) {
	b.mu.Lock()
	client := b.client
	b.mu.Unlock()
	if client == nil {
		return Result{}, errors.New("ssh: backend not open -- call Open first")
	}

	// Lazy reconnect if keepalive saw a dead client. Never retry
	// mid-call -- see reconnect's docstring.
	if !b.alive.Load() {
		if err := b.reconnect(ctx); err != nil {
			return Result{}, fmt.Errorf("ssh: reconnect after keepalive failure: %w", err)
		}
		b.mu.Lock()
		client = b.client
		b.mu.Unlock()
	}

	// Per-call timeout so a wedged remote cmdlet surfaces as
	// ErrTimeout. Note: ends the operator's wait, not the remote
	// process -- it keeps running until vmms unblocks.
	if b.opts.CommandTimeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, b.opts.CommandTimeout)
		defer cancel()
	}

	remotePath, cleanup, err := stageScript(ctx, client, script)
	if err != nil {
		return Result{}, fmt.Errorf("ssh: stage script: %w", err)
	}
	defer cleanup()

	session, err := client.NewSession()
	if err != nil {
		return Result{}, fmt.Errorf("ssh: open session: %w", err)
	}
	defer func() { _ = session.Close() }()

	cmd := fmt.Sprintf("%s -NoProfile -NonInteractive -ExecutionPolicy Bypass -File %s",
		b.opts.PwshPath, remotePath)

	if len(stdinJSON) > 0 {
		session.Stdin = bytes.NewReader(stdinJSON)
	}
	var stdout, stderr bytes.Buffer
	session.Stdout = &stdout
	session.Stderr = &stderr

	start := time.Now()
	runErr := runSessionWithCtx(ctx, session, cmd)
	duration := time.Since(start)

	if runErr != nil {
		if ctx.Err() != nil {
			return Result{}, fmt.Errorf("%w: %v", ErrTimeout, ctx.Err())
		}
		var exitErr *ssh.ExitError
		if errors.As(runErr, &exitErr) {
			return Result{
				Stdout:   stdout.Bytes(),
				Stderr:   stripCLIXML(stderr.Bytes()),
				ExitCode: exitErr.ExitStatus(),
				Duration: duration,
			}, nil
		}
		return Result{}, fmt.Errorf("ssh: run script: %w", runErr)
	}

	return Result{
		Stdout:   stdout.Bytes(),
		Stderr:   stripCLIXML(stderr.Bytes()),
		ExitCode: 0,
		Duration: duration,
	}, nil
}

// stageScript writes `script` to a remote temp file via SCP-over-SSH and
// returns the remote path plus a cleanup func that deletes the file. The
// returned path is the right argument for `powershell.exe -File`; stdin
// stays free for input JSON.
//
// Why SCP instead of -EncodedCommand: Windows OpenSSH server's exec channel
// silently truncates commands past ~1.3 KB on our test host (no error,
// command runs with garbled args, stdout empty). cmd.exe's 8191-char limit
// is a higher ceiling but the SSH layer is the bottleneck. Staging the
// body as a file removes the wire-size constraint entirely.
//
// The body is prefixed with a UTF-8 BOM so PS 5.1's `-File` reader picks
// the right encoding -- without it, 5.1 defaults to the system codepage
// (Windows-1252 on en-US) and corrupts any non-ASCII content. All current
// scripts are pure ASCII, but the BOM future-proofs.
func stageScript(ctx context.Context, client *ssh.Client, script string) (string, func(), error) {
	// 8 random bytes -- 64 bits of entropy is more than enough to avoid
	// collision when multiple resources apply concurrently against the same
	// backend. UnixNano() can collide if two goroutines hit it within the
	// same nanosecond; crypto/rand removes the wall-clock dependency.
	var suffix [8]byte
	if _, err := rand.Read(suffix[:]); err != nil {
		return "", nil, fmt.Errorf("ssh: generate temp filename: %w", err)
	}
	name := "hyperv-" + hex.EncodeToString(suffix[:]) + ".ps1"
	remotePath := `C:/Windows/Temp/` + name

	session, err := client.NewSession()
	if err != nil {
		return "", nil, fmt.Errorf("open scp session: %w", err)
	}
	stdinPipe, err := session.StdinPipe()
	if err != nil {
		_ = session.Close()
		return "", nil, fmt.Errorf("scp stdin pipe: %w", err)
	}
	var scpStderr bytes.Buffer
	session.Stderr = &scpStderr

	if err := session.Start(`scp -t C:/Windows/Temp`); err != nil {
		_ = session.Close()
		return "", nil, fmt.Errorf("scp start: %w", err)
	}

	// SCP sink protocol: `Cmmmm <size> <name>\n` then bytes then `\0`.
	body := append([]byte{0xEF, 0xBB, 0xBF}, script...)
	if _, err := fmt.Fprintf(stdinPipe, "C0644 %d %s\n", len(body), name); err != nil {
		_ = session.Close()
		return "", nil, fmt.Errorf("scp header: %w", err)
	}
	if _, err := stdinPipe.Write(body); err != nil {
		_ = session.Close()
		return "", nil, fmt.Errorf("scp body: %w", err)
	}
	if _, err := stdinPipe.Write([]byte{0}); err != nil {
		_ = session.Close()
		return "", nil, fmt.Errorf("scp eof: %w", err)
	}
	_ = stdinPipe.Close()

	// Race session.Wait against ctx so an apply-time cancel doesn't hang
	// on a slow remote write. Same pattern as Open's handshake guard.
	waitErr := make(chan error, 1)
	go func() { waitErr <- session.Wait() }()
	select {
	case err := <-waitErr:
		if err != nil {
			_ = session.Close()
			return "", nil, fmt.Errorf("scp wait: %w (stderr=%s)", err, scpStderr.String())
		}
	case <-ctx.Done():
		_ = session.Close()
		<-waitErr
		return "", nil, fmt.Errorf("scp canceled: %w", ctx.Err())
	}
	_ = session.Close()

	cleanup := func() {
		s, err := client.NewSession()
		if err != nil {
			return
		}
		defer func() { _ = s.Close() }()
		_ = s.Run(`cmd /c del "` + strings.ReplaceAll(remotePath, "/", `\`) + `"`)
	}
	return remotePath, cleanup, nil
}

// runSessionWithCtx wraps session.Run with ctx-cancel propagation. Cancel
// triggers a session.Close which causes the remote process to receive
// SIGHUP / connection-closed and exit; session.Run returns a transport
// error that the caller maps to ErrTimeout via ctx.Err() check.
func runSessionWithCtx(ctx context.Context, session *ssh.Session, cmd string) error {
	done := make(chan error, 1)
	go func() { done <- session.Run(cmd) }()

	select {
	case err := <-done:
		return err
	case <-ctx.Done():
		_ = session.Close()
		<-done // drain the run goroutine
		return ctx.Err()
	}
}

// buildSSHAuthMethods resolves the auth-method precedence: raw key bytes >
// key file path > password. Returns an empty slice if nothing is set;
// the caller turns that into a configuration error.
func buildSSHAuthMethods(opts SSHOptions) ([]ssh.AuthMethod, error) {
	var auths []ssh.AuthMethod

	if len(opts.PrivateKey) > 0 {
		signer, err := parsePrivateKey(opts.PrivateKey, opts.Passphrase)
		if err != nil {
			return nil, fmt.Errorf("ssh: parse private_key: %w", err)
		}
		auths = append(auths, ssh.PublicKeys(signer))
	} else if opts.PrivateKeyPath != "" {
		// #nosec G304 -- the path is operator-supplied (provider attribute
		// ssh.private_key_path or env HYPERV_SSH_PRIVATE_KEY_PATH), not
		// derived from untrusted input. The user explicitly told us to
		// read this file as their auth credential.
		keyBytes, err := os.ReadFile(opts.PrivateKeyPath)
		if err != nil {
			return nil, fmt.Errorf("ssh: read private_key_path %s: %w", opts.PrivateKeyPath, err)
		}
		signer, err := parsePrivateKey(keyBytes, opts.Passphrase)
		if err != nil {
			return nil, fmt.Errorf("ssh: parse %s: %w", opts.PrivateKeyPath, err)
		}
		auths = append(auths, ssh.PublicKeys(signer))
	}

	if opts.Password != "" {
		auths = append(auths, ssh.Password(opts.Password))
	}

	return auths, nil
}

// parsePrivateKey decodes an OpenSSH/PEM-encoded private key, optionally
// decrypting it with a passphrase.
func parsePrivateKey(keyBytes, passphrase []byte) (ssh.Signer, error) {
	if len(passphrase) > 0 {
		return ssh.ParsePrivateKeyWithPassphrase(keyBytes, passphrase)
	}
	return ssh.ParsePrivateKey(keyBytes)
}

// loadKnownHostsCallback returns a HostKeyCallback that verifies remote
// keys against the user's known_hosts file. An empty path resolves to
// ~/.ssh/known_hosts. Missing file is a fatal error -- silently
// disabling verification would be a security regression.
func loadKnownHostsCallback(path string) (ssh.HostKeyCallback, error) {
	if path == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return nil, fmt.Errorf("ssh: resolve home dir for known_hosts default: %w", err)
		}
		path = filepath.Join(home, ".ssh", "known_hosts")
	}
	cb, err := knownhosts.New(path)
	if err != nil {
		return nil, fmt.Errorf("ssh: load known_hosts %s: %w "+
			"(run `ssh-keyscan` first or set ssh.known_hosts_path)", path, err)
	}
	return cb, nil
}
