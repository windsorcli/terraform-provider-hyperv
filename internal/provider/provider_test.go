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

	// hyperv_virtual_switch (PLAN M1c) + hyperv_image_file (PLAN M4 first
	// slice: url + host_path source modes) + hyperv_vhd (PLAN M4: fixed/
	// dynamic/differencing). Pin the count so accidental wiring of
	// additional resources doesn't slip in unnoticed before their schema
	// is reviewed.
	if len(got) != 3 {
		t.Errorf("got %d resources, want 3 (hyperv_virtual_switch, hyperv_image_file, hyperv_vhd)", len(got))
	}
}

// Pin the registered data-source count so accidental wiring of additional
// data sources doesn't slip in before their schemas are reviewed.
func TestProvider_DataSources(t *testing.T) {
	t.Parallel()

	p := New("test")()
	got := p.DataSources(t.Context())

	// hyperv_host + hyperv_virtual_switch (PLAN S7).
	if len(got) != 2 {
		t.Fatalf("got %d data sources, want 2 (hyperv_host, hyperv_virtual_switch)", len(got))
	}

	for i, factory := range got {
		if factory() == nil {
			t.Errorf("data source factory %d returned nil", i)
		}
	}
}
