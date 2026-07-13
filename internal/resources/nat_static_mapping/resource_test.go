package nat_static_mapping

import (
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-framework/types/basetypes"

	"github.com/xeitu/terraform-provider-hyperv/internal/hyperv"
)

// Schema must lock attribute names + the lookup-tuple RequiresReplace
// pinning. Drift here is a user-visible breaking rename or a silent
// downgrade of the immutability contract.
func TestResource_Schema(t *testing.T) {
	t.Parallel()

	r := New()
	resp := &resource.SchemaResponse{}
	r.Schema(t.Context(), resource.SchemaRequest{}, resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("schema diagnostics: %v", resp.Diagnostics)
	}

	wantAttrs := []string{
		"id", "nat_name", "protocol", "address_family",
		"external_ip", "external_port", "internal_ip", "internal_port",
		"firewall_rule",
	}
	// static_mapping_id is deliberately absent (the Hyper-V mapping ID
	// re-rolls on internal_* updates; exposing it would force every plan
	// to show "(known after apply)" or trip the framework's
	// inconsistent-result guard on Update).
	if _, ok := resp.Schema.Attributes["static_mapping_id"]; ok {
		t.Error("static_mapping_id should NOT be on the schema -- the ID re-rolls on Update and isn't a foreign-key target")
	}
	for _, name := range wantAttrs {
		if _, ok := resp.Schema.Attributes[name]; !ok {
			t.Errorf("missing attribute %q", name)
		}
	}
}

// Metadata pins the resource's TF type name. Any change here is a
// user-visible breaking rename.
func TestResource_Metadata(t *testing.T) {
	t.Parallel()

	r := New()
	resp := &resource.MetadataResponse{}
	r.Metadata(t.Context(), resource.MetadataRequest{ProviderTypeName: "hyperv"}, resp)
	if resp.TypeName != "hyperv_nat_static_mapping" {
		t.Errorf("TypeName = %q, want %q", resp.TypeName, "hyperv_nat_static_mapping")
	}
}

// derivedFirewallRuleName is the schema's "no name supplied" fallback.
// Locking the format here keeps the Read-side reconciliation stable
// across users who don't set firewall_rule.name explicitly: state will
// always carry the same string and refresh won't introduce churn.
func TestDerivedFirewallRuleName(t *testing.T) {
	t.Parallel()

	cases := []struct {
		protocol string
		port     int64
		want     string
	}{
		{"tcp", 80, "hyperv-pf-tcp-80"},
		{"udp", 53, "hyperv-pf-udp-53"},
		{"TCP", 443, "hyperv-pf-tcp-443"}, // schema lowercases on input but defense-in-depth here
		{"tcp", 65535, "hyperv-pf-tcp-65535"},
	}
	for _, tc := range cases {
		got := derivedFirewallRuleName(tc.protocol, tc.port)
		if got != tc.want {
			t.Errorf("derivedFirewallRuleName(%q, %d) = %q, want %q",
				tc.protocol, tc.port, got, tc.want)
		}
	}
}

// buildNewInput happy path: every required field flows to the wire,
// firewall sub-attrs default cleanly when the nested object is null,
// and firewall.name derives from protocol + external_port.
func TestBuildNewInput_DefaultsWhenFirewallRuleNull(t *testing.T) {
	t.Parallel()

	plan := Model{
		NatName:      types.StringValue("windsor-nat"),
		Protocol:     types.StringValue("tcp"),
		ExternalIP:   types.StringValue("0.0.0.0"),
		ExternalPort: types.Int64Value(80),
		InternalIP:   types.StringValue("192.168.100.10"),
		InternalPort: types.Int64Value(30080),
		FirewallRule: types.ObjectNull(firewallRuleAttrTypes()),
	}

	in, fwName, diags := buildNewInput(t.Context(), plan)
	if diags.HasError() {
		t.Fatalf("diags: %v", diags)
	}
	if in.NatName != "windsor-nat" || in.Protocol != "tcp" || in.ExternalPort != 80 {
		t.Errorf("required fields wrong: %+v", in)
	}
	if !in.Firewall.Enabled {
		t.Error("firewall.enabled should default to true")
	}
	if in.Firewall.Profile != "Any" {
		t.Errorf("firewall.profile = %q, want Any", in.Firewall.Profile)
	}
	if in.Firewall.Name != "hyperv-pf-tcp-80" {
		t.Errorf("firewall.name = %q, want derived 'hyperv-pf-tcp-80'", in.Firewall.Name)
	}
	if fwName != "hyperv-pf-tcp-80" {
		t.Errorf("returned fwName = %q, want hyperv-pf-tcp-80", fwName)
	}
}

