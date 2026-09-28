package connection

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf16"

	"github.com/bodgit/ntlmssp"
	"github.com/masterzen/winrm"
	"github.com/masterzen/winrm/soap"
)

// WinRMOptions configures the WinRM backend. The provider's Configure pass
// resolves env vars + provider attributes into this struct (see
// internal/provider/backend_select.go).
type WinRMOptions struct {
	Host     string // required
	Port     int    // default 5986 (HTTPS) or 5985 (HTTP)
	Username string // required
	// Password is []byte so Close() can zero the long-lived copy held
	// by the backend. masterzen/winrm copies the value into its own
	// EndpointParams when we construct the client; that copy is outside
	// our reach, but our state stays clean across the connection's life.
	Password []byte // required for ntlm/basic; for kerberos, mutually exclusive with KrbCCachePath

	UseHTTPS bool   // default true; flip to false only for diagnosing TLS-only failures
	Insecure bool   // skip TLS certificate verification (default false)
	Auth     string // "ntlm" | "basic" | "kerberos"; default "ntlm"
	CACert   string // path to a CA bundle PEM; empty = system roots

	// Kerberos fields, meaningful only when Auth=="kerberos" (the
	// provider-config layer catches a misconfigured Auth at plan time).
	// KrbRealm is required; KrbSpn defaults to "HTTP/<Host>";
	// KrbConfigPath defaults to the first existing of $KRB5_CONFIG,
	// ~/.config/krb5.conf, /etc/krb5.conf. Setting KrbCCachePath
	// switches from password-mode (inline AS-REQ) to ccache-mode
	// (reuse an existing TGT) and Password is then ignored.
	KrbRealm      string
	KrbSpn        string
	KrbConfigPath string
	KrbCCachePath string

	// Timeout sets `http.Client.Timeout` for every WSMan request.
	// Default 0 (no wall-clock cap) -- file transfers can run for
	// arbitrarily long. The initial TCP dial is bounded by the OS;
	// a connection that stalls mid-transfer (host freezes, packets
	// silently dropped) holds the apply until ctx cancellation
	// (operator Ctrl+C).
	Timeout time.Duration

	// CommandTimeout bounds a single RunScript call. Default 5m. A wedged
	// remote cmdlet surfaces as ErrTimeout instead of blocking the whole
	// apply. Set to 0 to disable.
	CommandTimeout time.Duration

	// PwshPath is the binary the remote shell invokes per call. Default:
	// "powershell.exe" -- universally available on Windows. Set to "pwsh"
	// or "pwsh.exe" to prefer PS 7+ if installed.
	PwshPath string

	// MaxShells caps the number of concurrent WinRM shells this backend
	// will open. Windows' default MaxShellsPerUser is 5; with Terraform's
	// default -parallelism=10 the provider can easily exceed that and
	// receive HTTP 400. Default: 3.
	MaxShells int
}

const (
	defaultWinRMPortHTTPS      = 5986
	defaultWinRMPortHTTP       = 5985
	defaultWinRMCommandTimeout = 5 * time.Minute
	defaultWinRMPwshPath       = "powershell.exe"
	defaultWinRMAuth           = "ntlm"
	defaultWinRMMaxShells      = 3
)

// winrmBackend implements Connection over the masterzen/winrm client. WinRM
// is HTTP-based with no persistent socket the way SSH has -- the upstream
// library opens a fresh request per command -- so Open's job is mostly
// validating that auth and TLS work end-to-end, and Close is a no-op.
type winrmBackend struct {
	opts   WinRMOptions
	client *winrm.Client

	mu       sync.Mutex
	opened   bool
	shellSem chan struct{} // bounds concurrent open shells; capacity = MaxShells
}

// Compile-time assertion.
var _ Connection = (*winrmBackend)(nil)

// NewWinRM builds a Connection backed by masterzen/winrm, validating
// required fields and applying defaults; the HTTP client itself is
// built lazily by Open, so a unit test exercising only NewWinRM
// doesn't pay that cost. Auth supports ntlm (default), basic, and
// kerberos; kerberos uses jcmturner/gokrb5 (pure-Go, no GSSAPI on the
// runner) in either password mode (inline AS-REQ) or ccache mode
// (reuse an existing TGT), picked by setting Password or
// KrbCCachePath; setting both, or neither, is rejected.
func NewWinRM(opts WinRMOptions) (Connection, error) {
	if opts.Host == "" {
		return nil, errors.New("winrm: host is required")
	}
	if opts.Username == "" {
		return nil, errors.New("winrm: username is required")
	}

	auth := opts.Auth
	if auth == "" {
		auth = defaultWinRMAuth
	}
	switch auth {
	case "ntlm", "basic":
		if len(opts.Password) == 0 {
			return nil, fmt.Errorf("winrm: %s auth requires a password", auth)
		}
	case "kerberos":
		if opts.KrbRealm == "" {
			return nil, errors.New("winrm: kerberos auth requires kerberos.realm")
		}
		// Password XOR ccache: exactly one credential source, ambiguous or absent otherwise.
		hasPassword := len(opts.Password) > 0
		hasCCache := opts.KrbCCachePath != ""
		if hasPassword && hasCCache {
			return nil, errors.New("winrm: kerberos auth: password and kerberos.ccache_path are mutually exclusive (pick one)")
		}
		if !hasPassword && !hasCCache {
			return nil, errors.New("winrm: kerberos auth requires either password or kerberos.ccache_path")
		}
	default:
		return nil, fmt.Errorf("winrm: unknown auth %q (expected ntlm | basic | kerberos)", auth)
	}

	port := opts.Port
	if port == 0 {
		if opts.UseHTTPS {
			port = defaultWinRMPortHTTPS
		} else {
			port = defaultWinRMPortHTTP
		}
	}
	if port < 1 || port > 65535 {
		return nil, fmt.Errorf("winrm: port must be between 1 and 65535; got %d", port)
	}

	commandTimeout := opts.CommandTimeout
	if commandTimeout == 0 {
		commandTimeout = defaultWinRMCommandTimeout
	}
	timeout := opts.Timeout

	pwshPath := opts.PwshPath
	if pwshPath == "" {
		pwshPath = defaultWinRMPwshPath
	}

	maxShells := opts.MaxShells
	if maxShells == 0 {
		maxShells = defaultWinRMMaxShells
	}

	// Kerberos-only defaults; masterzen ignores these fields for ntlm/basic, so passing them through is harmless.
	krbSpn := opts.KrbSpn
	if auth == "kerberos" && krbSpn == "" {
		krbSpn = "HTTP/" + opts.Host
	}
	krbConfigPath := opts.KrbConfigPath
	if auth == "kerberos" && krbConfigPath == "" {
		krbConfigPath = defaultKrbConfigPath()
	}

	return &winrmBackend{
		opts: WinRMOptions{
			Host:           opts.Host,
			Port:           port,
			Username:       opts.Username,
			Password:       opts.Password,
			UseHTTPS:       opts.UseHTTPS,
			Insecure:       opts.Insecure,
			Auth:           auth,
			CACert:         opts.CACert,
			KrbRealm:       opts.KrbRealm,
			KrbSpn:         krbSpn,
			KrbConfigPath:  krbConfigPath,
			KrbCCachePath:  opts.KrbCCachePath,
			Timeout:        timeout,
			CommandTimeout: commandTimeout,
			PwshPath:       pwshPath,
			MaxShells:      maxShells,
		},
		shellSem: make(chan struct{}, maxShells),
	}, nil
}

