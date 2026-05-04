package connection

import (
	"bytes"
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

// TestNewWinRM_KerberosValidationErrors covers the three negative paths the
// Kerberos auth construction guards: realm-required, password-XOR-ccache
// (both set), password-XOR-ccache (neither set). Each surfaces as an
// inline NewWinRM error so misconfig lands at provider-Configure rather
// than as an opaque mid-plan SPNEGO failure.
func TestNewWinRM_KerberosValidationErrors(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		opts WinRMOptions
		want string // substring of expected error
	}{
		{
			name: "empty realm",
			opts: WinRMOptions{
				Host:     "hv.example.com",
				Username: "Administrator",
				Password: "x",
				Auth:     "kerberos",
			},
			want: "realm",
		},
		{
			name: "both password and ccache",
			opts: WinRMOptions{
				Host:          "hv.example.com",
				Username:      "Administrator",
				Password:      "x",
				Auth:          "kerberos",
				KrbRealm:      "EXAMPLE.COM",
				KrbCCachePath: "/tmp/krb5cc",
			},
			want: "mutually exclusive",
		},
		{
			name: "neither password nor ccache",
			opts: WinRMOptions{
				Host:     "hv.example.com",
				Username: "Administrator",
				Auth:     "kerberos",
				KrbRealm: "EXAMPLE.COM",
			},
			want: "password or kerberos.ccache_path",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, err := NewWinRM(tc.opts)
			if err == nil {
				t.Fatalf("err = nil, want non-nil with substring %q", tc.want)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("err = %q, want substring %q", err.Error(), tc.want)
			}
		})
	}
}

// TestNewWinRM_KerberosPasswordMode is the happy-path: realm + password,
// no ccache. Verifies fields land on the backend opts and SPN renders
// to HTTP/<host> when not explicitly set.
func TestNewWinRM_KerberosPasswordMode(t *testing.T) {
	t.Parallel()

	conn, err := NewWinRM(WinRMOptions{
		Host:     "hv-bench-01.hv.lab",
		Username: "Administrator",
		Password: "secret",
		Auth:     "kerberos",
		KrbRealm: "HV.LAB",
	})
	if err != nil {
		t.Fatalf("NewWinRM: %v", err)
	}
	b, ok := conn.(*winrmBackend)
	if !ok {
		t.Fatalf("type = %T, want *winrmBackend", conn)
	}
	if b.opts.Auth != "kerberos" {
		t.Errorf("Auth = %q, want kerberos", b.opts.Auth)
	}
	if b.opts.KrbRealm != "HV.LAB" {
		t.Errorf("KrbRealm = %q, want HV.LAB", b.opts.KrbRealm)
	}
	if b.opts.KrbSpn != "HTTP/hv-bench-01.hv.lab" {
		t.Errorf("KrbSpn default = %q, want HTTP/hv-bench-01.hv.lab", b.opts.KrbSpn)
	}
}

// TestNewWinRM_KerberosCCacheMode is the alternate happy-path: realm +
// ccache, no password. Verifies the password-less invocation passes
// validation and the ccache path threads through.
func TestNewWinRM_KerberosCCacheMode(t *testing.T) {
	t.Parallel()

	conn, err := NewWinRM(WinRMOptions{
		Host:          "hv-bench-01.hv.lab",
		Username:      "ryan@HV.LAB",
		Auth:          "kerberos",
		KrbRealm:      "HV.LAB",
		KrbCCachePath: "/tmp/krb5cc_501",
	})
	if err != nil {
		t.Fatalf("NewWinRM: %v", err)
	}
	b, ok := conn.(*winrmBackend)
	if !ok {
		t.Fatalf("type = %T, want *winrmBackend", conn)
	}
	if b.opts.KrbCCachePath != "/tmp/krb5cc_501" {
		t.Errorf("KrbCCachePath = %q, want /tmp/krb5cc_501", b.opts.KrbCCachePath)
	}
	if b.opts.Password != "" {
		t.Errorf("Password = %q, want empty (ccache mode does not require it)", b.opts.Password)
	}
}