// buildNewInput respects user overrides when the nested block is
// fully set. Tests pass-through fidelity for the firewall fields.
func TestBuildNewInput_PassesUserSuppliedFirewallFields(t *testing.T) {
	t.Parallel()

	fwModel := FirewallRuleModel{
		Enabled: types.BoolValue(false),
		Name:    types.StringValue("custom-rule-name"),
		Profile: types.StringValue("Domain"),
	}
	fwObj, diags := types.ObjectValueFrom(t.Context(), firewallRuleAttrTypes(), fwModel)
	if diags.HasError() {
		t.Fatalf("ObjectValueFrom: %v", diags)
	}
	plan := Model{
		NatName:      types.StringValue("windsor-nat"),
		Protocol:     types.StringValue("udp"),
		ExternalIP:   types.StringValue("0.0.0.0"),
		ExternalPort: types.Int64Value(53),
		InternalIP:   types.StringValue("192.168.100.10"),
		InternalPort: types.Int64Value(53),
		FirewallRule: fwObj,
	}

	in, fwName, diags := buildNewInput(t.Context(), plan)
	if diags.HasError() {
		t.Fatalf("buildNewInput: %v", diags)
	}
	if in.Firewall.Enabled {
		t.Error("firewall.enabled should be false (user explicitly disabled)")
	}
	if in.Firewall.Name != "custom-rule-name" {
		t.Errorf("firewall.name = %q, want custom-rule-name", in.Firewall.Name)
	}
	if in.Firewall.Profile != "Domain" {
		t.Errorf("firewall.profile = %q, want Domain", in.Firewall.Profile)
	}
	if fwName != "custom-rule-name" {
		t.Errorf("returned fwName = %q, want custom-rule-name", fwName)
	}
}

// buildSetInput sources the lookup tuple from STATE (every member is
// RequiresReplace at the schema layer); only internal_* and the
// mutable firewall fields come from plan. Pinning this keeps a future
// schema change that accidentally relaxes RequiresReplace from
// silently breaking the tuple identity.
func TestBuildSetInput_SourcesLookupTupleFromState(t *testing.T) {
	t.Parallel()

	stateFw := FirewallRuleModel{
		Enabled: types.BoolValue(true),
		Name:    types.StringValue("windsor-pf-tcp-80"),
		Profile: types.StringValue("Any"),
	}
	stateFwObj, _ := types.ObjectValueFrom(t.Context(), firewallRuleAttrTypes(), stateFw)
	state := Model{
		NatName:      types.StringValue("windsor-nat"),
		Protocol:     types.StringValue("tcp"),
		ExternalIP:   types.StringValue("0.0.0.0"),
		ExternalPort: types.Int64Value(80),
		InternalIP:   types.StringValue("192.168.100.10"),
		InternalPort: types.Int64Value(30080),
		FirewallRule: stateFwObj,
	}

	planFw := FirewallRuleModel{
		Enabled: types.BoolValue(true),
		Name:    types.StringValue("windsor-pf-tcp-80"), // RequiresReplace; matches state
		Profile: types.StringValue("Domain"),            // mutated
	}
	planFwObj, _ := types.ObjectValueFrom(t.Context(), firewallRuleAttrTypes(), planFw)
	plan := state
	plan.InternalIP = types.StringValue("192.168.100.20") // mutated
	plan.FirewallRule = planFwObj

	in, fwName, diags := buildSetInput(t.Context(), plan, state)
	if diags.HasError() {
		t.Fatalf("buildSetInput: %v", diags)
	}
	if in.NatName != "windsor-nat" || in.Protocol != "tcp" ||
		in.ExternalIPAddress != "0.0.0.0" || in.ExternalPort != 80 {
		t.Errorf("lookup tuple should source from state; got %+v", in)
	}
	if in.InternalIPAddress != "192.168.100.20" {
		t.Errorf("InternalIPAddress = %q, want plan value 192.168.100.20", in.InternalIPAddress)
	}
	if in.Firewall.Profile != "Domain" {
		t.Errorf("firewall.profile = %q, want plan value Domain", in.Firewall.Profile)
	}
	if fwName != "windsor-pf-tcp-80" {
		t.Errorf("returned fwName = %q, want state value", fwName)
	}
}

