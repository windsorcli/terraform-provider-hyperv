package connection

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"sync"
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

	// Timeout is the dial timeout. Default 30s. RunScript-level timeouts
	// come from the caller's ctx, not this field.
	Timeout time.Duration

	// PwshPath is the binary the remote shell invokes per call. Default:
	// "powershell.exe" -- universally available on Windows. Set to "pwsh"
	// or "pwsh.exe" to prefer PS 7+ if installed.
	PwshPath string
}

const (
	defaultSSHPort     = 22
	defaultSSHTimeout  = 30 * time.Second
	defaultSSHPwshPath = "powershell.exe"
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
		opts:   SSHOptions{Host: opts.Host, Port: port, Username: opts.Username, PwshPath: pwshPath, Timeout: timeout},
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
		return nil
	case <-ctx.Done():
		// Closing the underlying TCP conn forces the in-flight read in
		// NewClientConn to return with an error; the goroutine then sends
		// to `done` (buffered, so no leak) and exits.
		_ = tcpConn.Close()
		<-done
		return fmt.Errorf("ssh: handshake canceled: %w", ctx.Err())
	}
}

// Close shuts down the persistent client. Idempotent.
func (b *sshBackend) Close() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.client == nil {
		return nil
	}
	err := b.client.Close()
	b.client = nil
	return err
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

// RunScript executes a PowerShell script on the remote host via a fresh
// SSH session. The script body is base64+UTF-16LE-encoded and passed via
// powershell.exe's -EncodedCommand flag, matching the local backend's
// wire shape and the spike #2 contract.
func (b *sshBackend) RunScript(ctx context.Context, script string, stdinJSON []byte) (Result, error) {
	b.mu.Lock()
	client := b.client
	b.mu.Unlock()
	if client == nil {
		return Result{}, errors.New("ssh: backend not open -- call Open first")
	}

	session, err := client.NewSession()
	if err != nil {
		return Result{}, fmt.Errorf("ssh: open session: %w", err)
	}
	defer func() { _ = session.Close() }()

	encoded := base64.StdEncoding.EncodeToString(utf16leBytes(script))
	// The remote shell parses this single string. Default shell on Windows
	// OpenSSH is cmd.exe; either way, the args after the binary name are
	// passed unmodified to powershell.exe / pwsh.exe.
	cmd := fmt.Sprintf("%s -NoProfile -NonInteractive -ExecutionPolicy Bypass -EncodedCommand %s",
		b.opts.PwshPath, encoded)

	if len(stdinJSON) > 0 {
		session.Stdin = bytes.NewReader(stdinJSON)
	}
	var stdout, stderr bytes.Buffer
	session.Stdout = &stdout
	session.Stderr = &stderr

	// Run the command with ctx-cancel support. session.Run doesn't take a
	// context, so we race ctx.Done() against the run completion in a
	// goroutine; ctx-cancel triggers session.Signal then session.Close to
	// terminate the remote process.
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
