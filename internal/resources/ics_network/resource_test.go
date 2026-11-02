package ics_network

import (
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-go/tftypes"

	"github.com/windsorcli/terraform-provider-hyperv/internal/hyperv"
)

func TestResource_Metadata(t *testing.T) {
	t.Parallel()

	resp := &resource.MetadataResponse{}
	New().Metadata(t.Context(), resource.MetadataRequest{ProviderTypeName: "hyperv"}, resp)
	if resp.TypeName != "hyperv_ics_network" {
		t.Errorf("TypeName = %q, want hyperv_ics_network", resp.TypeName)
	}
}

func TestResource_Schema(t *testing.T) {
	t.Parallel()

	resp := &resource.SchemaResponse{}
	New().Schema(t.Context(), resource.SchemaRequest{}, resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("schema diagnostics: %v", resp.Diagnostics)
	}
	if diags := resp.Schema.ValidateImplementation(t.Context()); diags.HasError() {
		t.Fatalf("ValidateImplementation: %v", diags)
	}
	for _, name := range []string{"id", "name", "address_prefix", "switch_name", "isolation_id", "host_address"} {
		if _, ok := resp.Schema.Attributes[name]; !ok {
			t.Errorf("missing attribute %q", name)
		}
	}
}

// Both inputs must force replacement, and every computed attribute must keep its state value across plans.
func TestResource_Schema_Mutability(t *testing.T) {
	t.Parallel()

	s := resourceSchema()
	for _, name := range []string{"name", "address_prefix"} {
		a, ok := s.Attributes[name].(schema.StringAttribute)
		if !ok || !a.Required || len(a.PlanModifiers) != 1 {
			t.Errorf("%s: want a required string with one RequiresReplace modifier, got %+v", name, s.Attributes[name])
		}
	}
	for _, name := range []string{"id", "switch_name", "isolation_id", "host_address"} {
		attr := s.Attributes[name]
		if !attr.IsComputed() || attr.IsOptional() || attr.IsRequired() {
			t.Errorf("%s: want computed only", name)
		}
	}
}

func TestIPv4PrefixValidator(t *testing.T) {
	t.Parallel()

	cases := []struct {
		value   string
		wantErr bool
	}{
		{"172.30.97.0/24", false},
		{"10.0.0.0/8", false},
		{"192.168.50.0/30", false},
		{"172.30.97.1/24", true},
		{"192.168.50.0/31", true},
		{"172.30.97.0", true},
		{"fd00::/64", true},
		{"not-a-prefix", true},
	}
	for _, tc := range cases {
		t.Run(tc.value, func(t *testing.T) {
			t.Parallel()
			resp := &validator.StringResponse{}
			ipv4PrefixValidator{}.ValidateString(t.Context(), validator.StringRequest{
				Path:        path.Root("address_prefix"),
				ConfigValue: types.StringValue(tc.value),
			}, resp)
			if got := resp.Diagnostics.HasError(); got != tc.wantErr {
				t.Errorf("error = %v, want %v (%v)", got, tc.wantErr, resp.Diagnostics)
			}
		})
	}
}

func TestIPv4PrefixValidator_SkipsUnknown(t *testing.T) {
	t.Parallel()

	resp := &validator.StringResponse{}
	ipv4PrefixValidator{}.ValidateString(t.Context(), validator.StringRequest{ConfigValue: types.StringUnknown()}, resp)
	if resp.Diagnostics.HasError() {
		t.Errorf("unknown value produced %v", resp.Diagnostics)
	}
}

func TestBuildNewInput_DerivesFirstHost(t *testing.T) {
	t.Parallel()

	in, err := buildNewInput(Model{
		Name:          types.StringValue("lab"),
		AddressPrefix: types.StringValue("172.30.97.0/24"),
	})
	if err != nil {
		t.Fatalf("buildNewInput: %v", err)
	}
	want := hyperv.NewICSNetworkInput{Name: "lab", AddressPrefix: "172.30.97.0/24", HostAddress: "172.30.97.1"}
	if in != want {
		t.Errorf("got %+v, want %+v", in, want)
	}
}

func TestModelFromICSNetwork(t *testing.T) {
	t.Parallel()

	m := modelFromICSNetwork(&hyperv.ICSNetwork{
		ID:            "753900d5-83fd-4c4d-83b6-7c438c6ff4fe",
		Name:          "tfdev-ics",
		AddressPrefix: "172.30.97.0/24",
		HostAddress:   "172.30.97.1",
		IsolationID:   2,
		SwitchName:    "first-ics",
	})
	if m.IsolationID.ValueInt64() != 2 || m.SwitchName.ValueString() != "first-ics" || m.HostAddress.ValueString() != "172.30.97.1" {
		t.Errorf("got %+v", m)
	}
}

func TestImportState(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name    string
		id      string
		wantErr bool
	}{
		{name: "guid", id: "753900d5-83fd-4c4d-83b6-7c438c6ff4fe"},
		{name: "upper-case guid", id: "753900D5-83FD-4C4D-83B6-7C438C6FF4FE"},
		{name: "network name", id: "tfdev-ics", wantErr: true},
		{name: "braced guid", id: "{753900d5-83fd-4c4d-83b6-7c438c6ff4fe}", wantErr: true},
	}
	s := resourceSchema()
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			resp := &resource.ImportStateResponse{
				State: tfsdk.State{Schema: s, Raw: tftypes.NewValue(s.Type().TerraformType(t.Context()), nil)},
			}
			(&Resource{}).ImportState(t.Context(), resource.ImportStateRequest{ID: tc.id}, resp)
			if got := resp.Diagnostics.HasError(); got != tc.wantErr {
				t.Fatalf("error = %v, want %v (%v)", got, tc.wantErr, resp.Diagnostics)
			}
			if tc.wantErr {
				return
			}
			var id types.String
			resp.Diagnostics.Append(resp.State.GetAttribute(t.Context(), path.Root("id"), &id)...)
			if id.ValueString() != tc.id {
				t.Errorf("id = %q, want %q", id.ValueString(), tc.id)
			}
		})
	}
}