// TestNewWinRM_KerberosExplicitSPN locks the override path: when KrbSpn
// is set explicitly, NewWinRM does NOT render the HTTP/<host> default.
// This matters for sites that registered a non-standard SPN (e.g. an
// instance-specific HTTP/hv-bench-01.hv.lab:5986).
func TestNewWinRM_KerberosExplicitSPN(t *testing.T) {
	t.Parallel()

	conn, err := NewWinRM(WinRMOptions{
		Host:     "hv-bench-01.hv.lab",
		Username: "Administrator",
		Password: "x",
		Auth:     "kerberos",
		KrbRealm: "HV.LAB",
		KrbSpn:   "HTTP/wsman.hv.lab",
	})
	if err != nil {
		t.Fatalf("NewWinRM: %v", err)
	}
	b, ok := conn.(*winrmBackend)
	if !ok {
		t.Fatalf("type = %T, want *winrmBackend", conn)
	}
	if b.opts.KrbSpn != "HTTP/wsman.hv.lab" {
		t.Errorf("KrbSpn = %q, want HTTP/wsman.hv.lab (no default override)", b.opts.KrbSpn)
	}
}

// TestNewWinRM_KerberosKrb5ConfigFromEnv exercises the canonical-path
// detection: when KrbConfigPath is empty, defaultKrbConfigPath probes
// $KRB5_CONFIG first. The other priorities (~/.config/krb5.conf,
// /etc/krb5.conf) depend on filesystem state and aren't unit-testable
// hermetically; t.Setenv covers the most common operator-driven case.
func TestNewWinRM_KerberosKrb5ConfigFromEnv(t *testing.T) {
	t.Setenv("KRB5_CONFIG", "/custom/path/krb5.conf")

	conn, err := NewWinRM(WinRMOptions{
		Host:     "hv.example.com",
		Username: "Administrator",
		Password: "x",
		Auth:     "kerberos",
		KrbRealm: "EXAMPLE.COM",
	})
	if err != nil {
		t.Fatalf("NewWinRM: %v", err)
	}
	b, ok := conn.(*winrmBackend)
	if !ok {
		t.Fatalf("type = %T, want *winrmBackend", conn)
	}
	if b.opts.KrbConfigPath != "/custom/path/krb5.conf" {
		t.Errorf("KrbConfigPath = %q, want /custom/path/krb5.conf (from KRB5_CONFIG)", b.opts.KrbConfigPath)
	}
}

// TestBuildWinRMParams_KerberosSetsTransportDecorator verifies the
// load-bearing handoff: when Auth=kerberos, the masterzen-library
// Parameters get a non-nil TransportDecorator that returns the
// Kerberos transport. Without this, the library's NewClient falls
// back to NTLM/Negotiate and the Kerberos config is silently
// ignored -- exactly the regression the spike doc warned about.
func TestBuildWinRMParams_KerberosSetsTransportDecorator(t *testing.T) {
	t.Parallel()

	params := buildWinRMParams(WinRMOptions{
		Host:          "hv.example.com",
		Port:          5986,
		Username:      "Administrator",
		Password:      "x",
		UseHTTPS:      true,
		Auth:          "kerberos",
		KrbRealm:      "EXAMPLE.COM",
		KrbSpn:        "HTTP/hv.example.com",
		KrbConfigPath: "/etc/krb5.conf",
	})
	if params.TransportDecorator == nil {
		t.Fatal("TransportDecorator = nil, want non-nil for kerberos auth")
	}
	transport := params.TransportDecorator()
	// The masterzen library's *ClientKerberos is the only type we expect
	// here. A drift to a different transport (NTLM/Negotiate decorator,
	// nil clientRequest) would silently bypass Kerberos at runtime.
	if _, ok := transport.(*winrm.ClientKerberos); !ok {
		t.Errorf("TransportDecorator returned %T, want *winrm.ClientKerberos", transport)
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

func TestWinRM_StreamFileBeforeOpen(t *testing.T) {
	t.Parallel()

	conn, err := NewWinRM(WinRMOptions{
		Host:     "host",
		Username: "Administrator",
		Password: "x",
	})
	if err != nil {
		t.Fatalf("NewWinRM: %v", err)
	}
	err = conn.StreamFile(context.Background(), "/tmp/anywhere", "C:/anywhere")
	if err == nil || !strings.Contains(err.Error(), "not open") {
		t.Errorf("err = %v, want substring 'not open'", err)
	}
}

// TestBuildWinRMStreamFileScript pins the receiver script's shape. We
// don't compare the whole string verbatim -- comments would lock readers
// out of refactors -- but we do verify every load-bearing piece is in
// place: stdin encoding override, OpenWrite + SetLength(0), the ReadLine
// loop, FromBase64String, the finally Dispose, and that path escaping
// doubles single quotes per PS single-string conventions.
func TestBuildWinRMStreamFileScript(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		path string
		want []string // substrings that must all appear
	}{
		{
			name: "plain windows path",
			path: `C:\hyperv\iso\foo.iso`,
			want: []string{
				`[Console]::InputEncoding = [Text.UTF8Encoding]::new($false)`,
				`[IO.File]::OpenWrite('C:\hyperv\iso\foo.iso')`,
				`$stream.SetLength(0)`,
				`while ($null -ne ($line = $reader.ReadLine()))`,
				`[Convert]::FromBase64String($line)`,
				`finally { $stream.Dispose() }`,
			},
		},
		{
			name: "forward slashes pass through",
			path: `C:/hyperv/iso/foo.iso`,
			want: []string{`[IO.File]::OpenWrite('C:/hyperv/iso/foo.iso')`},
		},
		{
			name: "single quote in path is doubled",
			path: `C:\weird's\path.iso`,
			want: []string{`[IO.File]::OpenWrite('C:\weird''s\path.iso')`},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := buildWinRMStreamFileScript(tc.path)
			for _, sub := range tc.want {
				if !strings.Contains(got, sub) {
					t.Errorf("script missing %q\nfull script: %s", sub, got)
				}
			}
		})
	}
}

