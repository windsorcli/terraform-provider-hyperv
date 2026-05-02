package provider

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/hashicorp/terraform-plugin-framework/types"
	"golang.org/x/crypto/ssh"

	"github.com/windsorcli/terraform-provider-hyperv/internal/connection"
)

// makeTestPrivateKey returns a freshly-minted ed25519 private key in OpenSSH
// PEM form. Used by tests that need a parseable key without depending on a
// real key file in the repo.
func makeTestPrivateKey(t *testing.T) []byte {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate ed25519 key: %v", err)
	}
	block, err := ssh.MarshalPrivateKey(priv, "")
	if err != nil {
		t.Fatalf("marshal ed25519 key: %v", err)
	}
	return pem.EncodeToMemory(block)
}

// makeKnownHostsForTest writes a temp known_hosts file with one well-formed
// host entry so loadKnownHostsCallback succeeds. The entry doesn't need to
// match a real server -- these tests build the SSH connection but never
// dial.
func makeKnownHostsForTest(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "known_hosts")

	pub, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate ed25519 host key: %v", err)
	}
	sshPub, err := ssh.NewPublicKey(pub)
	if err != nil {
		t.Fatalf("ssh.NewPublicKey: %v", err)
	}
	line := "hyperv.example.com " + string(ssh.MarshalAuthorizedKey(sshPub))
	if err := os.WriteFile(path, []byte(line), 0o600); err != nil {
		t.Fatalf("write known_hosts: %v", err)
	}
	return path
}

func TestNewConnection_LocalDefault(t *testing.T) {
	// Not parallel: this test mutates env vars via t.Setenv.
	t.Setenv("HYPERV_BACKEND", "")
	t.Setenv("HYPERV_PWSH_PATH", "/tmp/fake-pwsh-that-doesnt-need-to-exist")

	m := HypervProviderModel{}
	conn, diags := newConnection(t.Context(), m)
	if diags.HasError() {
		t.Fatalf("unexpected diags: %v", diags)
	}
	if conn == nil {
		t.Fatal("expected a connection, got nil")
	}
	if conn.Backend() != "local" {
		t.Errorf("Backend() = %q, want local", conn.Backend())
	}
}

func TestNewConnection_LocalAttributeWinsOverEnv(t *testing.T) {
	// The §6 precedence: provider attribute > env var. With both set,
	// the attribute (via the Local nested block) takes effect.
	t.Setenv("HYPERV_BACKEND", "")
	t.Setenv("HYPERV_PWSH_PATH", "/from/env")

	m := HypervProviderModel{
		Local: &LocalConfig{
			PwshPath: types.StringValue("/from/attr"),
		},
	}
	conn, diags := newConnection(t.Context(), m)
	if diags.HasError() {
		t.Fatalf("unexpected diags: %v", diags)
	}
	if conn == nil {
		t.Fatal("expected a connection")
	}
	// We can't read pwshPath off the Connection directly (unexported),
	// but Backend() == "local" + no construction error confirms the
	// nested-block path was taken.
	if conn.Backend() != "local" {
		t.Errorf("Backend() = %q, want local", conn.Backend())
	}
}

// SSH backend wires successfully when host + username + auth are all set.
// Doesn't dial -- Open is what dials -- so the test is fast and offline.
func TestNewConnection_SSHHappyPath(t *testing.T) {
	t.Setenv("HYPERV_BACKEND", "")

	knownHosts := makeKnownHostsForTest(t)
	m := HypervProviderModel{
		Backend:  types.StringValue("ssh"),
		Host:     types.StringValue("hyperv.example.com"),
		Username: types.StringValue("admin"),
		SSH: &SSHConfig{
			PrivateKey:     types.StringValue(string(makeTestPrivateKey(t))),
			KnownHostsPath: types.StringValue(knownHosts),
		},
	}
	conn, diags := newConnection(t.Context(), m)
	if diags.HasError() {
		t.Fatalf("unexpected diags: %v", diags)
	}
	if conn == nil {
		t.Fatal("expected a connection")
	}
	if conn.Backend() != "ssh" {
		t.Errorf("Backend() = %q, want ssh", conn.Backend())
	}
}

// Missing host on the SSH backend fails with a clear, attribute-anchored
// diagnostic so operators see exactly which knob is unset.
func TestNewConnection_SSHRequiresHost(t *testing.T) {
	t.Setenv("HYPERV_BACKEND", "")
	t.Setenv("HYPERV_HOST", "")

	m := HypervProviderModel{
		Backend:  types.StringValue("ssh"),
		Username: types.StringValue("admin"),
	}
	_, diags := newConnection(t.Context(), m)
	if !diags.HasError() {
		t.Fatal("expected an error diagnostic")
	}
	if !strings.Contains(diags[0].Summary(), "host") {
		t.Errorf("error summary = %q, want host-related", diags[0].Summary())
	}
}

