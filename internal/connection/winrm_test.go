package connection

import (
	"context"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/masterzen/winrm"
)

// TestNewWinRM_RequiresHost ensures NewWinRM rejects an empty host with a
// clear, attribute-anchored message. Same shape as the SSH check.
func TestNewWinRM_RequiresHost(t *testing.T) {
	_, err := NewWinRM(WinRMOptions{
		Username: "Administrator",
		Password: "x",
	})
	if err == nil || !strings.Contains(err.Error(), "host") {
		t.Fatalf("err = %v, want substring 'host'", err)
	}
}

// TestNewWinRM_RequiresUsername mirrors the SSH backend's requirement: WinRM
// needs a user identity for NTLM/Basic; even Kerberos uses one for the SPN
// rendering. Empty username is always misconfiguration.
func TestNewWinRM_RequiresUsername(t *testing.T) {
	_, err := NewWinRM(WinRMOptions{
		Host:     "host",
		Password: "x",
	})
	if err == nil || !strings.Contains(err.Error(), "username") {
		t.Fatalf("err = %v, want substring 'username'", err)
	}
}

// TestNewWinRM_RequiresPasswordForNTLM verifies NTLM and Basic auth both
// require a password. Kerberos with a pre-cached TGT could in principle
// skip this, but Kerberos is rejected separately for now.
func TestNewWinRM_RequiresPasswordForNTLM(t *testing.T) {
	_, err := NewWinRM(WinRMOptions{
		Host:     "host",
		Username: "Administrator",
		Auth:     "ntlm",
	})
	if err == nil || !strings.Contains(err.Error(), "password") {
		t.Fatalf("err = %v, want substring 'password'", err)
	}
}

// TestNewWinRM_RequiresPasswordForBasic same as NTLM -- Basic auth is
// password-based by definition.
func TestNewWinRM_RequiresPasswordForBasic(t *testing.T) {
	_, err := NewWinRM(WinRMOptions{
		Host:     "host",
		Username: "Administrator",
		Auth:     "basic",
	})
	if err == nil || !strings.Contains(err.Error(), "password") {
		t.Fatalf("err = %v, want substring 'password'", err)
	}
}

// TestNewWinRM_RejectsKerberos pins the current scope: NTLM and Basic ship
// in the first slice; Kerberos is gated behind a "not currently implemented"
// diagnostic until SPN rendering and krb5 config are wired through. Re-check
// this when Kerberos lands.
func TestNewWinRM_RejectsKerberos(t *testing.T) {
	_, err := NewWinRM(WinRMOptions{
		Host:     "host",
		Username: "Administrator",
		Password: "x",
		Auth:     "kerberos",
	})
	if err == nil || !strings.Contains(err.Error(), "kerberos") {
		t.Fatalf("err = %v, want substring 'kerberos'", err)
	}
}

// TestNewWinRM_RejectsUnknownAuth catches typos and silently-bad config.
// The provider-level schema validator already restricts to {basic, ntlm,
// kerberos}, but defense-in-depth at the backend keeps the contract honest
// for direct callers (acc-test factories, future SDK use).
func TestNewWinRM_RejectsUnknownAuth(t *testing.T) {
	_, err := NewWinRM(WinRMOptions{
		Host:     "host",
		Username: "Administrator",
		Password: "x",
		Auth:     "bogus",
	})
	if err == nil || !strings.Contains(err.Error(), "auth") {
		t.Fatalf("err = %v, want substring 'auth'", err)
	}
}

// TestNewWinRM_DefaultPortHTTPS pins 5986 as the default for HTTPS. This
// matches the WSMan default-listener port and the schema description.
func TestNewWinRM_DefaultPortHTTPS(t *testing.T) {
	conn, err := NewWinRM(WinRMOptions{
		Host:     "host",
		Username: "Administrator",
		Password: "x",
		UseHTTPS: true,
	})
	if err != nil {
		t.Fatalf("NewWinRM: %v", err)
	}
	b, ok := conn.(*winrmBackend)
	if !ok {
		t.Fatalf("type = %T, want *winrmBackend", conn)
	}
	if b.opts.Port != 5986 {
		t.Errorf("Port = %d, want 5986", b.opts.Port)
	}
}

// TestNewWinRM_DefaultPortHTTP pins 5985 for HTTP. Operators rarely use
// this in production -- NTLM creds without TLS are exposed -- but it's the
// WSMan default and useful for diagnosing TLS-only failures.
func TestNewWinRM_DefaultPortHTTP(t *testing.T) {
	conn, err := NewWinRM(WinRMOptions{
		Host:     "host",
		Username: "Administrator",
		Password: "x",
		UseHTTPS: false,
	})
	if err != nil {
		t.Fatalf("NewWinRM: %v", err)
	}
	b, ok := conn.(*winrmBackend)
	if !ok {
		t.Fatalf("type = %T, want *winrmBackend", conn)
	}
	if b.opts.Port != 5985 {
		t.Errorf("Port = %d, want 5985", b.opts.Port)
	}
}

// TestNewWinRM_PortOutOfRange catches operator misconfig (HYPERV_PORT=99999,
// negative attribute value) at Configure time rather than letting an opaque
// "invalid port" surface from net.Dial mid-plan. Mirrors the SSH backend's
// bounds check. Port 0 is treated as "unset, apply default" -- see the
// dedicated default-port tests above.
func TestNewWinRM_PortOutOfRange(t *testing.T) {
	for _, port := range []int{-1, 65536, 99999} {
		_, err := NewWinRM(WinRMOptions{
			Host:     "host",
			Username: "Administrator",
			Password: "x",
			Port:     port,
		})
		if err == nil || !strings.Contains(err.Error(), "port") {
			t.Errorf("port=%d: err = %v, want 'port' substring", port, err)
		}
	}
}

