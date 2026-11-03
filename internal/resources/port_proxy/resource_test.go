package port_proxy

import (
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-go/tftypes"

	"github.com/windsorcli/terraform-provider-hyperv/internal/hyperv"
	"github.com/windsorcli/terraform-provider-hyperv/internal/resources/internal/firewallrule"
)

func TestResource_Metadata(t *testing.T) {
	t.Parallel()

	resp := &resource.MetadataResponse{}
	New().Metadata(t.Context(), resource.MetadataRequest{ProviderTypeName: "hyperv"}, resp)
	if resp.TypeName != "hyperv_port_proxy" {
		t.Errorf("TypeName = %q, want hyperv_port_proxy", resp.TypeName)
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
	for _, name := range []string{"id", "listen_address", "listen_port", "connect_address", "connect_port", "firewall_rule", "listening"} {
		if _, ok := resp.Schema.Attributes[name]; !ok {
			t.Errorf("missing attribute %q", name)
		}
	}
}

// The listen pair identifies the rule and must force replacement; the connect pair updates in place.
func TestResource_Schema_Mutability(t *testing.T) {
	t.Parallel()

	s := resourceSchema()
	cases := []struct {
		name        string
		wantReplace bool
	}{
		{"listen_address", true},
		{"listen_port", true},
		{"connect_address", false},
		{"connect_port", false},
	}
	for _, tc := range cases {
		var mods int
		switch a := s.Attributes[tc.name].(type) {
		case schema.StringAttribute:
			mods = len(a.PlanModifiers)
		case schema.Int64Attribute:
			mods = len(a.PlanModifiers)
		default:
			t.Fatalf("%s: unexpected attribute type %T", tc.name, a)
		}
		if got := mods > 0; got != tc.wantReplace {
			t.Errorf("%s: has plan modifiers = %v, want %v", tc.name, got, tc.wantReplace)
		}
	}
}

// A rule netsh holds with no listener must plan an in-place update, so the apply re-adds it.
func TestPlanListening(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name  string
		state types.Bool
	}{
		{"no listener", types.BoolValue(false)},
		{"listening", types.BoolValue(true)},
		{"create", types.BoolNull()},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			resp := &planmodifier.BoolResponse{PlanValue: types.BoolUnknown()}
			planListening{}.PlanModifyBool(t.Context(), planmodifier.BoolRequest{
				StateValue: tc.state,
				PlanValue:  types.BoolUnknown(),
			}, resp)
			if !resp.PlanValue.Equal(types.BoolValue(true)) {
				t.Errorf("PlanValue = %s, want true", resp.PlanValue)
			}
		})
	}
}

func TestResource_Schema_ListeningIsComputedOnly(t *testing.T) {
	t.Parallel()

	a, ok := resourceSchema().Attributes["listening"].(schema.BoolAttribute)
	if !ok {
		t.Fatalf("listening: want a BoolAttribute")
	}
	if !a.Computed || a.Optional || a.Required {
		t.Errorf("listening: Computed=%v Optional=%v Required=%v, want computed only", a.Computed, a.Optional, a.Required)
	}
	if len(a.PlanModifiers) != 1 {
		t.Errorf("listening: %d plan modifiers, want planListening only", len(a.PlanModifiers))
	}
}

func TestDerivedFirewallRuleName(t *testing.T) {
	t.Parallel()

	if got := derivedFirewallRuleName("0.0.0.0", 15000); got != "hyperv-pp-0.0.0.0-15000" {
		t.Errorf("got %q", got)
	}
	if got := derivedFirewallRuleName("192.168.3.77", 15000); got != "hyperv-pp-192.168.3.77-15000" {
		t.Errorf("got %q", got)
	}
}

func newModel(t *testing.T, fw *firewallrule.Model) Model {
	t.Helper()
	obj := types.ObjectNull(firewallrule.AttrTypes())
	if fw != nil {
		o, d := types.ObjectValueFrom(t.Context(), firewallrule.AttrTypes(), *fw)
		if d.HasError() {
			t.Fatalf("ObjectValueFrom: %v", d)
		}
		obj = o
	}
	return Model{
		ID:             types.StringValue("0.0.0.0:15000"),
		ListenAddress:  types.StringValue("0.0.0.0"),
		ListenPort:     types.Int64Value(15000),
		ConnectAddress: types.StringValue("172.30.98.222"),
		ConnectPort:    types.Int64Value(50000),
		FirewallRule:   obj,
	}
}

func TestBuildNewInput_DefaultsWhenFirewallRuleNull(t *testing.T) {
	t.Parallel()

	in, diags := buildNewInput(t.Context(), newModel(t, nil))
	if diags.HasError() {
		t.Fatalf("diags: %v", diags)
	}
	want := hyperv.NewPortProxyInput{
		ListenAddress:  "0.0.0.0",
		ListenPort:     15000,
		ConnectAddress: "172.30.98.222",
		ConnectPort:    50000,
		Firewall:       hyperv.PortProxyFirewallInput{Enabled: true, Name: "hyperv-pp-0.0.0.0-15000", Profile: "Any"},
	}
	if in != want {
		t.Errorf("got %+v, want %+v", in, want)
	}
}

func TestBuildNewInput_PassesUserFirewallFields(t *testing.T) {
	t.Parallel()

	in, diags := buildNewInput(t.Context(), newModel(t, &firewallrule.Model{
		Enabled: types.BoolValue(false),
		Name:    types.StringValue("talos-api"),
		Profile: types.StringValue("Private"),
	}))
	if diags.HasError() {
		t.Fatalf("diags: %v", diags)
	}
	want := hyperv.PortProxyFirewallInput{Enabled: false, Name: "talos-api", Profile: "Private"}
	if in.Firewall != want {
		t.Errorf("Firewall = %+v, want %+v", in.Firewall, want)
	}
}