// Missing username on the SSH backend likewise fails clearly.
func TestNewConnection_SSHRequiresUsername(t *testing.T) {
	t.Setenv("HYPERV_BACKEND", "")
	t.Setenv("HYPERV_USERNAME", "")

	m := HypervProviderModel{
		Backend: types.StringValue("ssh"),
		Host:    types.StringValue("hyperv.example.com"),
	}
	_, diags := newConnection(t.Context(), m)
	if !diags.HasError() {
		t.Fatal("expected an error diagnostic")
	}
	if !strings.Contains(diags[0].Summary(), "username") {
		t.Errorf("error summary = %q, want username-related", diags[0].Summary())
	}
}

// Env-var fallbacks for SSH-specific attributes -- the operator can wire
// auth purely through env without touching the provider block.
func TestNewConnection_SSHEnvVarFallbacks(t *testing.T) {
	t.Setenv("HYPERV_BACKEND", "ssh")
	t.Setenv("HYPERV_HOST", "from-env-host")
	t.Setenv("HYPERV_USERNAME", "from-env-user")
	t.Setenv("HYPERV_PORT", "2222")
	t.Setenv("HYPERV_SSH_PRIVATE_KEY", string(makeTestPrivateKey(t)))
	t.Setenv("HYPERV_SSH_KNOWN_HOSTS_PATH", makeKnownHostsForTest(t))

	conn, diags := newConnection(t.Context(), HypervProviderModel{})
	if diags.HasError() {
		t.Fatalf("unexpected diags: %v", diags)
	}
	if conn == nil {
		t.Fatal("expected a connection")
	}
	if conn.Backend() != "ssh" {
		t.Errorf("Backend() = %q, want ssh", conn.Backend())
	}
}

// Bogus port via env should produce a clear error rather than silently
// becoming zero (which would later fail the SSH dial with "connection
// refused on :0" -- much harder to debug).
func TestNewConnection_SSHInvalidPortEnv(t *testing.T) {
	t.Setenv("HYPERV_BACKEND", "ssh")
	t.Setenv("HYPERV_PORT", "not-a-number")

	m := HypervProviderModel{
		Host:     types.StringValue("h"),
		Username: types.StringValue("u"),
		SSH: &SSHConfig{
			PrivateKey:     types.StringValue(string(makeTestPrivateKey(t))),
			KnownHostsPath: types.StringValue(makeKnownHostsForTest(t)),
		},
	}
	_, diags := newConnection(t.Context(), m)
	if !diags.HasError() {
		t.Fatal("expected an error diagnostic")
	}
	if !strings.Contains(diags[0].Summary(), "port") {
		t.Errorf("error summary = %q, want port-related", diags[0].Summary())
	}
}

// Out-of-range port values must fail at Configure time with an
// attribute-anchored diagnostic, not silently propagate to net.Dial.
func TestNewConnection_SSHPortOutOfRange(t *testing.T) {
	cases := []struct {
		name string
		port int64
	}{
		{name: "zero", port: 0},
		{name: "negative", port: -1},
		{name: "above 65535", port: 65536},
		{name: "way above", port: 99999},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("HYPERV_BACKEND", "ssh")
			t.Setenv("HYPERV_PORT", "")

			m := HypervProviderModel{
				Host:     types.StringValue("h"),
				Username: types.StringValue("u"),
				Port:     types.Int64Value(tc.port),
				SSH: &SSHConfig{
					PrivateKey:     types.StringValue(string(makeTestPrivateKey(t))),
					KnownHostsPath: types.StringValue(makeKnownHostsForTest(t)),
				},
			}
			_, diags := newConnection(t.Context(), m)
			if !diags.HasError() {
				t.Fatalf("port=%d: expected an error diagnostic", tc.port)
			}
			if !strings.Contains(diags[0].Summary(), "port") {
				t.Errorf("port=%d: error summary = %q, want port-related", tc.port, diags[0].Summary())
			}
			if !strings.Contains(diags[0].Detail(), "1 and 65535") {
				t.Errorf("port=%d: error detail should name the valid range; got %q",
					tc.port, diags[0].Detail())
			}
		})
	}
}

