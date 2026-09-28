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
	// Trivial round-trip: confirms PS launches and encoding works.
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

// RunScript treats a ctx-driven kill as ErrTimeout only when cmd.Run
// itself errored and ctx is done, so a clean exit racing a late
// ctx-cancel doesn't get mistaken for a timeout. It checks only that
// runErr is non-nil and ctx is done, not the platform-specific
// exit-code value a signal-kill produces on Unix versus Windows.
func (b *localBackend) RunScript(ctx context.Context, script string, stdinJSON []byte) (Result, error) {
	cmd := b.buildCmd(ctx, script, stdinJSON)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	start := time.Now()
	runErr := cmd.Run()
	duration := time.Since(start)

	ctxDone := ctx.Err() != nil
	exitCode := 0
	if runErr != nil {
		if ctxDone {
			return Result{}, fmt.Errorf("%w: %v", ErrTimeout, ctx.Err())
		}
		var ee *exec.ExitError
		if errors.As(runErr, &ee) {
			// Non-zero app exit; the typed client reads the stderr envelope.
			exitCode = ee.ExitCode()
		} else {
			// Process couldn't start: a transport failure.
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

// StreamFile copies localPath to remotePath via os.Open + io.Copy,
// since the "remote" host is the same machine for this backend.
// Truncates an existing remotePath; ctx cancellation interrupts the
// copy via a small Reader adapter, checked between buffered writes.
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
	// Closed explicitly, not deferred: Windows can't os.Remove an open handle.
	if _, err := io.Copy(dst, &ctxReader{ctx: ctx, r: src}); err != nil {
		_ = dst.Close()
		// Best-effort cleanup so a re-apply starts clean.
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
	// #nosec G204 -- b.pwshPath is operator-configured, not shell-invoked; encoded is base64 authored in this package.
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
