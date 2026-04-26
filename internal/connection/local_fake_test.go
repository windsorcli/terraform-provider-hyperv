//go:build !windows

package connection

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// writeFakePwsh drops a tiny shell script in a fresh temp dir and returns
// its path. The script branches on $FAKE_PWSH_BEHAVIOR so a single binary
// can stand in for several pwsh failure modes without per-test scripts.
//
// This lets us exercise localBackend.RunScript end-to-end on Linux CI
// (where no real PowerShell is installed) — covering the non-zero exit,
// CLIXML stripping, transport failure, and ctx cancellation paths that
// the skip-if-no-pwsh integration tests can't reach there.
//
// Build-gated to !windows because Windows can't execute /bin/sh shebangs.
// On Windows, the existing skipIfNoPwsh-gated integration tests cover the
// equivalent behavior against a real powershell.exe.
func writeFakePwsh(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "fake-pwsh")

	const script = `#!/bin/sh
# Fake pwsh: $FAKE_PWSH_BEHAVIOR selects which failure mode to emit.
case "${FAKE_PWSH_BEHAVIOR:-echo_ok}" in
  echo_ok)
    printf '"hello"\n'
    exit 0
    ;;
  echo_pong)
    printf '"pong"\n'
    exit 0
    ;;
  echo_stdin)
    # Echo whatever stdin was piped in. Lets us verify the local backend
    # actually pipes stdinJSON through.
    cat
    exit 0
    ;;
  exit_nonzero)
    printf 'oops\n' >&2
    exit 7
    ;;
  sleep_long)
    sleep 30
    exit 0
    ;;
  clixml_stderr)
    # Mimics PS 5.1's progress-stream noise plus a real error envelope.
    printf '#< CLIXML\n' >&2
    printf '<Objs Version="1.1.0.1"></Objs>\n' >&2
    printf '{"category":"PermissionDenied","message":"denied"}\n' >&2
    exit 1
    ;;
  *)
    printf 'unknown FAKE_PWSH_BEHAVIOR=%s\n' "$FAKE_PWSH_BEHAVIOR" >&2
    exit 99
    ;;
esac
`
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake pwsh: %v", err)
	}
	return path
}

func TestLocalBackend_RunScript_Success(t *testing.T) {
	t.Setenv("FAKE_PWSH_BEHAVIOR", "echo_ok")
	b := &localBackend{pwshPath: writeFakePwsh(t)}

	res, err := b.RunScript(t.Context(), "ignored-by-fake", nil)
	if err != nil {
		t.Fatalf("unexpected transport error: %v", err)
	}
	if res.ExitCode != 0 {
		t.Errorf("ExitCode = %d, want 0", res.ExitCode)
	}
	if !strings.Contains(string(res.Stdout), `"hello"`) {
		t.Errorf("Stdout = %q, want substring %q", string(res.Stdout), `"hello"`)
	}
	if res.Duration <= 0 {
		t.Errorf("Duration = %v, want positive", res.Duration)
	}
}

func TestLocalBackend_RunScript_NonZeroExitIsAppLevelNotTransport(t *testing.T) {
	t.Setenv("FAKE_PWSH_BEHAVIOR", "exit_nonzero")
	b := &localBackend{pwshPath: writeFakePwsh(t)}

	res, err := b.RunScript(t.Context(), "ignored", nil)
	if err != nil {
		t.Fatalf("non-zero exit must be application-level (err == nil), got %v", err)
	}
	if res.ExitCode != 7 {
		t.Errorf("ExitCode = %d, want 7", res.ExitCode)
	}
	if !strings.Contains(string(res.Stderr), "oops") {
		t.Errorf("Stderr = %q, want substring 'oops'", string(res.Stderr))
	}
}

func TestLocalBackend_RunScript_StdinIsPiped(t *testing.T) {
	t.Setenv("FAKE_PWSH_BEHAVIOR", "echo_stdin")
	b := &localBackend{pwshPath: writeFakePwsh(t)}

	in := []byte(`{"k":"v"}`)
	res, err := b.RunScript(t.Context(), "ignored", in)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if string(res.Stdout) != string(in) {
		t.Errorf("Stdout = %q, want %q (the piped stdin)", string(res.Stdout), string(in))
	}
}

func TestLocalBackend_RunScript_StripsCLIXMLFromStderr(t *testing.T) {
	t.Setenv("FAKE_PWSH_BEHAVIOR", "clixml_stderr")
	b := &localBackend{pwshPath: writeFakePwsh(t)}

	res, err := b.RunScript(t.Context(), "ignored", nil)
	if err != nil {
		t.Fatalf("non-zero exit must be application-level (err == nil), got %v", err)
	}
	if res.ExitCode != 1 {
		t.Errorf("ExitCode = %d, want 1", res.ExitCode)
	}
	stderr := string(res.Stderr)
	if strings.Contains(stderr, "#< CLIXML") {
		t.Errorf("Stderr should have CLIXML stripped; got %q", stderr)
	}
	if strings.Contains(stderr, "<Objs ") {
		t.Errorf("Stderr should have CLIXML envelope continuation stripped; got %q", stderr)
	}
	if !strings.Contains(stderr, "PermissionDenied") {
		t.Errorf("Stderr should retain the real error envelope; got %q", stderr)
	}
}