// TestNewWinRM_AuthDefaultsToNTLM pins NTLM as the default when Auth is
// unset. Most workgroup Hyper-V hosts run NTLM; making it explicit avoids
// surprising silent-Basic fallback.
func TestNewWinRM_AuthDefaultsToNTLM(t *testing.T) {
	conn, err := NewWinRM(WinRMOptions{
		Host:     "host",
		Username: "Administrator",
		Password: "x",
	})
	if err != nil {
		t.Fatalf("NewWinRM: %v", err)
	}
	b, ok := conn.(*winrmBackend)
	if !ok {
		t.Fatalf("type = %T, want *winrmBackend", conn)
	}
	if b.opts.Auth != "ntlm" {
		t.Errorf("Auth = %q, want %q", b.opts.Auth, "ntlm")
	}
}

// TestWinRM_BackendIdentifier confirms the lowercase identifier used for
// tflog field decoration. The schema's `backend` attribute is the
// user-facing form; this is the internal one.
func TestWinRM_BackendIdentifier(t *testing.T) {
	conn, err := NewWinRM(WinRMOptions{
		Host:     "host",
		Username: "Administrator",
		Password: "x",
	})
	if err != nil {
		t.Fatalf("NewWinRM: %v", err)
	}
	if got := conn.Backend(); got != "winrm" {
		t.Errorf("Backend() = %q, want %q", got, "winrm")
	}
}

// TestWinRM_CloseIdempotent guards against double-close panics. The backend
// has no persistent state to release, so Close is essentially a no-op, but
// the contract still requires idempotency.
func TestWinRM_CloseIdempotent(t *testing.T) {
	conn, err := NewWinRM(WinRMOptions{
		Host:     "host",
		Username: "Administrator",
		Password: "x",
	})
	if err != nil {
		t.Fatalf("NewWinRM: %v", err)
	}
	if err := conn.Close(); err != nil {
		t.Errorf("first Close: %v", err)
	}
	if err := conn.Close(); err != nil {
		t.Errorf("second Close: %v", err)
	}
}

// TestBuildWinRMParams_DoesNotMutateGlobal pins the bugfix that prevents
// per-backend params from aliasing winrm.DefaultParameters. The upstream
// library declares DefaultParameters as a *Parameters, so a naive
// `params := winrm.DefaultParameters; params.Timeout = ...` mutates the
// shared global -- racing across concurrent Open calls and silently
// affecting later Opens (e.g., a Basic-auth Open clearing
// TransportDecorator would persist into a subsequent NTLM Open).
//
// This test pins the value-copy contract: build params for two
// differently-configured backends, mutate the result of the first, and
// verify both winrm.DefaultParameters and the second backend's params
// remain untouched.
func TestBuildWinRMParams_DoesNotMutateGlobal(t *testing.T) {
	originalTimeout := winrm.DefaultParameters.Timeout
	originalDecorator := winrm.DefaultParameters.TransportDecorator

	pBasic := buildWinRMParams(WinRMOptions{
		Auth:           "basic",
		CommandTimeout: time.Minute,
	})
	pNTLM := buildWinRMParams(WinRMOptions{
		Auth:           "ntlm",
		CommandTimeout: 5 * time.Second,
	})

	// Mutate the first backend's params -- if buildWinRMParams aliased
	// the global, this write would corrupt subsequent calls.
	pBasic.Timeout = "PT99H"
	pBasic.EnvelopeSize = 999

	if winrm.DefaultParameters.Timeout != originalTimeout {
		t.Errorf("DefaultParameters.Timeout = %q, want unchanged %q",
			winrm.DefaultParameters.Timeout, originalTimeout)
	}
	if !sameDecoratorRef(winrm.DefaultParameters.TransportDecorator, originalDecorator) {
		t.Error("DefaultParameters.TransportDecorator was mutated")
	}
	if pNTLM.Timeout == "PT99H" {
		t.Error("second backend's Timeout aliased the first's")
	}
	if pNTLM.EnvelopeSize == 999 {
		t.Error("second backend's EnvelopeSize aliased the first's")
	}
	// And the auth=basic path must clear TransportDecorator on its own
	// copy without touching the auth=ntlm path's decorator.
	if pBasic.TransportDecorator != nil {
		t.Error("auth=basic should clear TransportDecorator on its copy")
	}
	if pNTLM.TransportDecorator == nil && originalDecorator != nil {
		t.Error("auth=ntlm should preserve the default TransportDecorator")
	}
}

// sameDecoratorRef compares two function values by their underlying
// pointer. Go forbids `==` between func values directly; reflect.Value.
// Pointer is the canonical workaround for asking whether two references
// point at the same underlying function.
func sameDecoratorRef(a, b func() winrm.Transporter) bool {
	if (a == nil) != (b == nil) {
		return false
	}
	if a == nil {
		return true
	}
	return reflect.ValueOf(a).Pointer() == reflect.ValueOf(b).Pointer()
}

// TestWinRM_RunScriptBeforeOpen returns a clear "not open" error so a
// programmer mistake (forgetting Configure's Open) surfaces with a
// load-bearing message instead of a confusing nil-deref or auth-style
// failure mid-call.
func TestWinRM_RunScriptBeforeOpen(t *testing.T) {
	conn, err := NewWinRM(WinRMOptions{
		Host:     "host",
		Username: "Administrator",
		Password: "x",
	})
	if err != nil {
		t.Fatalf("NewWinRM: %v", err)
	}
	_, err = conn.RunScript(context.Background(), `Write-Output ok`, nil)
	if err == nil || !strings.Contains(err.Error(), "not open") {
		t.Errorf("err = %v, want substring 'not open'", err)
	}
}
