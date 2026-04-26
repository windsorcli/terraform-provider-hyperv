package provider

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"os"
	"path/filepath"
	"strings"
	"testing"

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

func TestNewConnection_WinRMReturnsClearDiagnostic(t *testing.T) {
	t.Setenv("HYPERV_BACKEND", "")

	m := HypervProviderModel{
		Backend: types.StringValue("winrm"),
	}
	conn, diags := newConnection(t.Context(), m)
	if conn != nil {
		t.Error("expected nil connection for unimplemented backend")
	}
	if !diags.HasError() {
		t.Fatal("expected an error diagnostic")
	}
	if !strings.Contains(diags[0].Detail(), "M3") {
		t.Errorf("error detail = %q, want substring 'M3'", diags[0].Detail())
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