func TestBuildSetInput_ListenPairFromStateConnectPairFromPlan(t *testing.T) {
	t.Parallel()

	state := newModel(t, &firewallrule.Model{
		Enabled: types.BoolValue(true),
		Name:    types.StringValue("talos-api"),
		Profile: types.StringValue("Any"),
	})
	plan := newModel(t, &firewallrule.Model{
		Enabled: types.BoolValue(true),
		Name:    types.StringUnknown(),
		Profile: types.StringValue("Private"),
	})
	plan.ConnectAddress = types.StringValue("172.30.98.177")

	in, diags := buildSetInput(t.Context(), plan, state)
	if diags.HasError() {
		t.Fatalf("diags: %v", diags)
	}
	if in.ConnectAddress != "172.30.98.177" {
		t.Errorf("ConnectAddress = %q, want the plan value", in.ConnectAddress)
	}
	if in.ListenAddress != "0.0.0.0" || in.ListenPort != 15000 {
		t.Errorf("listen pair = %s:%d, want 0.0.0.0:15000", in.ListenAddress, in.ListenPort)
	}
	if in.Firewall.Name != "talos-api" || in.Firewall.Profile != "Private" {
		t.Errorf("Firewall = %+v, want name from state and profile from plan", in.Firewall)
	}
}

func TestModelFromPortProxy_CoalescesEmptyProfile(t *testing.T) {
	t.Parallel()

	m, diags := modelFromPortProxy(t.Context(), &hyperv.PortProxy{
		ID:               "0.0.0.0:15000",
		ListenAddress:    "0.0.0.0",
		ListenPort:       15000,
		ConnectAddress:   "172.30.98.222",
		ConnectPort:      50000,
		FirewallRuleName: "hyperv-pp-0.0.0.0-15000",
	})
	if diags.HasError() {
		t.Fatalf("diags: %v", diags)
	}
	fw, d := firewallrule.Unpack(t.Context(), m.FirewallRule)
	if d.HasError() {
		t.Fatalf("unpack: %v", d)
	}
	if fw.Enabled.ValueBool() {
		t.Error("Enabled = true, want false for an absent rule")
	}
	if fw.Profile.ValueString() != "Any" {
		t.Errorf("Profile = %q, want Any", fw.Profile.ValueString())
	}
	if m.ConnectPort.ValueInt64() != 50000 {
		t.Errorf("ConnectPort = %d", m.ConnectPort.ValueInt64())
	}
}

func TestModelFromPortProxy_Listening(t *testing.T) {
	t.Parallel()

	for _, want := range []bool{true, false} {
		m, diags := modelFromPortProxy(t.Context(), &hyperv.PortProxy{
			ID:               "192.168.3.77:6443",
			ListenAddress:    "192.168.3.77",
			ListenPort:       6443,
			ConnectAddress:   "10.77.0.206",
			ConnectPort:      6443,
			FirewallRuleName: "hyperv-pp-192.168.3.77-6443",
			Listening:        want,
		})
		if diags.HasError() {
			t.Fatalf("diags: %v", diags)
		}
		if m.Listening.ValueBool() != want {
			t.Errorf("Listening = %v, want %v", m.Listening.ValueBool(), want)
		}
	}
}

func TestImportState(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name     string
		id       string
		wantErr  bool
		wantPort int64
		wantFw   string
	}{
		{name: "two-part", id: "0.0.0.0:15000", wantPort: 15000, wantFw: "hyperv-pp-0.0.0.0-15000"},
		{name: "three-part", id: "0.0.0.0:15000:talos-api", wantPort: 15000, wantFw: "talos-api"},
		{name: "missing port", id: "0.0.0.0", wantErr: true},
		{name: "port not a number", id: "0.0.0.0:http", wantErr: true},
		{name: "port out of range", id: "0.0.0.0:70000", wantErr: true},
	}
	s := resourceSchema()
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			resp := &resource.ImportStateResponse{
				State: tfsdk.State{Schema: s, Raw: tftypes.NewValue(s.Type().TerraformType(t.Context()), nil)},
			}
			(&Resource{}).ImportState(t.Context(), resource.ImportStateRequest{ID: tc.id}, resp)
			if tc.wantErr {
				if !resp.Diagnostics.HasError() {
					t.Fatal("want an error diagnostic")
				}
				return
			}
			if resp.Diagnostics.HasError() {
				t.Fatalf("diags: %v", resp.Diagnostics)
			}
			var id types.String
			var port types.Int64
			var fwName types.String
			resp.Diagnostics.Append(resp.State.GetAttribute(t.Context(), path.Root("id"), &id)...)
			resp.Diagnostics.Append(resp.State.GetAttribute(t.Context(), path.Root("listen_port"), &port)...)
			resp.Diagnostics.Append(resp.State.GetAttribute(t.Context(), path.Root("firewall_rule").AtName("name"), &fwName)...)
			if resp.Diagnostics.HasError() {
				t.Fatalf("read back: %v", resp.Diagnostics)
			}
			if id.ValueString() != "0.0.0.0:15000" {
				t.Errorf("id = %q, want the two-part form", id.ValueString())
			}
			if port.ValueInt64() != tc.wantPort {
				t.Errorf("listen_port = %d, want %d", port.ValueInt64(), tc.wantPort)
			}
			if fwName.ValueString() != tc.wantFw {
				t.Errorf("firewall_rule.name = %q, want %q", fwName.ValueString(), tc.wantFw)
			}
		})
	}
}
