package connection

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"os/exec"
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
	// would mask a real exit code on an unrelated race (process exits
	// non-zero AND parent ctx happens to cancel around the same moment).
	// Now: only treat as ErrTimeout when the process didn't exit cleanly
	// (signal-killed, exit code -1) AND the ctx is in fact done.
	ctxDone := ctx.Err() != nil
	exitCode := 0
	if runErr != nil {
		var ee *exec.ExitError
		if errors.As(runErr, &ee) {
			exitCode = ee.ExitCode()
			// Signal-killed (typically SIGKILL from exec.CommandContext's
			// Cancel func) shows up as ExitCode == -1. Combined with a
			// done ctx, that's our timeout signal.
			if ctxDone && exitCode == -1 {
				return Result{}, fmt.Errorf("%w: %v", ErrTimeout, ctx.Err())
			}
			// Otherwise: real application-level exit (any code, including
			// ones that happen to coincide with a ctx cancel). The typed
			// client interprets the stderr envelope.
		} else {
			// Process couldn't start, etc. — transport failure. If ctx is
			// also done, prefer ErrTimeout for a clearer caller signal.
			if ctxDone {
				return Result{}, fmt.Errorf("%w: %v", ErrTimeout, ctx.Err())
			}
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

// buildCmd is split out for testability — unit tests assert on the resulting
// *exec.Cmd's Args and Path without invoking pwsh.
func (b *localBackend) buildCmd(ctx context.Context, script string, stdinJSON []byte) *exec.Cmd {
	encoded := base64.StdEncoding.EncodeToString(utf16leBytes(script))
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
// powershell.exe -EncodedCommand expects.
func utf16leBytes(s string) []byte {
	u16 := utf16.Encode([]rune(s))
	out := make([]byte, len(u16)*2)
	for i, r := range u16 {
		out[i*2] = byte(r)
		out[i*2+1] = byte(r >> 8)
	}
	return out
}

// stripCLIXML removes the `#< CLIXML` progress envelopes that PS 5.1 emits
// on stderr regardless of $ProgressPreference (some cmdlets bypass the
// preference). Real PS errors don't use the CLIXML prefix, so dropping
// these lines defensively is safe.
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
		// CLIXML envelope continuation lines start with the XML tag itself.
		if bytes.HasPrefix(trimmed, []byte("<Objs ")) || bytes.HasPrefix(trimmed, []byte("_x")) {
			continue
		}
		out = append(out, line)
	}
	return bytes.TrimSpace(bytes.Join(out, []byte("\n")))
}