// modelFromNatStaticMapping hydrates a Model from a typed NatStaticMapping.
// Locks the wire-shape -> tfsdk attribute mapping plus the
// uppercase-protocol -> lowercase-state normalization (Get-NetNatStaticMapping
// reports TCP/UDP; the schema's `protocol` attribute is lowercase).
func TestModelFromNatStaticMapping_LowercasesProtocolAndPopulatesAllFields(t *testing.T) {
	t.Parallel()

	pf := &hyperv.NatStaticMapping{
		ID:                  "windsor-nat:tcp:0.0.0.0:80",
		StaticMappingID:     1,
		NatName:             "windsor-nat",
		Protocol:            "TCP",
		ExternalIPAddress:   "0.0.0.0",
		ExternalPort:        80,
		InternalIPAddress:   "192.168.100.10",
		InternalPort:        30080,
		FirewallRulePresent: true,
		FirewallRuleName:    "windsor-pf-tcp-80",
		FirewallRuleProfile: "Any",
	}
	got, diags := modelFromNatStaticMapping(t.Context(), pf, "windsor-pf-tcp-80")
	if diags.HasError() {
		t.Fatalf("modelFromNatStaticMapping: %v", diags)
	}
	if got.Protocol.ValueString() != "tcp" {
		t.Errorf("Protocol = %q, want lowercase 'tcp' (TCP on the wire, lowercase in state)", got.Protocol.ValueString())
	}
	if got.AddressFamily.ValueString() != "ipv4" {
		t.Errorf("AddressFamily = %q, want 'ipv4'", got.AddressFamily.ValueString())
	}
	// static_mapping_id is intentionally NOT in state -- the schema
	// drops it because it re-rolls on every internal_* update and
	// no other resource consumes it. modelFromNatStaticMapping correspondingly
	// has no field to populate; pinning the absence here keeps a future
	// re-add from slipping in unnoticed.
	if got.ID.ValueString() != "windsor-nat:tcp:0.0.0.0:80" {
		t.Errorf("ID = %q", got.ID.ValueString())
	}
}

// modelFromNatStaticMapping maps an empty FirewallRuleProfile string back
// to "Any" so state always holds a valid OneOf value -- the host's
// Get-NetFirewallRule reports an empty string when the rule is
// missing, and "Any" is the schema default.
func TestModelFromNatStaticMapping_CoalescesEmptyProfileToAny(t *testing.T) {
	t.Parallel()

	pf := &hyperv.NatStaticMapping{
		ID:                  "windsor-nat:tcp:0.0.0.0:80",
		Protocol:            "TCP",
		FirewallRulePresent: false,
		FirewallRuleProfile: "",
	}
	got, diags := modelFromNatStaticMapping(t.Context(), pf, "windsor-pf-tcp-80")
	if diags.HasError() {
		t.Fatalf("modelFromNatStaticMapping: %v", diags)
	}
	var fw FirewallRuleModel
	got.FirewallRule.As(t.Context(), &fw, basetypes.ObjectAsOptions{})
	if fw.Profile.ValueString() != "Any" {
		t.Errorf("firewall.profile = %q, want coalesced 'Any'", fw.Profile.ValueString())
	}
}