// TestLineWrappedWriter_Behavior covers the wrap-and-flush logic with
// known inputs. The base64 over WinRM stream relies on these edge cases
// so the receiver's ReadLine loop sees correctly-bounded lines.
func TestLineWrappedWriter_Behavior(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name    string
		lineLen int
		writes  []string
		want    string
	}{
		{
			name:    "empty input writes nothing",
			lineLen: 4,
			writes:  []string{},
			want:    "",
		},
		{
			name:    "shorter than line emits no newline pre-Close",
			lineLen: 4,
			writes:  []string{"abc"},
			want:    "abc\n", // Close emits the trailing \n
		},
		{
			name:    "exact line emits trailing newline on Close",
			lineLen: 4,
			writes:  []string{"abcd"},
			want:    "abcd\n",
		},
		{
			name:    "two full lines split with newline",
			lineLen: 4,
			writes:  []string{"abcdefgh"},
			want:    "abcd\nefgh\n",
		},
		{
			name:    "writes spanning a boundary",
			lineLen: 4,
			writes:  []string{"abc", "defgh"},
			want:    "abcd\nefgh\n",
		},
		{
			name:    "many small writes still wrap correctly",
			lineLen: 3,
			writes:  []string{"a", "b", "c", "d", "e", "f", "g"},
			want:    "abc\ndef\ng\n",
		},
		{
			name:    "exact-multiple input ends with newline",
			lineLen: 2,
			writes:  []string{"abcdef"},
			want:    "ab\ncd\nef\n",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var buf bytes.Buffer
			lw := newLineWrappedWriter(&buf, tc.lineLen)
			for _, s := range tc.writes {
				if _, err := lw.Write([]byte(s)); err != nil {
					t.Fatalf("Write(%q): %v", s, err)
				}
			}
			if err := lw.Close(); err != nil {
				t.Fatalf("Close: %v", err)
			}
			if got := buf.String(); got != tc.want {
				t.Errorf("\n got: %q\nwant: %q", got, tc.want)
			}
		})
	}
}

// TestLineWrappedWriter_CloseIdempotent ensures a second Close is a
// no-op rather than emitting a stray newline. The streaming pipeline
// only calls Close once today, but a future caller that double-closes
// (defer + explicit) shouldn't corrupt the wire format.
func TestLineWrappedWriter_CloseIdempotent(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer
	lw := newLineWrappedWriter(&buf, 4)
	if _, err := lw.Write([]byte("ab")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if err := lw.Close(); err != nil {
		t.Fatalf("first Close: %v", err)
	}
	if err := lw.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}
	if got, want := buf.String(), "ab\n"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}
