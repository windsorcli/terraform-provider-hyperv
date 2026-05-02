package connection

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/x509"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"time"
	"unicode/utf16"

	"github.com/masterzen/winrm"
)

// WinRMOptions configures the WinRM backend. The provider's Configure pass
// resolves env vars + provider attributes into this struct (see
// internal/provider/backend_select.go).
type WinRMOptions struct {
	Host     string // required
	Port     int    // default 5986 (HTTPS) or 5985 (HTTP)
	Username string // required
	Password string // required for ntlm/basic

	UseHTTPS bool   // default true; flip to false only for diagnosing TLS-only failures
	Insecure bool   // skip TLS certificate verification (default false)
	Auth     string // "ntlm" | "basic" | "kerberos"; default "ntlm"
	CACert   string // path to a CA bundle PEM; empty = system roots

	// Timeout bounds an individual WinRM operation (dial, auth, request).
	// Default 30s. Distinct from CommandTimeout, which bounds the remote
	// PowerShell call.
	Timeout time.Duration

	// CommandTimeout bounds a single RunScript call. Default 5m. A wedged
	// remote cmdlet surfaces as ErrTimeout instead of blocking the whole
	// apply. Set to 0 to disable.
	CommandTimeout time.Duration

	// PwshPath is the binary the remote shell invokes per call. Default:
	// "powershell.exe" -- universally available on Windows. Set to "pwsh"
	// or "pwsh.exe" to prefer PS 7+ if installed.
	PwshPath string
}

const (
	defaultWinRMPortHTTPS      = 5986
	defaultWinRMPortHTTP       = 5985
	defaultWinRMTimeout        = 30 * time.Second
	defaultWinRMCommandTimeout = 5 * time.Minute
	defaultWinRMPwshPath       = "powershell.exe"
	defaultWinRMAuth           = "ntlm"
)

// winrmBackend implements Connection over the masterzen/winrm client. WinRM
// is HTTP-based with no persistent socket the way SSH has -- the upstream
// library opens a fresh request per command -- so Open's job is mostly
// validating that auth and TLS work end-to-end, and Close is a no-op.
type winrmBackend struct {
	opts   WinRMOptions
	client *winrm.Client

	mu     sync.Mutex
	opened bool
}

// Compile-time assertion.
var _ Connection = (*winrmBackend)(nil)

// NewWinRM builds a Connection backed by masterzen/winrm. Validates
// required fields and applies defaults so callers get a fully-resolved
// backend; the actual HTTP client is constructed lazily by Open so a unit
// test that only exercises NewWinRM doesn't pay the construction cost.
//
// Auth methods supported: ntlm (default), basic. Kerberos is rejected here
// with a clear message until SPN rendering and krb5 config are wired
// through in a follow-up.
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
		if opts.Password == "" {
			return nil, fmt.Errorf("winrm: %s auth requires a password", auth)
		}
	case "kerberos":
		return nil, errors.New("winrm: kerberos auth is not currently implemented; " +
			"use ntlm or basic, or wait for the kerberos follow-up")
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

	timeout := opts.Timeout
	if timeout == 0 {
		timeout = defaultWinRMTimeout
	}
	commandTimeout := opts.CommandTimeout
	if commandTimeout == 0 {
		commandTimeout = defaultWinRMCommandTimeout
	}

	pwshPath := opts.PwshPath
	if pwshPath == "" {
		pwshPath = defaultWinRMPwshPath
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
			Timeout:        timeout,
			CommandTimeout: commandTimeout,
			PwshPath:       pwshPath,
		},
	}, nil
}

// Backend returns the lowercase identifier used for tflog field decoration.
func (b *winrmBackend) Backend() string { return "winrm" }