// defaultKrbConfigPath probes KRB5_CONFIG, then ~/.config/krb5.conf,
// then /etc/krb5.conf, returning the first existing path or empty. An
// empty result surfaces as masterzen/winrm's own "open <empty>: no
// such file" error at first auth attempt, a reasonable signal that
// nothing was set and auto-detect failed.
func defaultKrbConfigPath() string {
	if p := os.Getenv("KRB5_CONFIG"); p != "" {
		return p
	}
	if home, err := os.UserHomeDir(); err == nil {
		userPath := filepath.Join(home, ".config", "krb5.conf")
		if _, err := os.Stat(userPath); err == nil {
			return userPath
		}
	}
	const systemPath = "/etc/krb5.conf"
	if _, err := os.Stat(systemPath); err == nil {
		return systemPath
	}
	return ""
}

// Backend returns the lowercase identifier used for tflog field decoration.
func (b *winrmBackend) Backend() string { return "winrm" }

// Open constructs the masterzen/winrm Client and runs a Healthcheck
// round-trip so misconfiguration (auth failure, wrong port, untrusted
// cert) surfaces at provider-Configure time rather than mid-plan
// during a Read. Idempotent -- subsequent calls return nil if the
// client is already up.
func (b *winrmBackend) Open(ctx context.Context) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.opened {
		return nil
	}

	endpoint := winrm.NewEndpoint(
		b.opts.Host,
		b.opts.Port,
		b.opts.UseHTTPS,
		b.opts.Insecure,
		nil, // CACert bytes -- populated below from disk
		nil, // Cert
		nil, // Key
		b.opts.Timeout,
	)

	if b.opts.CACert != "" {
		// #nosec G304 -- operator-supplied path (winrm.cacert or HYPERV_WINRM_CACERT), not untrusted input.
		caBytes, err := os.ReadFile(b.opts.CACert)
		if err != nil {
			return fmt.Errorf("winrm: read cacert %s: %w", b.opts.CACert, err)
		}
		// Sanity-check PEM parsing so a typo surfaces here, not as an opaque TLS handshake error later.
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(caBytes) {
			return fmt.Errorf("winrm: cacert %s contains no PEM certificates", b.opts.CACert)
		}
		endpoint.CACert = caBytes
	}

	params := buildWinRMParams(b.opts)

	// masterzen copies the password into its own EndpointParams; our []byte stays canonical, zeroed at Close().
	client, err := winrm.NewClientWithParameters(endpoint, b.opts.Username, string(b.opts.Password), params)
	if err != nil {
		return fmt.Errorf("winrm: build client: %w", err)
	}
	b.client = client

	if err := b.healthcheckLocked(ctx); err != nil {
		// Not marked opened on a failed healthcheck, so a subsequent Open reattempts cleanly.
		b.client = nil
		return err
	}
	b.opened = true
	return nil
}

// Close releases the backend's persistent state and zeros the held
// password bytes; idempotent. Zeroing makes the backend non-reusable,
// since Open reads b.opts.Password and would silently auth with
// zeroed bytes afterward. That's fine given the provider's
// single-Configure, single-Close lifecycle, but a future
// reconnect-on-failure path must rebuild via NewWinRM instead of
// calling Open again here.
func (b *winrmBackend) Close() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.client = nil
	b.opened = false
	zeroBytes(b.opts.Password)
	return nil
}

// acquireShell blocks until a shell slot is available in the semaphore or
// ctx is canceled. Must be paired with releaseShell.
func (b *winrmBackend) acquireShell(ctx context.Context) error {
	select {
	case b.shellSem <- struct{}{}:
		return nil
	case <-ctx.Done():
		return fmt.Errorf("winrm: waiting for shell slot: %w", ctx.Err())
	}
}

func (b *winrmBackend) releaseShell() { <-b.shellSem }

// runShellCmd executes a single command on an already-open shell, optionally
// piping stdin, and drains stdout/stderr to the provided writers. Returns the
// exit code and any transport error. A non-zero exit code is NOT surfaced as
// an error here — callers check it themselves.
func runShellCmd(ctx context.Context, shell *winrm.Shell, cmd string, stdin io.Reader, stdout, stderr io.Writer) (int, error) {
	winrmCmd, err := shell.ExecuteWithContext(ctx, cmd)
	if err != nil {
		return 1, err
	}
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		_, _ = io.Copy(stdout, winrmCmd.Stdout)
	}()
	go func() {
		defer wg.Done()
		_, _ = io.Copy(stderr, winrmCmd.Stderr)
	}()
	if stdin != nil {
		_, _ = io.Copy(winrmCmd.Stdin, stdin)
		_ = winrmCmd.Stdin.Close()
	}
	wg.Wait()
	if err := winrmCmd.Error(); err != nil {
		return 1, err
	}
	return winrmCmd.ExitCode(), nil
}