func TestLocalBackend_RunScript_ContextCanceledMapsToErrTimeout(t *testing.T) {
	t.Setenv("FAKE_PWSH_BEHAVIOR", "sleep_long")
	b := &localBackend{pwshPath: writeFakePwsh(t)}

	ctx, cancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
	defer cancel()

	_, err := b.RunScript(ctx, "ignored", nil)
	if !errors.Is(err, ErrTimeout) {
		t.Errorf("err = %v, want ErrTimeout", err)
	}
}

func TestLocalBackend_RunScript_TransportFailureWhenBinaryMissing(t *testing.T) {
	b := &localBackend{pwshPath: "/no/such/binary/anywhere"}

	_, err := b.RunScript(t.Context(), "ignored", nil)
	if err == nil {
		t.Fatal("expected an error when pwsh binary doesn't exist")
	}
	if errors.Is(err, ErrTimeout) {
		t.Errorf("err should not be ErrTimeout for missing-binary transport failure; got %v", err)
	}
	if !strings.Contains(err.Error(), "local backend exec") {
		t.Errorf("err = %q, want substring 'local backend exec'", err.Error())
	}
}

func TestLocalBackend_Healthcheck_FailsLoudlyOnNonZeroExit(t *testing.T) {
	t.Setenv("FAKE_PWSH_BEHAVIOR", "exit_nonzero")
	b := &localBackend{pwshPath: writeFakePwsh(t)}

	err := b.Healthcheck(t.Context())
	if err == nil {
		t.Fatal("expected Healthcheck to fail on non-zero exit")
	}
	if !strings.Contains(err.Error(), "non-zero exit") {
		t.Errorf("err = %q, want substring 'non-zero exit'", err.Error())
	}
}

func TestLocalBackend_Healthcheck_FailsOnUnexpectedStdout(t *testing.T) {
	// echo_ok emits `"hello"`, not `"pong"` — so Healthcheck's stdout
	// match should fail and surface a helpful error.
	t.Setenv("FAKE_PWSH_BEHAVIOR", "echo_ok")
	b := &localBackend{pwshPath: writeFakePwsh(t)}

	err := b.Healthcheck(t.Context())
	if err == nil {
		t.Fatal("expected Healthcheck to fail when stdout doesn't contain 'pong'")
	}
	if !strings.Contains(err.Error(), "unexpected stdout") {
		t.Errorf("err = %q, want substring 'unexpected stdout'", err.Error())
	}
}

func TestLocalBackend_Healthcheck_SuccessOnPongStdout(t *testing.T) {
	// echo_pong is what a real pwsh would emit for the 'pong' | ConvertTo-Json
	// round-trip Healthcheck runs. Lock the success path so a future
	// refactor of Healthcheck doesn't silently break the round-trip.
	t.Setenv("FAKE_PWSH_BEHAVIOR", "echo_pong")
	b := &localBackend{pwshPath: writeFakePwsh(t)}

	if err := b.Healthcheck(t.Context()); err != nil {
		t.Errorf("Healthcheck should succeed when stdout contains 'pong'; got %v", err)
	}
}

func TestNewLocal_AcceptsExplicitOverride(t *testing.T) {
	// Pass an arbitrary path; NewLocal trusts it (the user opted in via
	// HYPERV_PWSH_PATH or local.pwsh_path). Construction succeeds even if
	// the binary doesn't exist — Healthcheck is what actually invokes it.
	t.Parallel()

	conn, err := NewLocal(LocalOptions{PwshPath: "/some/path/that/need/not/exist"})
	if err != nil {
		t.Fatalf("NewLocal with override: %v", err)
	}
	if conn.Backend() != "local" {
		t.Errorf("Backend() = %q, want 'local'", conn.Backend())
	}
}

func TestNewLocal_FailsWhenNothingOnPATH(t *testing.T) {
	// Empty PATH and no override → discoverPwsh returns the actionable
	// error pointing at the env-var escape hatch.
	t.Setenv("PATH", t.TempDir())

	_, err := NewLocal(LocalOptions{})
	if err == nil {
		t.Fatal("expected NewLocal to fail when no pwsh on PATH and no override")
	}
	if !strings.Contains(err.Error(), "HYPERV_PWSH_PATH") {
		t.Errorf("err should point at the env-var override; got %q", err.Error())
	}
}

func TestDiscoverPwsh_FindsBinaryOnIsolatedPATH(t *testing.T) {
	// Drop a fake "pwsh" in a temp dir, then point PATH at only that dir
	// to verify discoverPwsh actually walks PATH (not just uses the
	// override). Caveat: this test is order-sensitive on PATH, so it's
	// not t.Parallel.
	dir := t.TempDir()
	fakePath := filepath.Join(dir, "pwsh")
	if err := os.WriteFile(fakePath, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatalf("write fake: %v", err)
	}
	t.Setenv("PATH", dir)

	got, err := discoverPwsh("")
	if err != nil {
		t.Fatalf("discoverPwsh: %v", err)
	}
	if got != fakePath {
		t.Errorf("got %q, want %q", got, fakePath)
	}
}

func TestDiscoverPwsh_ErrorsWhenNothingOnPATH(t *testing.T) {
	t.Setenv("PATH", t.TempDir()) // empty dir
	_, err := discoverPwsh("")
	if err == nil {
		t.Fatal("expected error when no pwsh / powershell.exe / powershell on PATH")
	}
	if !strings.Contains(err.Error(), "HYPERV_PWSH_PATH") {
		t.Errorf("err should mention the env-var escape hatch; got %q", err.Error())
	}
}