// Open constructs the masterzen/winrm Client and runs a Healthcheck round-
// trip so misconfiguration (auth failure, wrong port, untrusted cert)
// surfaces at provider-Configure time rather than mid-plan during a Read.
//
// Idempotent -- subsequent calls return nil if the client is already up.
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
		// #nosec G304 -- the path is operator-supplied (provider attribute
		// winrm.cacert or env HYPERV_WINRM_CACERT), not derived from
		// untrusted input. The user explicitly told us to use this CA
		// bundle for cert verification.
		caBytes, err := os.ReadFile(b.opts.CACert)
		if err != nil {
			return fmt.Errorf("winrm: read cacert %s: %w", b.opts.CACert, err)
		}
		// Sanity-check that the file actually parses as a PEM bundle so a
		// typoed path or truncated copy surfaces here instead of as an
		// opaque TLS handshake error later.
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(caBytes) {
			return fmt.Errorf("winrm: cacert %s contains no PEM certificates", b.opts.CACert)
		}
		endpoint.CACert = caBytes
	}

	params := buildWinRMParams(b.opts)

	client, err := winrm.NewClientWithParameters(endpoint, b.opts.Username, b.opts.Password, params)
	if err != nil {
		return fmt.Errorf("winrm: build client: %w", err)
	}
	b.client = client

	if err := b.healthcheckLocked(ctx); err != nil {
		// Don't mark the backend opened on a failed healthcheck so a
		// subsequent Open reattempts cleanly.
		b.client = nil
		return err
	}
	b.opened = true
	return nil
}

// Close releases the backend's persistent state. WinRM has none beyond the
// cached client struct, so this is essentially a flag flip. Idempotent.
func (b *winrmBackend) Close() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.client = nil
	b.opened = false
	return nil
}