// StreamFile copies localPath to remotePath by streaming base64 over
// the remote PowerShell process's stdin, encoded chunk-by-chunk on the
// runner and decoded line-by-line by a receiver script on the host, so
// memory use stays constant regardless of file size. WinRM's transport
// adds ~33% encoding overhead and runs roughly 10x slower than the SSH
// backend's SCP path; prefer SSH or host_path-mode for multi-GiB
// artifacts. The remote parent directory must already exist; the
// receiver doesn't create it.
func (b *winrmBackend) StreamFile(ctx context.Context, localPath, remotePath string) error {
	b.mu.Lock()
	client := b.client
	opened := b.opened
	b.mu.Unlock()
	if !opened || client == nil {
		return errors.New("winrm: backend not open -- call Open first")
	}

	src, err := os.Open(localPath) // #nosec G304 -- localPath is operator-supplied via resource config
	if err != nil {
		return fmt.Errorf("winrm: open local %s: %w", localPath, err)
	}
	defer func() { _ = src.Close() }()

	// Close order matters: enc flushes into lw before lw's newline into bufW, before bufW flushes into pw.
	pr, pw := io.Pipe()
	bufW := bufio.NewWriterSize(pw, streamFileBufSize)
	lw := newLineWrappedWriter(bufW, base64LineLen)
	enc := base64.NewEncoder(base64.StdEncoding, lw)

	go func() {
		err := func() error {
			if _, err := io.Copy(enc, &ctxReader{ctx: ctx, r: src}); err != nil {
				return err
			}
			if err := enc.Close(); err != nil {
				return err
			}
			if err := lw.Close(); err != nil {
				return err
			}
			return bufW.Flush()
		}()
		if err != nil {
			_ = pw.CloseWithError(err)
		} else {
			_ = pw.Close()
		}
	}()

	cmdStr := fmt.Sprintf("%s -NoProfile -NonInteractive -EncodedCommand %s",
		b.opts.PwshPath, encodePSScript(buildWinRMStreamFileScript(remotePath)))

	// Own loop, not RunWithContextWithInput: that discards stdin write errors, causing silent truncation.
	if err := b.acquireShell(ctx); err != nil {
		return err
	}
	defer b.releaseShell()

	shell, err := client.CreateShell()
	if err != nil {
		return fmt.Errorf("winrm: create shell: %w", err)
	}
	defer func() { _ = shell.Close() }()

	winrmCmd, err := shell.ExecuteWithContext(ctx, cmdStr)
	if err != nil {
		return fmt.Errorf("winrm: execute: %w", err)
	}

	var stdout, stderr bytes.Buffer
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		_, _ = io.Copy(&stdout, winrmCmd.Stdout)
	}()
	go func() {
		defer wg.Done()
		_, _ = io.Copy(&stderr, winrmCmd.Stderr)
	}()

	bufferedPr := bufio.NewReaderSize(pr, streamFileBufSize)
	stdinErr := streamFileStdin(ctx, winrmCmd.Stdin, bufferedPr)
	_ = pr.Close() // unblock encoder goroutine if we exited early
	_ = winrmCmd.Stdin.Close()

	// Wait() can block forever if the host hangs; a goroutine plus timeout surfaces a clean error instead.
	const streamWaitTimeout = 10 * time.Minute
	waitDone := make(chan struct{})
	go func() {
		winrmCmd.Wait()
		wg.Wait()
		_ = winrmCmd.Close()
		close(waitDone)
	}()

	timer := time.NewTimer(streamWaitTimeout)
	defer timer.Stop()

	select {
	case <-waitDone:
		// normal completion -- fall through to error checks below
	case <-ctx.Done():
		return fmt.Errorf("%w: context cancelled waiting for stream-file script: %v", ErrTimeout, ctx.Err())
	case <-timer.C:
		return fmt.Errorf("winrm: stream-file script did not complete within %v (host may be unresponsive)", streamWaitTimeout)
	}

	if stdinErr != nil {
		return fmt.Errorf("winrm: stream file stdin: %w", stdinErr)
	}
	if code := winrmCmd.ExitCode(); code != 0 {
		if ctx.Err() != nil {
			return fmt.Errorf("%w: %v", ErrTimeout, ctx.Err())
		}
		return fmt.Errorf("winrm: stream file exit %d: %s", code, stderr.String())
	}
	return nil
}

// streamFileStdin feeds r into w chunk by chunk, handling partial writes
// via streamFileWriteAll. Context cancellation is checked between chunks.
func streamFileStdin(ctx context.Context, w io.Writer, r io.Reader) error {
	buf := make([]byte, streamFileBufSize)
	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		n, readErr := r.Read(buf)
		if n > 0 {
			if err := streamFileWriteAll(w, buf[:n]); err != nil {
				return err
			}
		}
		if readErr == io.EOF {
			return nil
		}
		if readErr != nil {
			return readErr
		}
	}
}

// streamFileWriteAll writes all of data to w. When Write returns a partial
// count (io.ErrShortWrite from a failed WinRM sendInput), it advances past
// the confirmed bytes and retries. Zero-progress failures (nw == 0) are
// retried with exponential backoff up to streamFileMaxSendRetries times
// before returning an error.
func streamFileWriteAll(w io.Writer, data []byte) error {
	zeroProgress := 0
	for len(data) > 0 {
		nw, err := w.Write(data)
		if nw > 0 {
			data = data[nw:]
			zeroProgress = 0
		}
		if err == nil {
			continue
		}
		zeroProgress++
		if zeroProgress > streamFileMaxSendRetries {
			return fmt.Errorf("stdin send stalled after %d zero-progress errors: %w", zeroProgress-1, err)
		}
		// Exponential backoff: 250 ms, 500 ms, 1 s, 2 s, 4 s, …
		time.Sleep(time.Duration(1<<(zeroProgress-1)) * 250 * time.Millisecond)
	}
	return nil
}

// streamFileMaxSendRetries is the number of consecutive zero-progress Write
// calls (each sendInput sent zero bytes) before streamFileWriteAll gives up.
const streamFileMaxSendRetries = 5

// base64LineLen is the line length the line-wrapping writer inserts
// newlines at. 76 is the MIME-canonical width and a multiple of 4, so
// each line is a self-contained base64 string the receiver can
// FromBase64String without joining lines.
const base64LineLen = 76

// streamFileBufSize controls two buffers in StreamFile:
//   - bufio.Writer on the write side: coalesces 76-byte base64 line writes
//     into large pipe flushes, so commandWriter.Write sees chunks near this
//     size rather than one tiny write per base64 line.
//   - bufio.Reader on the read side: provides large chunks to streamFileStdin
//     so each Read gives commandWriter.Write a full envelope's worth of data.
//
// Size budget (stock WS2019/2022 host, MaxEnvelopeSizekb = 500):
//
//	envelope ceiling    512 000 bytes  (500 × 1024)
//	NTLM + SOAP framing  ~40 000 bytes  (multipart boundaries, sig, XML)
//	second base64 ratio      × 4/3
//
//	bufSize × 4/3 + 40 000 ≤ 512 000
//	bufSize ≤ (512 000 − 40 000) × 3/4 ≈ 354 000 bytes
//
// 256 KB (262 144 bytes) gives an on-wire payload of ~341 KB and leaves
// ~130 KB of headroom, conservative enough to survive hosts with a
// slightly lower effective limit due to NTLM session overhead.
//
// lint:allow-long-comment
const streamFileBufSize = 256 * 1024