// TestNewConnection_WinRMRequiresHost mirrors the SSH and Local equivalents:
// missing host produces an attribute-anchored diagnostic rather than a
// confusing later "could not connect" failure mid-plan.
func TestNewConnection_WinRMRequiresHost(t *testing.T) {
	t.Setenv("HYPERV_BACKEND", "")
	t.Setenv("HYPERV_HOST", "")
	t.Setenv("HYPERV_USERNAME", "")

	m := HypervProviderModel{
		Backend: types.StringValue("winrm"),
	}
	conn, diags := newConnection(t.Context(), m)
	if conn != nil {
		t.Error("expected nil connection when host is missing")
	}
	if !diags.HasError() {
		t.Fatal("expected an error diagnostic")
	}
	if !strings.Contains(diags[0].Detail(), "host") {
		t.Errorf("error detail = %q, want substring 'host'", diags[0].Detail())
	}
}

// TestNewConnection_WinRMBuildsBackend verifies the happy path: with host,
// username, and password set, newConnection returns a non-nil winrm-backed
// Connection without error. The actual network call is in Open, exercised
// by acceptance tests against the bench.
func TestNewConnection_WinRMBuildsBackend(t *testing.T) {
	t.Setenv("HYPERV_BACKEND", "")
	t.Setenv("HYPERV_HOST", "")
	t.Setenv("HYPERV_USERNAME", "")
	t.Setenv("HYPERV_PASSWORD", "")

	m := HypervProviderModel{
		Backend:  types.StringValue("winrm"),
		Host:     types.StringValue("hv01.example.com"),
		Username: types.StringValue("Administrator"),
		Password: types.StringValue("placeholder"),
	}
	conn, diags := newConnection(t.Context(), m)
	if diags.HasError() {
		t.Fatalf("unexpected diagnostics: %v", diags)
	}
	if conn == nil {
		t.Fatal("expected non-nil connection")
	}
	if got := conn.Backend(); got != "winrm" {
		t.Errorf("Backend() = %q, want %q", got, "winrm")
	}
}

