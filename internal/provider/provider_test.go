package provider

import (
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/provider"
	"github.com/hashicorp/terraform-plugin-framework/types"
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
	// slice: url + host_path source modes) + hyperv_iso_volume (PLAN M4:
	// runner-side iso9660 synthesis) + hyperv_vhd (PLAN M4: fixed/
	// dynamic/differencing) + hyperv_vm (PLAN M4 minimal: name/generation/
	// vcpu/memory_bytes/secure_boot/notes). Pin the count so accidental
	// wiring of additional resources doesn't slip in unnoticed before
	// their schema is reviewed.
	if len(got) != 5 {
		t.Errorf("got %d resources, want 5 (hyperv_virtual_switch, hyperv_image_file, hyperv_iso_volume, hyperv_vhd, hyperv_vm)", len(got))
	}
}

// Pin the registered data-source count so accidental wiring of additional
// data sources doesn't slip in before their schemas are reviewed.
func TestProvider_DataSources(t *testing.T) {
	t.Parallel()

	p := New("test")()
	got := p.DataSources(t.Context())

	// hyperv_host + hyperv_vm_state + hyperv_virtual_switch.
	if len(got) != 3 {
		t.Fatalf("got %d data sources, want 3 (hyperv_host, hyperv_vm_state, hyperv_virtual_switch)", len(got))
	}

	for i, factory := range got {
		if factory() == nil {
			t.Errorf("data source factory %d returned nil", i)
		}
	}
}

// TestProvider_ConfigValidators_RegistersAll confirms every provider-level
// validator is wired into ConfigValidators(). Mirrors the resource-level
// "RegistersAll" tests under internal/resources/*; drift here means a
// validator silently dropped from the cross-attribute check surface.
func TestProvider_ConfigValidators_RegistersAll(t *testing.T) {
	t.Parallel()

	p, ok := New("test")().(*HypervProvider)
	if !ok {
		t.Fatal("New() did not return *HypervProvider")
	}
	got := p.ConfigValidators(t.Context())
	if len(got) != 1 {
		t.Fatalf("got %d ConfigValidators, want 1 (kerberosRealmRequiredValidator)", len(got))
	}
}

// TestKerberosRealmRequiredValidator covers the framework-level realm-
// required rule. The schema layer can only mark the attribute Optional
// (Terraform doesn't model conditional requirements at schema), so the
// validator is the seam where "auth=kerberos but no realm" surfaces at
// `terraform validate` instead of waiting until plan/apply.
func TestKerberosRealmRequiredValidator(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name      string
		model     HypervProviderModel
		wantError bool
	}{
		{
			name: "no winrm block -> ok (other backends or default ntlm)",
			model: HypervProviderModel{
				Backend: types.StringValue("local"),
			},
		},
		{
			name: "winrm but auth omitted -> ok (defaults to ntlm)",
			model: HypervProviderModel{
				Backend: types.StringValue("winrm"),
				WinRM:   &WinRMConfig{},
			},
		},
		{
			name: "winrm auth=ntlm -> ok",
			model: HypervProviderModel{
				Backend: types.StringValue("winrm"),
				WinRM:   &WinRMConfig{Auth: types.StringValue("ntlm")},
			},
		},
		{
			name: "winrm auth=kerberos with realm -> ok",
			model: HypervProviderModel{
				Backend: types.StringValue("winrm"),
				WinRM: &WinRMConfig{
					Auth: types.StringValue("kerberos"),
					Kerberos: &WinRMKerberosConfig{
						Realm: types.StringValue("HV.LAB"),
					},
				},
			},
		},
		{
			name: "winrm auth=kerberos with empty realm -> fires",
			model: HypervProviderModel{
				Backend: types.StringValue("winrm"),
				WinRM: &WinRMConfig{
					Auth: types.StringValue("kerberos"),
					Kerberos: &WinRMKerberosConfig{
						Realm: types.StringValue(""),
					},
				},
			},
			wantError: true,
		},
		{
			name: "winrm auth=kerberos with no kerberos block -> fires",
			model: HypervProviderModel{
				Backend: types.StringValue("winrm"),
				WinRM: &WinRMConfig{
					Auth: types.StringValue("kerberos"),
					// Kerberos block omitted entirely.
				},
			},
			wantError: true,
		},
		{
			name: "winrm auth=kerberos with null realm -> fires",
			model: HypervProviderModel{
				Backend: types.StringValue("winrm"),
				WinRM: &WinRMConfig{
					Auth: types.StringValue("kerberos"),
					Kerberos: &WinRMKerberosConfig{
						Realm: types.StringNull(),
					},
				},
			},
			wantError: true,
		},
		{
			name: "auth=kerberos but realm unknown -> skip (deferred dep)",
			model: HypervProviderModel{
				Backend: types.StringValue("winrm"),
				WinRM: &WinRMConfig{
					Auth: types.StringValue("kerberos"),
					Kerberos: &WinRMKerberosConfig{
						Realm: types.StringUnknown(),
					},
				},
			},
		},
		{
			name: "auth unknown -> skip (deferred dep)",
			model: HypervProviderModel{
				Backend: types.StringValue("winrm"),
				WinRM: &WinRMConfig{
					Auth: types.StringUnknown(),
				},
			},
		},
	}
	v := kerberosRealmRequiredValidator{}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			diags := v.validate(tc.model)
			if tc.wantError && !diags.HasError() {
				t.Errorf("expected error, got none")
			}
			if !tc.wantError && diags.HasError() {
				t.Errorf("expected no error, got %v", diags)
			}
		})
	}
}