// buildWinRMStreamFileScript emits a single-statement PS body that reads
// newline-delimited base64 from stdin and writes the decoded bytes to
// remotePath. Single-line so it stays well under MaxCommandLine even
// before -EncodedCommand wrapping. Any single quote in remotePath is
// doubled to escape the single-quoted PS string -- standard PS escaping.
func buildWinRMStreamFileScript(remotePath string) string {
	escaped := strings.ReplaceAll(remotePath, "'", "''")
	return `[Console]::InputEncoding = [Text.UTF8Encoding]::new($false); ` +
		`$stream = [IO.File]::OpenWrite('` + escaped + `'); ` +
		`try { $stream.SetLength(0); $reader = [Console]::In; ` +
		`while ($null -ne ($line = $reader.ReadLine())) { ` +
		`if ($line.Length -gt 0) { $bytes = [Convert]::FromBase64String($line); ` +
		`$stream.Write($bytes, 0, $bytes.Length) } } } ` +
		`finally { $stream.Dispose() }`
}

// lineWrappedWriter inserts a newline after every lineLen bytes written
// to the underlying writer, breaking a continuous base64 stream into
// per-line chunks so the WinRM receive script can decode each line
// independently via ReadLine + FromBase64String, keeping host memory
// proportional to one line rather than the whole payload. Close emits
// a trailing newline if the last line is partial; the PS receiver's
// loop terminates on ReadLine returning $null (EOF), so a missing
// trailing newline doesn't corrupt the stream, but emitting it keeps
// the wire format consistent and tested.
type lineWrappedWriter struct {
	w       io.Writer
	lineLen int
	written int // bytes written to the current line
}

func newLineWrappedWriter(w io.Writer, lineLen int) *lineWrappedWriter {
	return &lineWrappedWriter{w: w, lineLen: lineLen}
}

func (l *lineWrappedWriter) Write(p []byte) (int, error) {
	var written int
	for len(p) > 0 {
		remain := l.lineLen - l.written
		if remain == 0 {
			if _, err := l.w.Write([]byte{'\n'}); err != nil {
				return written, err
			}
			l.written = 0
			remain = l.lineLen
		}
		n := remain
		if n > len(p) {
			n = len(p)
		}
		m, err := l.w.Write(p[:n])
		written += m
		l.written += m
		p = p[n:]
		if err != nil {
			return written, err
		}
	}
	return written, nil
}

func (l *lineWrappedWriter) Close() error {
	if l.written > 0 {
		if _, err := l.w.Write([]byte{'\n'}); err != nil {
			return err
		}
		l.written = 0
	}
	return nil
}

// ntlmEncryptionTransporter implements winrm.Transporter for NTLM-
// authenticated, message-level encrypted WinRM sessions over HTTP. It
// keeps a persistent NTLM session so the full 3-way handshake is paid
// once per connection, not on every Post(): without reuse, each 256KB
// chunk costs 4-5 RTTs of NTLM overhead, turning a 335MB transfer over
// a ~300ms-RTT VPN link into 30+ minutes instead of ~3.
//
// The first Post() does a raw-TCP 3-way handshake (dial a raw
// net.Conn, Type1/Type2/Type3, then encrypted SOAP on the same
// socket); raw sockets matter because http.Transport can silently
// retry on a fresh connection when the idle one closes, landing the
// Type3 token on a connection with no NTLM context. The authenticated
// conn is then promoted into the fast-path httpTransport via
// injectedConn, so later calls skip the handshake (one RTT); any
// auth/transport error invalidates the session and retries the full
// handshake once. The mutex guards session state, so Post() is safe
// for concurrent callers.
//
// bodgit/ntlmssp negotiates the full NTLM flag set (SIGN/SEAL/
// KEY_EXCHANGE); Azure/go-ntlmssp negotiates a reduced set that
// Windows Server 2019 rejects.
//
// lint:allow-long-comment
type ntlmEncryptionTransporter struct {
	username string
	password string
	endpoint *winrm.Endpoint

	mu            sync.Mutex
	httpClient    *http.Client             // fast-path: sends encrypted SOAP on keep-alive conn
	httpTransport *http.Transport          // underlying transport; DialContext injects handshake conn
	injectedConn  *bufReaderConn           // authenticated conn to inject on first fast-path dial
	sessionReady  bool                     // true once slow path succeeded at least once
	session       *ntlmssp.SecuritySession // session keys for encrypt/decrypt
	endpointURL   string
}

// bufReaderConn wraps a net.Conn with a bufio.Reader so that bytes that were
// pre-read during the NTLM handshake (into br's internal buffer) are not lost
// when the connection is handed to http.Transport.
type bufReaderConn struct {
	net.Conn
	br *bufio.Reader
}

func (c *bufReaderConn) Read(b []byte) (int, error) { return c.br.Read(b) }

func (t *ntlmEncryptionTransporter) Transport(endpoint *winrm.Endpoint) error {
	t.endpoint = endpoint
	scheme := "http"
	if endpoint.HTTPS {
		scheme = "https"
	}
	t.endpointURL = (&url.URL{
		Scheme: scheme,
		Host:   net.JoinHostPort(endpoint.Host, strconv.Itoa(endpoint.Port)),
		Path:   "/wsman",
	}).String()
	return nil
}

// invalidateSession drops the cached NTLM session. Must be called with t.mu held.
func (t *ntlmEncryptionTransporter) invalidateSession() {
	if t.httpTransport != nil {
		t.httpTransport.CloseIdleConnections()
	}
	if t.injectedConn != nil {
		_ = t.injectedConn.Close()
		t.injectedConn = nil
	}
	t.httpTransport = nil
	t.httpClient = nil
	t.sessionReady = false
	t.session = nil
}

