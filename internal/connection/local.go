package connection

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf16"
)

// LocalOptions configures the local backend. Empty values mean "discover
// from PATH": prefer pwsh (faster cold start), fall back to powershell.exe.
type LocalOptions struct {
	// PwshPath, if non-empty, is used as-is. Set via the provider's
	// `local.pwsh_path` attribute or the HYPERV_PWSH_PATH env var.
	PwshPath string
}

// localBackend is the in-process exec implementation of Connection. Used
// when the provider runs on the same machine as the Hyper-V host.
type localBackend struct {
	pwshPath string
}

// NewLocal returns a Connection backed by a local pwsh / powershell.exe
// process per call. Opens nothing — the local backend is stateless.
func NewLocal(opts LocalOptions) (Connection, error) {
	pwshPath, err := discoverPwsh(opts.PwshPath)
	if err != nil {
		return nil, err
	}
	return &localBackend{pwshPath: pwshPath}, nil
}

// Compile-time assertion.
var _ Connection = (*localBackend)(nil)

func (b *localBackend) Backend() string              { return "local" }
func (b *localBackend) Open(_ context.Context) error { return nil }
func (b *localBackend) Close() error                 { return nil }
func (b *localBackend) Healthcheck(ctx context.Context) error {
	// A trivial round-trip — confirms PS launches, encoding works, and
	// the binary on PATH is the version we expect.
	res, err := b.RunScript(ctx, `'pong' | ConvertTo-Json -Compress`, nil)
	if err != nil {
		return err
	}
	if res.ExitCode != 0 {
		return fmt.Errorf("local healthcheck non-zero exit %d: %s", res.ExitCode, string(res.Stderr))
	}
	if !strings.Contains(string(res.Stdout), `"pong"`) {
		return fmt.Errorf("local healthcheck unexpected stdout: %q", string(res.Stdout))
	}
	return nil
}

func (b *localBackend) RunScript(ctx context.Context, script string, stdinJSON []byte) (Result, error) {
	cmd := b.buildCmd(ctx, script, stdinJSON)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	start := time.Now()
	runErr := cmd.Run()
	duration := time.Since(start)

	// Distinguish ctx-driven termination from app-level exit. The original
	// code did `if ctx.Err() != nil → ErrTimeout` unconditionally, which
	// could mask a clean exit. The fix: only treat as ErrTimeout when
	// `cmd.Run` returned an error (process killed or couldn't start) AND
	// the ctx is in fact done. A clean exit produces runErr == nil, so a
	// coincidental late ctx-cancel doesn't suppress the Result.
	//
	// Cross-platform note: on Unix a signal-kill produces ExitCode == -1;
	// on Windows exec.CommandContext kills via TerminateProcess yielding
	// ExitCode == 1 (or whatever Go's KillProcess passes). Checking just
	// `runErr != nil && ctxDone` covers both — we don't need to inspect
	// the exit-code value, only that there was an error and ctx is done.
	ctxDone := ctx.Err() != nil
	exitCode := 0
	if runErr != nil {
		if ctxDone {
			return Result{}, fmt.Errorf("%w: %v", ErrTimeout, ctx.Err())
		}
		var ee *exec.ExitError
		if errors.As(runErr, &ee) {
			// Application-level non-zero exit. The typed client interprets
			// the stderr envelope.
			exitCode = ee.ExitCode()
		} else {
			// Process couldn't start, etc. — transport failure.
			return Result{}, fmt.Errorf("local backend exec: %w", runErr)
		}
	}

	return Result{
		Stdout:   stdout.Bytes(),
		Stderr:   stripCLIXML(stderr.Bytes()),
		ExitCode: exitCode,
		Duration: duration,
	}, nil
}

