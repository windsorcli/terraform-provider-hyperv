package connection

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/agent"
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
	//
	// All three sensitive fields use []byte so Close() can zero the
	// long-lived copy held by the backend. Libraries we hand the value
	// to (golang.org/x/crypto/ssh) make their own copies; zeroing here
	// covers the provider's own state, not theirs.
	PrivateKey     []byte
	PrivateKeyPath string
	Passphrase     []byte
	Password       []byte

	// KnownHostsPath is the file used for host key verification. Default:
	// ~/.ssh/known_hosts. Empty path falls back to the default. A missing
	// file is a hard error -- silently disabling host-key checking would be
	// a security regression.
	KnownHostsPath string

	// HostKey optionally pins a public key (authorized_keys format) or
	// SHA256 fingerprint. When empty, KnownHostsPath remains mandatory.
	HostKey string

	// UseSSHAgent adds identities exposed through SSH_AUTH_SOCK before
	// password fallback. It is opt-in and never weakens host verification.
	UseSSHAgent bool

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

	// MaxConcurrentSessions caps the number of in-flight RunScript calls
	// against the persistent ssh.Client. OpenSSH's per-connection
	// MaxSessions limit (default 10, often 4-6 on hardened Windows
	// builds) rejects late session opens with "rejected: connect failed
	// (open failed)" once exceeded. Default 4 stays well under typical
	// Windows OpenSSH caps; raise it on hosts with sshd_config tuned
	// upward, lower it if the bench surfaces the error under heavier
	// fanout. Set to 0 to use the default; negative values disable the
	// cap entirely (not recommended).
	MaxConcurrentSessions int
}