// TestNewConnection_WinRMBasicWithoutHTTPSWarns verifies the operator-
// safety guard: the auth=basic + use_https=false combination sends creds
// as plaintext-base64. We don't hard-block (the schema doc explicitly
// keeps it as a TLS-only diagnostic option), but a plan-time warning
// keeps the risky combo from landing in production config silently.
func TestNewConnection_WinRMBasicWithoutHTTPSWarns(t *testing.T) {
	t.Setenv("HYPERV_BACKEND", "")
	t.Setenv("HYPERV_HOST", "")
	t.Setenv("HYPERV_USERNAME", "")
	t.Setenv("HYPERV_PASSWORD", "")
	t.Setenv("HYPERV_WINRM_USE_HTTPS", "")
	t.Setenv("HYPERV_WINRM_AUTH", "")

	m := HypervProviderModel{
		Backend:  types.StringValue("winrm"),
		Host:     types.StringValue("hv01.example.com"),
		Username: types.StringValue("Administrator"),
		Password: types.StringValue("placeholder"),
		WinRM: &WinRMConfig{
			UseHTTPS: types.BoolValue(false),
			Auth:     types.StringValue("basic"),
		},
	}
	conn, diags := newConnection(t.Context(), m)
	if diags.HasError() {
		t.Fatalf("unexpected error diagnostics: %v", diags.Errors())
	}
	if conn == nil {
		t.Fatal("expected non-nil connection (warning, not error)")
	}
	warnings := diags.Warnings()
	if len(warnings) == 0 {
		t.Fatal("expected at least one warning diagnostic")
	}
	found := false
	for _, w := range warnings {
		if strings.Contains(w.Summary(), "Basic auth over HTTP") ||
			strings.Contains(w.Detail(), "cleartext") {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected a warning about Basic-over-HTTP cleartext exposure; got %v", warnings)
	}
}

// TestNewConnection_WinRMBasicWithHTTPSDoesNotWarn confirms the warning
// is gated on the *combination* -- Basic auth over HTTPS is fine (the
// Authorization header rides encrypted transport) and shouldn't trigger
// the diagnostic.
func TestNewConnection_WinRMBasicWithHTTPSDoesNotWarn(t *testing.T) {
	t.Setenv("HYPERV_BACKEND", "")
	t.Setenv("HYPERV_HOST", "")
	t.Setenv("HYPERV_USERNAME", "")
	t.Setenv("HYPERV_PASSWORD", "")
	t.Setenv("HYPERV_WINRM_USE_HTTPS", "")
	t.Setenv("HYPERV_WINRM_AUTH", "")

	m := HypervProviderModel{
		Backend:  types.StringValue("winrm"),
		Host:     types.StringValue("hv01.example.com"),
		Username: types.StringValue("Administrator"),
		Password: types.StringValue("placeholder"),
		WinRM: &WinRMConfig{
			UseHTTPS: types.BoolValue(true),
			Auth:     types.StringValue("basic"),
		},
	}
	_, diags := newConnection(t.Context(), m)
	if diags.HasError() {
		t.Fatalf("unexpected error diagnostics: %v", diags.Errors())
	}
	for _, w := range diags.Warnings() {
		if strings.Contains(w.Summary(), "Basic auth over HTTP") {
			t.Errorf("did not expect cleartext warning when use_https=true; got %v", w)
		}
	}
}

func TestNewConnection_InvalidBackend(t *testing.T) {
	t.Setenv("HYPERV_BACKEND", "")

	m := HypervProviderModel{
		Backend: types.StringValue("garbage"),
	}
	_, diags := newConnection(t.Context(), m)
	if !diags.HasError() {
		t.Fatal("expected an error diagnostic for invalid backend")
	}
	if !strings.Contains(diags[0].Detail(), "garbage") {
		t.Errorf("error detail should name the offending value; got %q", diags[0].Detail())
	}
}

func TestResolveString_AttributeWinsOverEnvVar(t *testing.T) {
	t.Setenv("FOO", "from-env")
	got := resolveString(types.StringValue("from-attr"), "FOO", "default")
	if got != "from-attr" {
		t.Errorf("got %q, want from-attr", got)
	}
}

func TestResolveString_EnvVarFallback(t *testing.T) {
	t.Setenv("FOO", "from-env")
	got := resolveString(types.StringNull(), "FOO", "default")
	if got != "from-env" {
		t.Errorf("got %q, want from-env", got)
	}
}

func TestResolveString_FallbackWhenAllMissing(t *testing.T) {
	t.Setenv("FOO", "")
	got := resolveString(types.StringNull(), "FOO", "default")
	if got != "default" {
		t.Errorf("got %q, want default", got)
	}
}

func TestResolveString_UnknownAttributeFallsThroughToEnv(t *testing.T) {
	// During plan, attributes can be types.StringUnknown if they reference
	// another resource's computed output. resolveString should treat that
	// the same as null and fall through to the env var.
	t.Setenv("FOO", "from-env")
	got := resolveString(types.StringUnknown(), "FOO", "default")
	if got != "from-env" {
		t.Errorf("got %q, want from-env", got)
	}
}

// resolveDuration's job: parse attr || env-var, return 0 + nil when both
// missing so the caller's default applies.
func TestResolveDuration_AttributeWinsOverEnvVar(t *testing.T) {
	t.Setenv("FOO_TIMEOUT", "10s")
	got, err := resolveDuration(types.StringValue("90s"), "FOO_TIMEOUT")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != 90*time.Second {
		t.Errorf("got %v, want 90s", got)
	}
}

func TestResolveDuration_EnvVarFallback(t *testing.T) {
	t.Setenv("FOO_TIMEOUT", "2m")
	got, err := resolveDuration(types.StringNull(), "FOO_TIMEOUT")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != 2*time.Minute {
		t.Errorf("got %v, want 2m", got)
	}
}

func TestResolveDuration_BothMissingReturnsZero(t *testing.T) {
	t.Setenv("FOO_TIMEOUT", "")
	got, err := resolveDuration(types.StringNull(), "FOO_TIMEOUT")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != 0 {
		t.Errorf("got %v, want 0", got)
	}
}

func TestResolveDuration_UnparseableErrors(t *testing.T) {
	t.Setenv("FOO_TIMEOUT", "")
	_, err := resolveDuration(types.StringValue("not-a-duration"), "FOO_TIMEOUT")
	if err == nil {
		t.Fatal("expected an error for an unparseable duration; got nil")
	}
}

// Compile-time check that NewLocal returns a Connection (not just a Runner).
// NewLocal's return type is `Connection` already, so the assignment below
// is the actual assertion — staticcheck QF1011 wants the redundant type
// dropped from the LHS.
func TestLocalImplementsConnection(t *testing.T) {
	t.Parallel()
	_ = mustLocal(t)
}

func mustLocal(t *testing.T) connection.Connection {
	t.Helper()
	conn, err := connection.NewLocal(connection.LocalOptions{
		PwshPath: "/tmp/fake-doesnt-need-to-exist-for-construction",
	})
	if err != nil {
		t.Fatalf("NewLocal: %v", err)
	}
	return conn
}