// newNTLMClient creates a fresh ntlmssp.Client from the transporter's stored
// credentials. Called once per slow-path handshake so each handshake uses an
// independent state machine — the ntlmssp.Client is not goroutine-safe across
// concurrent Authenticate calls, so sharing it between simultaneous re-auth
// attempts would corrupt the Type3 token.
func (t *ntlmEncryptionTransporter) newNTLMClient() (*ntlmssp.Client, error) {
	var userName, domain string
	if idx := strings.Index(t.username, "@"); idx >= 0 {
		userName, domain = t.username[:idx], t.username[idx+1:]
	} else if idx := strings.Index(t.username, `\`); idx >= 0 {
		domain, userName = t.username[:idx], t.username[idx+1:]
	} else {
		userName = t.username
	}
	c, err := ntlmssp.NewClient(
		ntlmssp.SetUserInfo(userName, t.password),
		ntlmssp.SetDomain(domain),
		ntlmssp.SetVersion(ntlmssp.DefaultVersion()),
	)
	if err != nil {
		return nil, fmt.Errorf("winrm: create ntlm client: %w", err)
	}
	return c, nil
}

// ensureClients initialises the HTTP transport and client if they have not been
// created yet (or were dropped by invalidateSession). No I/O is performed here;
// the actual NTLM handshake is deferred to postOnce's slow path.
// Must be called with t.mu held.
func (t *ntlmEncryptionTransporter) ensureClients() {
	if t.httpTransport != nil {
		return
	}

	tlsCfg := &tls.Config{InsecureSkipVerify: t.endpoint.Insecure} // #nosec G402
	if len(t.endpoint.CACert) > 0 {
		pool := x509.NewCertPool()
		pool.AppendCertsFromPEM(t.endpoint.CACert)
		tlsCfg.RootCAs = pool
	}

	// Injects the slow-path handshake's authenticated conn so the first fast-path request reuses it.
	dialer := &net.Dialer{Timeout: 30 * time.Second, KeepAlive: 30 * time.Second}
	httpTransport := &http.Transport{
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			t.mu.Lock()
			c := t.injectedConn
			if c != nil {
				t.injectedConn = nil
			}
			t.mu.Unlock()
			if c != nil {
				return c, nil
			}
			return dialer.DialContext(ctx, network, addr)
		},
		TLSClientConfig:       tlsCfg,
		ResponseHeaderTimeout: maxDuration(t.endpoint.Timeout, 5*time.Minute),
	}

	t.httpTransport = httpTransport
	t.httpClient = &http.Client{Transport: httpTransport}
}

// dialRaw opens a raw TCP (or TLS) connection to the WinRM endpoint.
func (t *ntlmEncryptionTransporter) dialRaw() (net.Conn, error) {
	addr := net.JoinHostPort(t.endpoint.Host, strconv.Itoa(t.endpoint.Port))
	if t.endpoint.HTTPS {
		tlsCfg := &tls.Config{InsecureSkipVerify: t.endpoint.Insecure} // #nosec G402
		if len(t.endpoint.CACert) > 0 {
			pool := x509.NewCertPool()
			pool.AppendCertsFromPEM(t.endpoint.CACert)
			tlsCfg.RootCAs = pool
		}
		return tls.DialWithDialer(&net.Dialer{Timeout: 30 * time.Second}, "tcp", addr, tlsCfg)
	}
	return net.DialTimeout("tcp", addr, 30*time.Second)
}

func (t *ntlmEncryptionTransporter) Post(_ *winrm.Client, message *soap.SoapMessage) (string, error) {
	result, err := t.postOnce(message)
	if err != nil {
		// Auth errors may mean a dropped/expired session; invalidate and retry once.
		t.mu.Lock()
		t.invalidateSession()
		t.mu.Unlock()
		result, err = t.postOnce(message)
	}
	return result, err
}

// postOnce sends a single SOAP message via one of two paths: the slow
// path (sessionReady=false) dials a raw net.Conn and does the 3-way
// NTLM handshake manually, injecting the authenticated conn into
// httpTransport so the next fast-path call skips re-dialing; the fast
// path (sessionReady=true) sends encrypted SOAP directly on the
// keep-alive connection in one RTT, returning any error so Post() can
// invalidate and retry via the slow path.
func (t *ntlmEncryptionTransporter) postOnce(message *soap.SoapMessage) (string, error) {
	t.mu.Lock()
	t.ensureClients()
	sessionReady := t.sessionReady
	httpClient := t.httpClient
	session := t.session
	t.mu.Unlock()

	soapBytes := []byte(message.String())
	encCT := `multipart/encrypted;protocol="application/HTTP-SPNEGO-session-encrypted";boundary="Encrypted Boundary"`

	if !sessionReady {
		// Fresh client per call: ntlmssp.Client isn't safe for concurrent Authenticate, corrupting the Type3 token.
		ntlmClient, err := t.newNTLMClient()
		if err != nil {
			return "", err
		}

		rawConn, err := t.dialRaw()
		if err != nil {
			return "", fmt.Errorf("winrm: ntlm dial: %w", err)
		}
		closeConn := true
		defer func() {
			if closeConn {
				_ = rawConn.Close()
			}
		}()
		br := bufio.NewReader(rawConn)

		// Step 1: Type1 Negotiate.
		type1, err := ntlmClient.Authenticate(nil, nil)
		if err != nil {
			return "", fmt.Errorf("winrm: ntlm negotiate: %w", err)
		}

		// req.Write/http.ReadResponse on the raw socket keeps the challenge and Type3+SOAP on the same conn.
		req1, err := http.NewRequest(http.MethodPost, t.endpointURL, nil)
		if err != nil {
			return "", fmt.Errorf("winrm: build challenge request: %w", err)
		}
		req1.Header.Set("Content-Type", "application/soap+xml;charset=UTF-8")
		req1.Header.Set("Connection", "Keep-Alive")
		req1.Header.Set("User-Agent", "WinRM client")
		req1.Header.Set("Authorization", "Negotiate "+base64.StdEncoding.EncodeToString(type1))
		if err := req1.Write(rawConn); err != nil {
			return "", fmt.Errorf("winrm: ntlm type1 write: %w", err)
		}
		resp1, err := http.ReadResponse(br, req1)
		if err != nil {
			return "", fmt.Errorf("winrm: ntlm type1 read: %w", err)
		}
		_, _ = io.Copy(io.Discard, resp1.Body)
		_ = resp1.Body.Close()
		if resp1.StatusCode != http.StatusUnauthorized {
			return "", fmt.Errorf("winrm: expected 401 for NTLM challenge, got %d", resp1.StatusCode)
		}
		challenge, err := extractNTLMChallenge(resp1)
		if err != nil {
			return "", err
		}

		// Step 3: Type3 Authenticate + derive session keys.
		type3, err := ntlmClient.Authenticate(challenge, nil)
		if err != nil {
			return "", fmt.Errorf("winrm: ntlm authenticate: %w", err)
		}
		session = ntlmClient.SecuritySession()
		if session == nil {
			return "", fmt.Errorf("winrm: ntlm session not established after authenticate")
		}

		// Encrypt SOAP with the fresh session keys.
		sealed, signature, err := session.Wrap(soapBytes)
		if err != nil {
			return "", fmt.Errorf("winrm: encrypt soap: %w", err)
		}
		encBody := buildWinRMEncryptedBody(soapBytes, sealed, signature)

		// Step 4: write Type3 + encrypted SOAP on the SAME rawConn.
		req2, err := http.NewRequest(http.MethodPost, t.endpointURL, bytes.NewReader(encBody))
		if err != nil {
			return "", fmt.Errorf("winrm: build soap request: %w", err)
		}
		req2.Header.Set("Content-Type", encCT)
		req2.Header.Set("Connection", "Keep-Alive")
		req2.Header.Set("User-Agent", "WinRM client")
		req2.Header.Set("Authorization", "Negotiate "+base64.StdEncoding.EncodeToString(type3))
		if err := req2.Write(rawConn); err != nil {
			return "", fmt.Errorf("winrm: type3+soap write: %w", err)
		}
		resp2, err := http.ReadResponse(br, req2)
		if err != nil {
			return "", fmt.Errorf("winrm: type3+soap read: %w", err)
		}
		defer func() { _ = resp2.Body.Close() }()

		respBody, err := io.ReadAll(resp2.Body)
		if err != nil {
			return "", fmt.Errorf("winrm: read soap response: %w", err)
		}

		respCT := resp2.Header.Get("Content-Type")
		result, err := t.processSoapResponse(resp2.StatusCode, respCT, respBody, session)
		if err != nil {
			return "", fmt.Errorf("winrm: processSoapResponse: %w", err)
		}

		// Promotes to fast path: the next httpClient.Do picks this up via the custom DialContext.
		wrapped := &bufReaderConn{Conn: rawConn, br: br}
		closeConn = false // transfer ownership; don't close in defer
		t.mu.Lock()
		if t.injectedConn != nil {
			_ = t.injectedConn.Close()
		}
		t.injectedConn = wrapped
		t.session = session
		t.sessionReady = true
		t.mu.Unlock()
		return result, nil
	}

	// --- Fast path: reuse session keys + keep-alive connection ---
	sealed, signature, err := session.Wrap(soapBytes)
	if err != nil {
		return "", fmt.Errorf("winrm: encrypt soap: %w", err)
	}
	encBody := buildWinRMEncryptedBody(soapBytes, sealed, signature)

	soapReq, err := http.NewRequest(http.MethodPost, t.endpointURL, bytes.NewReader(encBody))
	if err != nil {
		return "", fmt.Errorf("winrm: build soap request: %w", err)
	}
	soapReq.Header.Set("Content-Type", encCT)
	soapReq.Header.Set("Connection", "Keep-Alive")
	soapReq.Header.Set("User-Agent", "WinRM client")

	soapResp, err := httpClient.Do(soapReq)
	if err != nil {
		return "", fmt.Errorf("winrm: soap post: %w", err)
	}
	defer func() { _ = soapResp.Body.Close() }()

	respBody, err := io.ReadAll(soapResp.Body)
	if err != nil {
		return "", fmt.Errorf("winrm: read soap response: %w", err)
	}

	result, err := t.processSoapResponse(soapResp.StatusCode, soapResp.Header.Get("Content-Type"), respBody, session)
	if err != nil {
		return "", fmt.Errorf("winrm: processSoapResponse: %w", err)
	}
	return result, nil
}

// processSoapResponse interprets a WinRM HTTP response. Encrypted responses are
// decrypted; unencrypted 200 soap+xml responses are passed through; anything
// else is an error that will trigger session invalidation and retry.
func (t *ntlmEncryptionTransporter) processSoapResponse(statusCode int, contentType string, body []byte, session *ntlmssp.SecuritySession) (string, error) {
	if strings.Contains(contentType, "multipart/encrypted") {
		plaintext, err := decryptWinRMEncryptedResponse(body, session)
		if err != nil {
			return "", fmt.Errorf("winrm: decrypt response: %w", err)
		}
		return string(plaintext), nil
	}
	if statusCode == http.StatusUnauthorized {
		return "", fmt.Errorf("winrm: http 401 unauthorized (session expired)")
	}
	if statusCode != http.StatusOK {
		snippet := body
		if len(snippet) > 512 {
			snippet = snippet[:512]
		}
		return "", fmt.Errorf("winrm: http %d: %q", statusCode, snippet)
	}
	if !strings.Contains(contentType, "application/soap+xml") {
		return "", fmt.Errorf("winrm: unexpected content type: %s", contentType)
	}
	return string(body), nil
}

// extractNTLMChallenge extracts the base64-decoded Type2 (Challenge) message
// from a 401 response's WWW-Authenticate: Negotiate <token> header.
func extractNTLMChallenge(resp *http.Response) ([]byte, error) {
	for _, v := range resp.Header["Www-Authenticate"] {
		if strings.HasPrefix(v, "Negotiate ") {
			b, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(v, "Negotiate "))
			if err != nil {
				return nil, fmt.Errorf("winrm: decode NTLM challenge: %w", err)
			}
			return b, nil
		}
	}
	return nil, fmt.Errorf("winrm: no NTLM challenge in 401 response (WWW-Authenticate: %v)", resp.Header["Www-Authenticate"])
}

const winrmMIMEBoundary = "Encrypted Boundary"
const winrmProtocolString = "application/HTTP-SPNEGO-session-encrypted"

// buildWinRMEncryptedBody constructs the WS-Management multipart/encrypted
// request body matching masterzen/winrm's encryptMessage format. The
// OriginalContent.Length field carries the plaintext SOAP length so the
// Windows WinRM service can validate the decrypted payload size.
func buildWinRMEncryptedBody(plaintext, sealed, signature []byte) []byte {
	dashBoundary := "--" + winrmMIMEBoundary
	blob := binary.LittleEndian.AppendUint32(nil, uint32(len(signature))) // #nosec G115 -- NTLM signatures are always 16 bytes
	blob = append(blob, signature...)
	blob = append(blob, sealed...)

	buf := &bytes.Buffer{}
	fmt.Fprintf(buf, "%s\r\n\tContent-Type: %s\r\n\tOriginalContent: type=application/soap+xml;charset=UTF-8;Length=%d\r\n%s\r\n\tContent-Type: application/octet-stream\r\n",
		dashBoundary, winrmProtocolString, len(plaintext), dashBoundary)
	buf.Write(blob)
	fmt.Fprintf(buf, "%s--\r\n", dashBoundary)
	return buf.Bytes()
}

// decryptWinRMEncryptedResponse parses the WS-Management
// multipart/encrypted response body and decrypts each MIME part using
// the NTLM session. The format uses tab-indented pseudo-headers with no
// blank line before the binary body, so it isn't standard MIME and
// mime/multipart can't parse it; instead this locates the octet-stream
// anchor and extracts exactly 4 + sigLen + originalContentLen bytes
// using the preceding OriginalContent: Length value, avoiding a
// boundary search that bytes.Split would misparse if ciphertext
// happened to contain the boundary bytes.
func decryptWinRMEncryptedResponse(respBody []byte, session interface {
	Unwrap([]byte, []byte) ([]byte, error)
}) ([]byte, error) {
	// anchor precedes the binary payload; long enough that a ciphertext collision is negligible.
	anchor := []byte("--" + winrmMIMEBoundary + "\r\n\tContent-Type: application/octet-stream\r\n")
	const origPrefix = "\tOriginalContent: "

	var message []byte
	rest := respBody

	for {
		idx := bytes.Index(rest, anchor)
		if idx < 0 {
			break
		}

		// LastIndex so an earlier part's header in a multi-part response doesn't shadow this one.
		meta := rest[:idx]
		origIdx := bytes.LastIndex(meta, []byte(origPrefix))
		if origIdx < 0 {
			return nil, fmt.Errorf("winrm: encrypted response missing OriginalContent header")
		}
		origLine := meta[origIdx+len(origPrefix):]
		if end := bytes.IndexByte(origLine, '\r'); end >= 0 {
			origLine = origLine[:end]
		}
		originalLen, err := parseWinRMOriginalContentLength(string(origLine))
		if err != nil {
			return nil, fmt.Errorf("winrm: %w", err)
		}

		payload := rest[idx+len(anchor):]
		if len(payload) < 4 {
			return nil, fmt.Errorf("winrm: encrypted payload too short (%d bytes)", len(payload))
		}
		sigLen := int(binary.LittleEndian.Uint32(payload[:4]))
		need := 4 + sigLen + originalLen
		if len(payload) < need {
			return nil, fmt.Errorf("winrm: encrypted payload truncated (need %d, have %d)", need, len(payload))
		}
		sig := payload[4 : 4+sigLen]
		sealed := payload[4+sigLen : need]

		decrypted, err := session.Unwrap(sealed, sig)
		if err != nil {
			return nil, err
		}
		message = append(message, decrypted...)
		rest = rest[idx+len(anchor)+need:]
	}

	if len(message) == 0 {
		return nil, fmt.Errorf("winrm: no encrypted parts found in response")
	}
	return message, nil
}

// parseWinRMOriginalContentLength extracts the Length field from a
// WS-Management OriginalContent header value such as
// "type=application/soap+xml;charset=UTF-8;Length=1234".
func parseWinRMOriginalContentLength(line string) (int, error) {
	for _, field := range strings.Split(line, ";") {
		if strings.HasPrefix(field, "Length=") {
			n, err := strconv.Atoi(strings.TrimPrefix(field, "Length="))
			if err != nil {
				return 0, fmt.Errorf("invalid OriginalContent Length %q: %w", field, err)
			}
			if n < 0 {
				return 0, fmt.Errorf("invalid OriginalContent Length %q: must be non-negative", field)
			}
			return n, nil
		}
	}
	return 0, fmt.Errorf("OriginalContent header missing Length field in %q", line)
}

// buildWinRMParams constructs per-backend WSMan parameters from the
// resolved options, copying winrm.DefaultParameters by value rather
// than aliasing the package-level *Parameters pointer: a naive
// `params := winrm.DefaultParameters` would mutate the shared global
// across concurrent Opens, letting a Basic-auth Open's cleared
// TransportDecorator silently affect a later NTLM Open. A nil
// TransportDecorator (the default) falls back to masterzen's plain
// Basic-auth transport; NTLM sets ntlmEncryptionTransporter and
// kerberos swaps in masterzen's own ClientKerberos.
func buildWinRMParams(opts WinRMOptions) *winrm.Parameters {
	pCopy := *winrm.DefaultParameters
	params := &pCopy
	params.Timeout = formatXSDDuration(opts.CommandTimeout)
	if opts.Auth == "ntlm" {
		username, password := opts.Username, string(opts.Password)
		params.TransportDecorator = func() winrm.Transporter {
			return &ntlmEncryptionTransporter{username: username, password: password}
		}
	}
	if opts.Auth == "kerberos" {
		// masterzen's Kerberos transport uses jcmturner/gokrb5 for a TGT (password or ccache mode) and sets SPNEGO per request.
		proto := "http"
		if opts.UseHTTPS {
			proto = "https"
		}
		settings := &winrm.Settings{
			WinRMUsername: opts.Username,
			WinRMPassword: string(opts.Password),
			WinRMHost:     opts.Host,
			WinRMPort:     opts.Port,
			WinRMProto:    proto,
			WinRMInsecure: opts.Insecure,
			KrbRealm:      opts.KrbRealm,
			KrbConfig:     opts.KrbConfigPath,
			KrbSpn:        opts.KrbSpn,
			KrbCCache:     opts.KrbCCachePath,
		}
		params.TransportDecorator = func() winrm.Transporter {
			return winrm.NewClientKerberos(settings)
		}
	}
	return params
}

// Healthcheck runs a trivial PowerShell round-trip to confirm the WSMan
// endpoint accepts our credentials and pwsh launches on the remote.
func (b *winrmBackend) Healthcheck(ctx context.Context) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.healthcheckLocked(ctx)
}

// healthcheckLocked is the unsynchronized core. Caller holds b.mu. Open
// reuses this directly during construction; the public Healthcheck takes
// the mutex first.
func (b *winrmBackend) healthcheckLocked(ctx context.Context) error {
	if b.client == nil {
		return errors.New("winrm: backend not open")
	}
	res, err := b.runScriptOnClient(ctx, b.client, `'pong' | ConvertTo-Json -Compress`, nil)
	if err != nil {
		return err
	}
	if res.ExitCode != 0 {
		return fmt.Errorf("winrm healthcheck non-zero exit %d: %s", res.ExitCode, string(res.Stderr))
	}
	if !bytes.Contains(res.Stdout, []byte(`"pong"`)) {
		return fmt.Errorf("winrm healthcheck unexpected stdout: %q", string(res.Stdout))
	}
	return nil
}

// RunScript executes a PowerShell script on the remote host. The body is
// shipped as `-EncodedCommand` (UTF-16LE base64) so multi-line scripts and
// quoting-sensitive content arrive intact -- matches the runner.go contract
// the SSH and local backends honor too.
func (b *winrmBackend) RunScript(ctx context.Context, script string, stdinJSON []byte) (Result, error) {
	b.mu.Lock()
	client := b.client
	opened := b.opened
	b.mu.Unlock()
	if !opened || client == nil {
		return Result{}, errors.New("winrm: backend not open -- call Open first")
	}
	return b.runScriptOnClient(ctx, client, script, stdinJSON)
}

// runScriptOnClient is the shared body of RunScript and Healthcheck;
// Open can call this with its still-being-constructed client without
// flipping the opened flag. It opens a single shell and runs three
// commands on it: stage the script body to a remote temp file via
// stdin (WSMan's 8192-char MaxCommandLine rejects a base64'd script
// via -EncodedCommand directly), execute the staged script, then clean
// up the temp file on a fresh background context so a canceled apply
// still runs the delete. Sharing one shell across all three keeps the
// peak open-shell count at 1 per call regardless of parallelism;
// shellSem caps total concurrent shells backend-wide.
func (b *winrmBackend) runScriptOnClient(ctx context.Context, client *winrm.Client, script string, stdinJSON []byte) (Result, error) {
	if b.opts.CommandTimeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, b.opts.CommandTimeout)
		defer cancel()
	}

	var suffix [8]byte
	if _, err := rand.Read(suffix[:]); err != nil {
		return Result{}, fmt.Errorf("winrm: generate temp filename: %w", err)
	}
	name := "hyperv-" + hex.EncodeToString(suffix[:]) + ".ps1"
	remotePath := `C:/Windows/Temp/` + name

	// UTF-8 in and out, overriding the system codepage; one semicolon-joined expression to fit MaxCommandLine.
	stagingScript := `[Console]::InputEncoding = [Text.UTF8Encoding]::new($false); ` +
		`[IO.File]::WriteAllText('` + remotePath + `', [Console]::In.ReadToEnd(), ` +
		`[Text.UTF8Encoding]::new($true))`
	stageCmd := fmt.Sprintf("%s -NoProfile -NonInteractive -EncodedCommand %s",
		b.opts.PwshPath, encodePSScript(stagingScript))
	execCmd := fmt.Sprintf("%s -NoProfile -NonInteractive -ExecutionPolicy Bypass -File %s",
		b.opts.PwshPath, remotePath)
	delScript := `Remove-Item -LiteralPath '` + remotePath + `' -Force -ErrorAction SilentlyContinue`
	cleanupCmd := fmt.Sprintf("%s -NoProfile -NonInteractive -EncodedCommand %s",
		b.opts.PwshPath, encodePSScript(delScript))

	if err := b.acquireShell(ctx); err != nil {
		return Result{}, err
	}
	defer b.releaseShell()

	shell, err := client.CreateShell()
	if err != nil {
		return Result{}, fmt.Errorf("winrm: create shell: %w", err)
	}
	defer func() { _ = shell.Close() }()

	// Step 1: stage script body to temp file via stdin.
	var stageStderr bytes.Buffer
	stageCode, stageErr := runShellCmd(ctx, shell, stageCmd, strings.NewReader(script), io.Discard, &stageStderr)
	if stageErr != nil {
		if ctx.Err() != nil {
			return Result{}, fmt.Errorf("%w: %v", ErrTimeout, ctx.Err())
		}
		return Result{}, fmt.Errorf("winrm: stage script: %w", stageErr)
	}
	if stageCode != 0 {
		return Result{}, fmt.Errorf("winrm: stage script exit %d: %s", stageCode, stageStderr.String())
	}

	// Step 2: execute the staged script.
	var stdout, stderr bytes.Buffer
	var stdinReader io.Reader
	if len(stdinJSON) > 0 {
		stdinReader = bytes.NewReader(stdinJSON)
	}
	start := time.Now()
	exitCode, runErr := runShellCmd(ctx, shell, execCmd, stdinReader, &stdout, &stderr)
	duration := time.Since(start)

	// Best-effort on a fresh context; Windows auto-cleans %TEMP%, so a failure here isn't fatal.
	cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cleanupCancel()
	_, _ = runShellCmd(cleanupCtx, shell, cleanupCmd, nil, io.Discard, io.Discard)

	if runErr != nil {
		if ctx.Err() != nil {
			return Result{}, fmt.Errorf("%w: %v", ErrTimeout, ctx.Err())
		}
		return Result{}, fmt.Errorf("winrm: run script: %w", runErr)
	}

	return Result{
		Stdout:   stdout.Bytes(),
		Stderr:   stripCLIXML(stderr.Bytes()),
		ExitCode: exitCode,
		Duration: duration,
	}, nil
}

// encodePSScript produces the value for `powershell.exe -EncodedCommand`:
// UTF-16LE bytes of the script, base64-encoded. PowerShell's -EncodedCommand
// is the canonical way to pass multi-line / quote-sensitive bodies without
// shell-escape hazards. binary.LittleEndian.PutUint16 is stdlib's
// named helper for the 2-byte little-endian split -- expresses intent
// more clearly than `byte(r) byte(r>>8)` and sidesteps gosec G115's
// generic narrowing-conversion warning on the manual form.
func encodePSScript(s string) string {
	utf16Runes := utf16.Encode([]rune(s))
	buf := make([]byte, len(utf16Runes)*2)
	for i, r := range utf16Runes {
		binary.LittleEndian.PutUint16(buf[i*2:], r)
	}
	return base64.StdEncoding.EncodeToString(buf)
}

// formatXSDDuration converts a Go duration to the ISO 8601 / xs:duration
// form WSMan expects on the OperationTimeout SOAP header (e.g., "PT5M30S").
// Falls back to PT0S if d <= 0 (matching DefaultParameters.Timeout when
// the operator opts out).
func maxDuration(a, b time.Duration) time.Duration {
	if a > b {
		return a
	}
	return b
}

func formatXSDDuration(d time.Duration) string {
	if d <= 0 {
		return "PT0S"
	}
	totalSeconds := int64(d.Seconds())
	hours := totalSeconds / 3600
	minutes := (totalSeconds % 3600) / 60
	seconds := totalSeconds % 60
	out := "PT"
	if hours > 0 {
		out += fmt.Sprintf("%dH", hours)
	}
	if minutes > 0 {
		out += fmt.Sprintf("%dM", minutes)
	}
	if seconds > 0 || (hours == 0 && minutes == 0) {
		out += fmt.Sprintf("%dS", seconds)
	}
	return out
}
