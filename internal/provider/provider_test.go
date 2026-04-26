package provider

import (
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/provider"
)

// Sanity check that the provider type satisfies the framework interface and
// metadata reports the expected name. More substantive Configure tests land
// at acceptance tier (TF_ACC=1) since Configure exercises real pwsh via
// Healthcheck — component tests in connection/ and provider/backend_select_test.go
// cover the meaningful logic.
func TestProvider_Metadata(t *testing.T) {
	t.Parallel()

	p := New("test-version")()
	resp := &provider.MetadataResponse{}
	p.Metadata(t.Context(), provider.MetadataRequest{}, resp)

	if resp.TypeName != "hyperv" {
		t.Errorf("TypeName = %q, want %q", resp.TypeName, "hyperv")
	}
	if resp.Version != "test-version" {
		t.Errorf("Version = %q, want %q", resp.Version, "test-version")
	}
}

func TestProvider_Schema(t *testing.T) {
	t.Parallel()

	p := New("test")()
	resp := &provider.SchemaResponse{}
	p.Schema(t.Context(), provider.SchemaRequest{}, resp)

	if resp.Diagnostics.HasError() {
		t.Fatalf("schema diagnostics: %v", resp.Diagnostics)
	}

	// Pin the §6 attribute names — these are locked after M1 per §13;
	// changing any of them would be a breaking change.
	wantAttrs := []string{"backend", "host", "port", "username", "password", "timeout", "local", "ssh", "winrm"}
	for _, name := range wantAttrs {
		if _, ok := resp.Schema.Attributes[name]; !ok {
			t.Errorf("missing top-level attribute %q", name)
		}
	}
}

func TestProvider_Resources(t *testing.T) {
	t.Parallel()

	p := New("test")()
	got := p.Resources(t.Context())

	// Currently hyperv_virtual_switch -- the first mutating resource (PLAN
	// M1c). Pin the count so accidental wiring of additional resources
	// doesn't slip in unnoticed before their schema is reviewed.
	if len(got) != 1 {
		t.Errorf("got %d resources, want 1 (hyperv_virtual_switch only at this milestone)", len(got))
	}
}

func TestProvider_DataSources(t *testing.T) {
	t.Parallel()

	p := New("test")()
	got := p.DataSources(t.Context())

	// Currently just hyperv_host (PLAN §7).
	if len(got) != 1 {
		t.Fatalf("got %d data sources, want 1 (hyperv_host)", len(got))
	}

	// Construct one to ensure the factory works end-to-end.
	if got[0]() == nil {
		t.Error("data source factory returned nil")
	}
}