// buildWinRMParams constructs the per-backend WSMan parameters from the
// resolved options. Critically, it copies winrm.DefaultParameters by value
// rather than aliasing the package-level pointer -- the upstream library
// declares DefaultParameters as a *Parameters, so naive `params := winrm.
// DefaultParameters` followed by `params.Timeout = ...` would mutate the
// shared global, racing across concurrent Open calls and persisting
// across them (a Basic-auth Open clearing TransportDecorator would
// silently affect later NTLM Opens). The value-copy isolates each
// backend's params.
//
// masterzen/winrm uses Negotiate (NTLM) by default. Switching to a
// Basic-only client is a matter of clearing the transport decorator;
// the library then sends the Authorization: Basic header itself.
func buildWinRMParams(opts WinRMOptions) *winrm.Parameters {
	pCopy := *winrm.DefaultParameters
	params := &pCopy
	params.Timeout = formatXSDDuration(opts.CommandTimeout)
	if opts.Auth == "basic" {
		params.TransportDecorator = nil
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

// runScriptOnClient is the shared body of RunScript and Healthcheck. Open
// can call this with its still-being-constructed client without flipping
// the opened flag.
//
// Stages the script body as a remote temp file before invoking it with
// `powershell -File <path>`. The staging step exists because WSMan's
// default MaxCommandLine is 8192 chars -- a preamble + verb script
// base64-encodes to ~5-9KB and gets rejected with "command line too
// long" when shipped via -EncodedCommand. Same architectural fix SSH
// uses (see ssh.go's stageScript). Adds one extra WSMan round-trip per
// call (~50-100ms) -- negligible vs PowerShell startup cost.
func (b *winrmBackend) runScriptOnClient(ctx context.Context, client *winrm.Client, script string, stdinJSON []byte) (Result, error) {
	if b.opts.CommandTimeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, b.opts.CommandTimeout)
		defer cancel()
	}

	remotePath, cleanup, err := b.stageWinRMScript(ctx, client, script)
	if err != nil {
		if ctx.Err() != nil {
			return Result{}, fmt.Errorf("%w: %v", ErrTimeout, ctx.Err())
		}
		return Result{}, err
	}
	defer cleanup()

	cmd := fmt.Sprintf("%s -NoProfile -NonInteractive -ExecutionPolicy Bypass -File %s",
		b.opts.PwshPath, remotePath)

	var stdout, stderr bytes.Buffer
	var stdin *bytes.Reader
	if len(stdinJSON) > 0 {
		stdin = bytes.NewReader(stdinJSON)
	}

	start := time.Now()
	exitCode, runErr := runWithOptionalStdin(ctx, client, cmd, stdin, &stdout, &stderr)
	duration := time.Since(start)

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

// stageWinRMScript writes the script body to a remote temp file via a
// small staging WinRM call that reads stdin and writes the bytes to disk.
// Returns the remote path plus a cleanup func that deletes the file via a
// fresh background-context call so a canceled apply still cleans up.
//
// Why staging exists: WSMan's MaxCommandLine setting (default 8192 chars)
// caps the -EncodedCommand argument. Our preamble + verb scripts run
// 5-9KB once base64-encoded as UTF-16LE. The staging command is ~150
// chars of source, well under the limit; the actual script body rides
// as stdin data, which has no length limit at the protocol layer.
//
// The staging script writes the file with a UTF-8 BOM. PowerShell 5.1's
// `-File` reader uses the BOM to determine encoding; without it, 5.1
// defaults to the system codepage (Windows-1252 on en-US) and would
// corrupt any non-ASCII content. All current scripts are pure ASCII,
// but the BOM future-proofs the contract -- matches the SSH backend's
// staging behavior at ssh.go's stageScript.
func (b *winrmBackend) stageWinRMScript(ctx context.Context, client *winrm.Client, script string) (string, func(), error) {
	var suffix [8]byte
	if _, err := rand.Read(suffix[:]); err != nil {
		return "", nil, fmt.Errorf("winrm: generate temp filename: %w", err)
	}
	name := "hyperv-" + hex.EncodeToString(suffix[:]) + ".ps1"
	remotePath := `C:/Windows/Temp/` + name

	// Read stdin as UTF-8 (overrides the system codepage default), write
	// the bytes to the file with a UTF-8 BOM. Single semicolon-joined
	// expression so it stays a one-liner that fits under any MaxCommandLine.
	stagingScript := `[Console]::InputEncoding = [Text.UTF8Encoding]::new($false); ` +
		`[IO.File]::WriteAllText('` + remotePath + `', [Console]::In.ReadToEnd(), ` +
		`[Text.UTF8Encoding]::new($true))`

	cmd := fmt.Sprintf("%s -NoProfile -NonInteractive -EncodedCommand %s",
		b.opts.PwshPath, encodePSScript(stagingScript))

	var stdout, stderr bytes.Buffer
	code, err := client.RunWithContextWithInput(ctx, cmd, &stdout, &stderr, strings.NewReader(script))
	if err != nil {
		return "", nil, fmt.Errorf("winrm: stage script: %w", err)
	}
	if code != 0 {
		return "", nil, fmt.Errorf("winrm: stage script exit %d: %s", code, stderr.String())
	}

	cleanup := func() {
		// Run the deletion attempt in a goroutine so a wedged remote
		// doesn't block runScriptOnClient's return -- particularly
		// important on the timeout path, where the apply ctx is already
		// canceled and the operator is waiting for the error to surface.
		// Fresh background context (the apply ctx may already be done)
		// with a 10s ceiling: temp-file deletion is best-effort; if the
		// remote can't drain the call in 10s, leak the file -- Windows
		// auto-cleans %TEMP% periodically. Parallel applies with many
		// concurrent timeouts therefore can't pile up cleanup goroutines
		// for more than 10 seconds each.
		go func() {
			cleanupCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			delScript := `Remove-Item -LiteralPath '` + remotePath + `' -Force -ErrorAction SilentlyContinue`
			delCmd := fmt.Sprintf("%s -NoProfile -NonInteractive -EncodedCommand %s",
				b.opts.PwshPath, encodePSScript(delScript))
			_, _ = client.RunWithContext(cleanupCtx, delCmd, io.Discard, io.Discard)
		}()
	}
	return remotePath, cleanup, nil
}

// runWithOptionalStdin dispatches to the right masterzen/winrm entry point.
// RunWithContext takes no stdin; RunWithContextWithInput pipes a reader.
// Choosing per-call lets the no-stdin path stay simple and matches what the
// other backends do for scripts that don't need input JSON.
func runWithOptionalStdin(ctx context.Context, client *winrm.Client, cmd string, stdin *bytes.Reader, stdout, stderr *bytes.Buffer) (int, error) {
	if stdin == nil {
		return client.RunWithContext(ctx, cmd, stdout, stderr)
	}
	return client.RunWithContextWithInput(ctx, cmd, stdout, stderr, stdin)
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