const (
	defaultSSHPort                  = 22
	defaultSSHTimeout               = 30 * time.Second
	defaultSSHCommandTimeout        = 5 * time.Minute
	defaultSSHKeepaliveInterval     = 30 * time.Second
	defaultSSHPwshPath              = "powershell.exe"
	defaultSSHMaxConcurrentSessions = 4
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

	// sem caps in-flight RunScript calls so the SSH backend stays
	// under OpenSSH's per-connection MaxSessions limit. Acquired at
	// the top of RunScript, released on return -- bounds both the
	// SCP staging session and the main exec session within one slot.
	// nil means uncapped (MaxConcurrentSessions < 0).
	sem chan struct{}

	// agentConn must remain open while agent-backed signers are in use.
	// It is separate from the SSH transport so reconnecting the transport
	// does not invalidate the authentication method.
	agentConn io.ReadWriteCloser
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

	auths, agentConn, err := buildSSHAuthMethods(opts)
	if err != nil {
		return nil, err
	}
	if len(auths) == 0 {
		return nil, errors.New("ssh: no auth method configured -- " +
			"set private_key, private_key_path, or password")
	}

	hostKeyCallback, err := loadHostKeyCallback(opts.HostKey, opts.KnownHostsPath)
	if err != nil {
		if agentConn != nil {
			_ = agentConn.Close()
		}
		return nil, err
	}

	pwshPath := opts.PwshPath
	if pwshPath == "" {
		pwshPath = defaultSSHPwshPath
	}

	maxSessions := opts.MaxConcurrentSessions
	if maxSessions == 0 {
		maxSessions = defaultSSHMaxConcurrentSessions
	}
	var sem chan struct{}
	if maxSessions > 0 {
		sem = make(chan struct{}, maxSessions)
	}

	cfg := &ssh.ClientConfig{
		User:            opts.Username,
		Auth:            auths,
		HostKeyCallback: hostKeyCallback,
		Timeout:         timeout,
	}

	return &sshBackend{
		opts: SSHOptions{
			Host:                  opts.Host,
			Port:                  port,
			Username:              opts.Username,
			PwshPath:              pwshPath,
			Timeout:               timeout,
			CommandTimeout:        commandTimeout,
			KeepaliveInterval:     keepalive,
			MaxConcurrentSessions: maxSessions,
		},
		config:    cfg,
		addr:      net.JoinHostPort(opts.Host, strconv.Itoa(port)),
		sem:       sem,
		agentConn: agentConn,
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
//
// No credential bytes to zero here -- they're consumed once in NewSSH
// (via buildSSHAuthMethods) and never carried onto the backend; see
// NewSSH for the zero-after-use hygiene path.
func (b *sshBackend) Close() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	var firstErr error
	if b.keepaliveDone != nil {
		close(b.keepaliveDone)
		b.keepaliveDone = nil
	}
	if b.client != nil {
		firstErr = b.client.Close()
		b.client = nil
	}
	if b.agentConn != nil {
		if err := b.agentConn.Close(); firstErr == nil {
			firstErr = err
		}
		b.agentConn = nil
	}
	b.alive.Store(false)
	return firstErr
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
	b.mu.Lock()
	if b.client != nil {
		_ = b.client.Close()
		b.client = nil
	}
	if b.keepaliveDone != nil {
		close(b.keepaliveDone)
		b.keepaliveDone = nil
	}
	b.alive.Store(false)
	b.mu.Unlock()
	return b.Open(ctx)
}

// newSessionWithRetry opens an ssh.Session with bounded retry on the
// transient "rejected: connect failed (open failed)" failure that
// OpenSSH returns when the per-connection MaxSessions cap is reached.
// The cap is concurrent, not cumulative, so a short backoff usually
// finds free capacity once peer sessions close. Backoff schedule
// 250ms, 500ms, 1s, 2s -- 5 attempts total cap at ~3.75s of waiting
// before the failure bubbles. NewSession itself is read-only on the
// remote (no cmdlet executes until Start/Run is called), so retry is
// safe regardless of script idempotency. ctx cancellation aborts the
// wait promptly.
func newSessionWithRetry(ctx context.Context, client *ssh.Client) (*ssh.Session, error) {
	delays := []time.Duration{
		250 * time.Millisecond,
		500 * time.Millisecond,
		1 * time.Second,
		2 * time.Second,
	}
	var lastErr error
	for attempt := 0; attempt <= len(delays); attempt++ {
		s, err := client.NewSession()
		if err == nil {
			return s, nil
		}
		if !isSessionRejected(err) {
			return nil, err
		}
		lastErr = err
		if attempt == len(delays) {
			break
		}
		select {
		case <-ctx.Done():
			return nil, fmt.Errorf("ssh: wait for session capacity: %w", ctx.Err())
		case <-time.After(delays[attempt]):
		}
	}
	return nil, fmt.Errorf("ssh: open session after %d attempts: %w", len(delays)+1, lastErr)
}

// isSessionRejected returns true for the OpenSSH "rejected: connect
// failed (open failed)" error that surfaces from NewSession when the
// server hits its MaxSessions cap. Match on the error-string
// substring rather than the concrete *ssh.OpenChannelError type --
// crypto/ssh has shifted the wrapping shape across releases, and the
// "open failed" suffix is the stable signal across all of them.
func isSessionRejected(err error) bool {
	if err == nil {
		return false
	}
	return strings.Contains(err.Error(), "open failed")
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

	// Cap in-flight sessions so the backend stays under the bench's
	// per-connection MaxSessions limit. The slot covers SCP staging,
	// the main exec session, and the deferred cleanup -- they run
	// sequentially within one RunScript, so one slot = one concurrent
	// SSH session against the server. Honor ctx so a canceled apply
	// doesn't wedge waiting on a slot.
	if b.sem != nil {
		select {
		case b.sem <- struct{}{}:
			defer func() { <-b.sem }()
		case <-ctx.Done():
			return Result{}, fmt.Errorf("ssh: wait for session slot: %w", ctx.Err())
		}
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

	session, err := newSessionWithRetry(ctx, client)
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
		// session.Wait() returns *ssh.ExitMissingError when the channel
		// closed without an exit-status reply -- typical for "the remote
		// process probably finished but the network blinked". Mark the
		// client dead so the next RunScript reconnects on its own (the
		// keepalive ticker would otherwise notice on its next interval,
		// up to keepaliveInterval seconds later -- a verify-on-drop
		// recovery path can't wait that long), and surface the typed
		// sentinel so typed-client methods can opt into a follow-up
		// verify when their cmdlet is idempotent.
		var exitMissing *ssh.ExitMissingError
		if errors.As(runErr, &exitMissing) {
			b.alive.Store(false)
			return Result{}, fmt.Errorf("ssh: run script: %w: %v", ErrSessionDropped, runErr)
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

	body := append([]byte{0xEF, 0xBB, 0xBF}, script...)
	if err := scpSink(ctx, client, "C:/Windows/Temp", name, int64(len(body)), bytes.NewReader(body)); err != nil {
		return "", nil, err
	}

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

// scpSink writes `size` bytes from `body` to `remoteDir/remoteName` via the
// SCP-sink protocol (`scp -t <dir>` on the server, sink-mode framing on
// stdin). The size is required up front because SCP's protocol carries a
// length prefix; callers that don't know the size in advance must buffer
// or stat the source first.
//
// Two callers today: stageScript (script body, in-memory bytes) and
// StreamFile (arbitrary local file, streamed via os.Open). Both pay the
// same scp-protocol round trip; the body io.Reader keeps memory pressure
// proportional to one pipe-buffer's worth of bytes regardless of payload
// size.
//
// ctx is honored on the session.Wait phase: a canceled apply unblocks
// promptly even if the remote disk is slow. Mid-Copy cancellation is
// indirect — the goroutine writing to stdinPipe returns when ctx fires
// only at the next scheduled Read, but for the workloads this primitive
// serves (multi-MB to multi-GB files), an io.Copy chunk completes well
// inside any user-perceptible delay.
func scpSink(ctx context.Context, client *ssh.Client, remoteDir, remoteName string, size int64, body io.Reader) error {
	session, err := newSessionWithRetry(ctx, client)
	if err != nil {
		return fmt.Errorf("ssh: open scp session: %w", err)
	}
	defer func() { _ = session.Close() }()

	// Use *Pipe accessors rather than session.Stdin/Stdout/Stderr
	// assignment. The library only spawns its internal copy goroutines
	// for the latter; with all three fields bound to pipes there is
	// nothing to drain, so we never call session.Wait (and don't hit
	// the Windows-OpenSSH wedge where the server occasionally fails to
	// send SSH_MSG_CHANNEL_EOF after exit-status). Reading the SCP
	// protocol's ACK bytes synchronously gives us the same delivery
	// guarantee Wait would have, without needing the channel to be
	// torn down by the server.
	stdinPipe, err := session.StdinPipe()
	if err != nil {
		return fmt.Errorf("ssh: scp stdin pipe: %w", err)
	}
	stdoutPipe, err := session.StdoutPipe()
	if err != nil {
		return fmt.Errorf("ssh: scp stdout pipe: %w", err)
	}
	// StderrPipe is called for its side effect only: it sets the
	// session's stderrpipe flag, which suppresses the default
	// io.Copy(io.Discard, channel.Stderr()) goroutine the library
	// would otherwise spawn. That goroutine reads until EOF; on the
	// Windows-OpenSSH wedge (no EOF after exit-status) it parks
	// forever -- the same deadlock pattern we just dismantled for
	// stdout. Returning the channel directly here means no goroutine
	// gets created, and the unread bytes just sit in the channel
	// buffer until the deferred session.Close drops them.
	if _, err := session.StderrPipe(); err != nil {
		return fmt.Errorf("ssh: scp stderr pipe: %w", err)
	}

	if err := session.Start(scpStartCmd(remoteDir)); err != nil {
		return fmt.Errorf("ssh: scp start: %w", err)
	}

	// Each ACK read is wrapped against ctx so a wedged SCP never
	// blocks the goroutine indefinitely -- closing the session on
	// ctx-fire causes the read to return EOF/error promptly. scp -t
	// emits three ACKs in sequence: initial-ready, post-header,
	// post-body terminator. 0x00 = OK, 0x01 = warning + textual
	// message until \n, 0x02 = fatal + textual message.
	readAck := func(stage string) error {
		var b [1]byte
		ackErr := make(chan error, 1)
		go func() {
			_, err := io.ReadFull(stdoutPipe, b[:])
			ackErr <- err
		}()
		select {
		case err := <-ackErr:
			if err != nil {
				return fmt.Errorf("ssh: scp %s ack read: %w", stage, err)
			}
		case <-ctx.Done():
			_ = session.Close()
			return fmt.Errorf("ssh: scp %s canceled: %w", stage, ctx.Err())
		}
		if b[0] == 0 {
			return nil
		}
		// Read the textual error message (warning/fatal) up to \n.
		// Bound the message length so a misbehaving server can't
		// stream gigabytes into our memory.
		const maxMsg = 4096
		msg := make([]byte, 0, 64)
		for len(msg) < maxMsg {
			var c [1]byte
			if _, err := io.ReadFull(stdoutPipe, c[:]); err != nil {
				break
			}
			if c[0] == '\n' {
				break
			}
			msg = append(msg, c[0])
		}
		return fmt.Errorf("ssh: scp %s ack=0x%02x msg=%q", stage, b[0], string(msg))
	}

	if err := readAck("initial"); err != nil {
		return err
	}

	// SCP sink protocol: `Cmmmm <size> <name>\n` then bytes then `\0`.
	if _, err := fmt.Fprintf(stdinPipe, "C0644 %d %s\n", size, remoteName); err != nil {
		return fmt.Errorf("ssh: scp header: %w", err)
	}
	if err := readAck("header"); err != nil {
		return err
	}
	if _, err := io.Copy(stdinPipe, body); err != nil {
		return fmt.Errorf("ssh: scp body: %w", err)
	}
	if _, err := stdinPipe.Write([]byte{0}); err != nil {
		return fmt.Errorf("ssh: scp eof: %w", err)
	}
	if err := readAck("body"); err != nil {
		return err
	}
	_ = stdinPipe.Close()
	return nil
}

// StreamFile copies localPath to remotePath via the SCP-sink primitive.
// The remote parent directory must already exist; SCP errors if the
// destination directory is missing. Resources that need parent-dir
// creation should issue a one-line `New-Item -ItemType Directory -Force`
// via RunScript before calling this.
//
// No wall-clock cap is applied -- transfers run as long as the payload
// requires. SSH keepalive (alive=false on a stalled session) and ctx
// cancellation are the remaining bounds.
func (b *sshBackend) StreamFile(ctx context.Context, localPath, remotePath string) error {
	b.mu.Lock()
	client := b.client
	b.mu.Unlock()
	if client == nil {
		return errors.New("ssh: backend not open -- call Open first")
	}

	// Lazy reconnect on dead client, mirroring RunScript's policy. Stream
	// is one-shot from the user's perspective; reconnecting between
	// applies is fine, mid-stream is not.
	if !b.alive.Load() {
		if err := b.reconnect(ctx); err != nil {
			return fmt.Errorf("ssh: reconnect after keepalive failure: %w", err)
		}
		b.mu.Lock()
		client = b.client
		b.mu.Unlock()
	}

	src, err := os.Open(localPath) // #nosec G304 -- localPath is operator-supplied via resource config
	if err != nil {
		return fmt.Errorf("ssh: open local %s: %w", localPath, err)
	}
	defer func() { _ = src.Close() }()

	info, err := src.Stat()
	if err != nil {
		return fmt.Errorf("ssh: stat local %s: %w", localPath, err)
	}

	remoteDir, remoteName := splitRemotePath(remotePath)
	if err := scpSink(ctx, client, remoteDir, remoteName, info.Size(), src); err != nil {
		return err
	}
	return nil
}

// scpStartCmd formats the `scp -t <dir>` command line with `<dir>`
// double-quoted so the remote shell (cmd.exe on Windows OpenSSH,
// /bin/sh on Linux) treats it as a single argument even when the
// path contains spaces. Embedded `"` is not handled because Windows
// filename rules forbid it and POSIX paths almost never carry it; if
// either changes the tests in ssh_test.go will surface the gap.
func scpStartCmd(remoteDir string) string {
	return `scp -t "` + remoteDir + `"`
}

// splitRemotePath separates an absolute Windows path (forward or back
// slashes) into a directory and a leaf filename for SCP-sink mode. SCP
// addresses the directory in `scp -t <dir>` and names the file in the
// `Cmmmm <size> <name>` header, so the two pieces ride separately on
// the wire.
func splitRemotePath(p string) (dir, name string) {
	norm := strings.ReplaceAll(p, "\\", "/")
	idx := strings.LastIndex(norm, "/")
	if idx < 0 {
		return ".", norm
	}
	return norm[:idx], norm[idx+1:]
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
//
// Credential hygiene: opts.PrivateKey, opts.Passphrase, opts.Password,
// and the locally-read keyBytes are zeroed before this function returns.
// golang.org/x/crypto/ssh has already copied the values into its
// auth-method closures by then; the library's copies stay live for the
// connection's lifetime but our own input slices (which the caller still
// holds via the shared underlying array) are scrubbed.
func buildSSHAuthMethods(opts SSHOptions) ([]ssh.AuthMethod, io.ReadWriteCloser, error) {
	defer zeroBytes(opts.PrivateKey)
	defer zeroBytes(opts.Passphrase)
	defer zeroBytes(opts.Password)

	var auths []ssh.AuthMethod

	if len(opts.PrivateKey) > 0 {
		signer, err := parsePrivateKey(opts.PrivateKey, opts.Passphrase)
		if err != nil {
			return nil, nil, fmt.Errorf("ssh: parse private_key: %w", err)
		}
		auths = append(auths, ssh.PublicKeys(signer))
	} else if opts.PrivateKeyPath != "" {
		// #nosec G304 -- the path is operator-supplied (provider attribute
		// ssh.private_key_path or env HYPERV_SSH_PRIVATE_KEY_PATH), not
		// derived from untrusted input. The user explicitly told us to
		// read this file as their auth credential.
		keyBytes, err := os.ReadFile(opts.PrivateKeyPath)
		if err != nil {
			return nil, nil, fmt.Errorf("ssh: read private_key_path %s: %w", opts.PrivateKeyPath, err)
		}
		defer zeroBytes(keyBytes)
		signer, err := parsePrivateKey(keyBytes, opts.Passphrase)
		if err != nil {
			return nil, nil, fmt.Errorf("ssh: parse %s: %w", opts.PrivateKeyPath, err)
		}
		auths = append(auths, ssh.PublicKeys(signer))
	}

	var agentConn io.ReadWriteCloser
	if opts.UseSSHAgent {
		socket := os.Getenv("SSH_AUTH_SOCK")
		if socket == "" {
			return nil, nil, errors.New("ssh: use_ssh_agent is true but SSH_AUTH_SOCK is not set")
		}
		var err error
		agentConn, err = dialSSHAgent(socket)
		if err != nil {
			return nil, nil, fmt.Errorf("ssh: connect to SSH agent: %w", err)
		}
		agentClient := agent.NewClient(agentConn)
		signers, err := agentClient.Signers()
		if err != nil {
			_ = agentConn.Close()
			return nil, nil, fmt.Errorf("ssh: list SSH agent identities: %w", err)
		}
		if len(signers) == 0 {
			_ = agentConn.Close()
			return nil, nil, errors.New("ssh: SSH agent contains no identities")
		}
		auths = append(auths, ssh.PublicKeys(signers...))
	}

	if len(opts.Password) > 0 {
		// ssh.Password takes a string; the library copies it into its
		// own auth-method closure. Our []byte is zeroed by the deferred
		// zeroBytes above; the library's copy is outside our reach.
		auths = append(auths, ssh.Password(string(opts.Password)))
	}

	return auths, agentConn, nil
}

// loadHostKeyCallback uses an explicit pin when supplied, otherwise the
// standard known_hosts verifier. Host verification is therefore always on.
func loadHostKeyCallback(hostKey, knownHostsPath string) (ssh.HostKeyCallback, error) {
	pin := strings.TrimSpace(hostKey)
	if pin == "" {
		return loadKnownHostsCallback(knownHostsPath)
	}
	if strings.HasPrefix(pin, "SHA256:") {
		return func(_ string, _ net.Addr, key ssh.PublicKey) error {
			if ssh.FingerprintSHA256(key) != pin {
				return fmt.Errorf("ssh: host key fingerprint mismatch: got %s", ssh.FingerprintSHA256(key))
			}
			return nil
		}, nil
	}
	want, _, _, _, err := ssh.ParseAuthorizedKey([]byte(pin))
	if err != nil {
		return nil, fmt.Errorf("ssh: parse host_key (expected OpenSSH public key or SHA256 fingerprint): %w", err)
	}
	return func(_ string, _ net.Addr, key ssh.PublicKey) error {
		if !bytes.Equal(want.Marshal(), key.Marshal()) {
			return errors.New("ssh: pinned host key mismatch")
		}
		return nil
	}, nil
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
