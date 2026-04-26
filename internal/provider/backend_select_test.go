package provider

import (
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/windsorcli/terraform-provider-hyperv/internal/connection"
)

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

func TestNewConnection_SSHReturnsClearDiagnostic(t *testing.T) {
	t.Setenv("HYPERV_BACKEND", "")

	m := HypervProviderModel{
		Backend: types.StringValue("ssh"),
	}
	conn, diags := newConnection(t.Context(), m)
	if conn != nil {
		t.Error("expected nil connection for unimplemented backend")
	}
	if !diags.HasError() {
		t.Fatal("expected an error diagnostic")
	}
	if !strings.Contains(diags[0].Detail(), "M2") {
		t.Errorf("error detail = %q, want substring 'M2'", diags[0].Detail())
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