// StreamFile copies localPath to remotePath via os.Open + io.Copy. The
// "remote" host is the same machine in the local-backend case, so this is
// a plain file copy with the destination directory pre-created. Truncates
// remotePath if it already exists; preserves source mode bits is not a
// goal (Windows filesystem permissions don't carry the same semantics).
//
// ctx cancellation interrupts io.Copy via a small adapter — exec.Cmd-style
// pre-emption isn't available, but the loop checks ctx between buffered
// writes so a canceled apply unblocks within one chunk.
func (b *localBackend) StreamFile(ctx context.Context, localPath, remotePath string) error {
	src, err := os.Open(localPath) // #nosec G304 -- localPath is the operator's own file path from resource config
	if err != nil {
		return fmt.Errorf("local: open %s: %w", localPath, err)
	}
	defer func() { _ = src.Close() }()

	if err := os.MkdirAll(filepath.Dir(remotePath), 0o750); err != nil {
		return fmt.Errorf("local: mkdir %s: %w", filepath.Dir(remotePath), err)
	}
	dst, err := os.Create(remotePath) // #nosec G304 -- remotePath is the operator's destination from resource config
	if err != nil {
		return fmt.Errorf("local: create %s: %w", remotePath, err)
	}
	// dst is closed explicitly on both paths -- not via defer -- because
	// Windows refuses os.Remove against an open handle, and the cleanup
	// branch below relies on the partial file being unlinkable.
	if _, err := io.Copy(dst, &ctxReader{ctx: ctx, r: src}); err != nil {
		_ = dst.Close()
		// Best-effort cleanup of the partial destination so a re-apply
		// starts from a clean slate. Mirrors what the typed client does
		// for a `.part` staging file at the higher layer.
		_ = os.Remove(remotePath)
		if ctx.Err() != nil {
			return fmt.Errorf("%w: %v", ErrTimeout, ctx.Err())
		}
		return fmt.Errorf("local: copy %s to %s: %w", localPath, remotePath, err)
	}
	if err := dst.Close(); err != nil {
		return fmt.Errorf("local: close %s: %w", remotePath, err)
	}
	return nil
}

// ctxReader wraps an io.Reader and surfaces ctx cancellation as an error
// on the next Read. Lets io.Copy abort promptly on apply-time cancel
// without spawning a separate goroutine.
type ctxReader struct {
	ctx context.Context
	r   io.Reader
}

func (cr *ctxReader) Read(p []byte) (int, error) {
	if err := cr.ctx.Err(); err != nil {
		return 0, err
	}
	return cr.r.Read(p)
}

// buildCmd is split out for testability — unit tests assert on the resulting
// *exec.Cmd's Args and Path without invoking pwsh.
func (b *localBackend) buildCmd(ctx context.Context, script string, stdinJSON []byte) *exec.Cmd {
	encoded := base64.StdEncoding.EncodeToString(utf16leBytes(script))
	// #nosec G204 -- b.pwshPath is the operator-configured path to the PowerShell
	// binary they asked us to invoke (provider attribute local.pwsh_path or env
	// HYPERV_PWSH_PATH, otherwise discovered from PATH). exec.CommandContext does
	// not invoke a shell, so no metacharacter expansion is possible. All other
	// arguments are static literals; `encoded` is base64 (no shell-unsafe chars)
	// of UTF-16-encoded script content authored in this package. The tainted-input
	// finding describes a config knob, not an untrusted-data path.
	cmd := exec.CommandContext(ctx, b.pwshPath,
		"-NoProfile",
		"-NonInteractive",
		"-ExecutionPolicy", "Bypass",
		"-EncodedCommand", encoded,
	)
	if len(stdinJSON) > 0 {
		cmd.Stdin = bytes.NewReader(stdinJSON)
	}
	return cmd
}

// discoverPwsh resolves a usable PowerShell binary path. If `override` is
// set we trust it (caller bears the consequences); otherwise we prefer pwsh
// and fall back to powershell.exe / powershell.
func discoverPwsh(override string) (string, error) {
	if override != "" {
		return override, nil
	}
	for _, name := range []string{"pwsh", "powershell.exe", "powershell"} {
		if path, err := exec.LookPath(name); err == nil {
			return path, nil
		}
	}
	return "", errors.New("no PowerShell binary found on PATH; tried pwsh, powershell.exe, powershell. " +
		"Set HYPERV_PWSH_PATH or local.pwsh_path to point at a specific binary")
}

// utf16leBytes encodes s as little-endian UTF-16 with no BOM — the format
// powershell.exe -EncodedCommand expects. Each uint16 code unit is packed
// into two bytes via encoding/binary; the manual byte(r)/byte(r>>8) shorthand
// is equivalent but trips gosec G115's uint16->byte truncation check.
func utf16leBytes(s string) []byte {
	u16 := utf16.Encode([]rune(s))
	out := make([]byte, len(u16)*2)
	for i, r := range u16 {
		binary.LittleEndian.PutUint16(out[i*2:], r)
	}
	return out
}

// stripCLIXML drops PS 5.1 stderr progress noise: lines starting with
// `#< CLIXML` or `<Objs `.
func stripCLIXML(b []byte) []byte {
	if len(b) == 0 {
		return b
	}
	lines := bytes.Split(b, []byte("\n"))
	out := lines[:0]
	for _, line := range lines {
		trimmed := bytes.TrimSpace(line)
		if bytes.HasPrefix(trimmed, []byte("#< CLIXML")) {
			continue
		}
		if bytes.HasPrefix(trimmed, []byte("<Objs ")) {
			continue
		}
		out = append(out, line)
	}
	return bytes.TrimSpace(bytes.Join(out, []byte("\n")))
}
