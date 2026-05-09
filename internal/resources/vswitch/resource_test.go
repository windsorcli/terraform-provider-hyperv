package vswitch

import (
	"reflect"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/windsorcli/terraform-provider-hyperv/internal/hyperv"
)

// hasPlanModifier checks if any plan-modifier in `mods` has a type whose
// package-qualified name contains `keyword`. Used by schema tests to assert
// presence of RequiresReplace / UseStateForUnknown without depending on
// the framework's user-facing Description() text, which is localizable
// and prone to wording tweaks.
func hasPlanModifier[M any](mods []M, keyword string) bool {
	for _, pm := range mods {
		if strings.Contains(strings.ToLower(reflect.TypeOf(pm).String()), strings.ToLower(keyword)) {
			return true
		}
	}
	return false
}

// Schema test: every locked-in attribute is present and the plan-modifier
// invariants (RequiresReplace on name + switch_type, UseStateForUnknown on
// id) are wired. Drift here is a contract break for users.
func TestResource_Schema(t *testing.T) {
	t.Parallel()

	r := New()
	resp := &resource.SchemaResponse{}
	r.Schema(t.Context(), resource.SchemaRequest{}, resp)

	if resp.Diagnostics.HasError() {
		t.Fatalf("schema diagnostics: %v", resp.Diagnostics)
	}
	wantAttrs := []string{
		"id",
		"name",
		"switch_type",
		"net_adapter_names",
		"allow_management_os",
		"notes",
		"net_adapter_interface_description",
	}
	for _, name := range wantAttrs {
		if _, ok := resp.Schema.Attributes[name]; !ok {
			t.Errorf("missing attribute %q", name)
		}
	}
}

// RequiresReplace must be on name + switch_type. Hyper-V can't rename a
// switch in place, and switch_type is also immutable -- changing either
// must trigger destroy+recreate, not a Set-VMSwitch attempt.
func TestResource_Schema_RequiresReplaceOnImmutableAttrs(t *testing.T) {
	t.Parallel()

	r := New()
	resp := &resource.SchemaResponse{}
	r.Schema(t.Context(), resource.SchemaRequest{}, resp)

	for _, attrName := range []string{"name", "switch_type"} {
		raw, ok := resp.Schema.Attributes[attrName]
		if !ok {
			t.Fatalf("missing attribute %q", attrName)
		}
		strAttr, ok := raw.(schema.StringAttribute)
		if !ok {
			t.Errorf("%q is not a StringAttribute (got %T)", attrName, raw)
			continue
		}
		if !hasPlanModifier(strAttr.PlanModifiers, "RequiresReplace") {
			t.Errorf("%q must carry the RequiresReplace plan modifier", attrName)
		}
	}
}

// id and the Optional+Computed mutable attributes should all carry
// UseStateForUnknown so refreshes don't show phantom (known after apply)
// diffs when the user hasn't set the value in config.
func TestResource_Schema_UseStateForUnknownOnComputedAttrs(t *testing.T) {
	t.Parallel()

	r := New()
	resp := &resource.SchemaResponse{}
	r.Schema(t.Context(), resource.SchemaRequest{}, resp)

	checkString := func(attrName string) {
		raw, ok := resp.Schema.Attributes[attrName]
		if !ok {
			t.Fatalf("missing attribute %q", attrName)
		}
		strAttr, ok := raw.(schema.StringAttribute)
		if !ok {
			t.Fatalf("%q is not a StringAttribute (got %T)", attrName, raw)
		}
		if !hasPlanModifier(strAttr.PlanModifiers, "UseStateForUnknown") {
			t.Errorf("%q must carry UseStateForUnknown", attrName)
		}
	}
	checkString("id")
	checkString("notes")
	checkString("net_adapter_interface_description")

	// Bool / List variants
	if boolAttr, ok := resp.Schema.Attributes["allow_management_os"].(schema.BoolAttribute); ok {
		if !hasPlanModifier(boolAttr.PlanModifiers, "UseStateForUnknown") {
			t.Error(`"allow_management_os" must carry UseStateForUnknown`)
		}
	} else {
		t.Error(`"allow_management_os" missing or wrong type`)
	}
	if listAttr, ok := resp.Schema.Attributes["net_adapter_names"].(schema.ListAttribute); ok {
		if !hasPlanModifier(listAttr.PlanModifiers, "UseStateForUnknown") {
			t.Error(`"net_adapter_names" must carry UseStateForUnknown`)
		}
	} else {
		t.Error(`"net_adapter_names" missing or wrong type`)
	}
}

// Metadata pins the resource's TF type name. Any change here is a
// user-visible breaking rename.
func TestResource_Metadata(t *testing.T) {
	t.Parallel()

	r := New()
	resp := &resource.MetadataResponse{}
	r.Metadata(t.Context(), resource.MetadataRequest{ProviderTypeName: "hyperv"}, resp)
	if resp.TypeName != "hyperv_virtual_switch" {
		t.Errorf("TypeName = %q, want %q", resp.TypeName, "hyperv_virtual_switch")
	}
}

// Configure with nil ProviderData (validate-time invocation before Configure
// has resolved) must NOT panic and must NOT error. The test repeats the
// pattern from the host datasource since this is the same framework gotcha.
func TestResource_Configure_NilProviderDataIsNoop(t *testing.T) {
	t.Parallel()

	r, ok := New().(*Resource)
	if !ok {
		t.Fatal("New() did not return *Resource")
	}
	resp := &resource.ConfigureResponse{}
	r.Configure(t.Context(), resource.ConfigureRequest{ProviderData: nil}, resp)
	if resp.Diagnostics.HasError() {
		t.Errorf("nil ProviderData should be a no-op; got diags: %v", resp.Diagnostics)
	}
	if r.client != nil {
		t.Error("client should remain nil when ProviderData is nil")
	}
}

// Configure with the wrong ProviderData concrete type must produce a
// diagnostic that names *hyperv.Client so the operator can correct the
// provider wiring without spelunking the framework internals.
func TestResource_Configure_WrongTypeIsClearError(t *testing.T) {
	t.Parallel()

	r, ok := New().(*Resource)
	if !ok {
		t.Fatal("New() did not return *Resource")
	}
	resp := &resource.ConfigureResponse{}
	r.Configure(t.Context(),
		resource.ConfigureRequest{ProviderData: "not a client"},
		resp,
	)
	if !resp.Diagnostics.HasError() {
		t.Fatal("expected an error diagnostic")
	}
	if !strings.Contains(resp.Diagnostics[0].Detail(), "*hyperv.Client") {
		t.Errorf("diag detail should name the expected type; got %q", resp.Diagnostics[0].Detail())
	}
}

// buildNewInput translates a Create plan into the wire-level input. Verify
// the External path forwards NetAdapterNames and the optional pointer fields
// pick up the user's explicit values.
func TestBuildNewInput_ExternalWithAllFields(t *testing.T) {
	t.Parallel()

	names, _ := types.ListValueFrom(t.Context(), types.StringType, []string{"NIC1", "NIC2"})
	plan := Model{
		Name:              types.StringValue("ext0"),
		SwitchType:        types.StringValue("External"),
		NetAdapterNames:   names,
		AllowManagementOS: types.BoolValue(true),
		Notes:             types.StringValue("production"),
	}

	in, diags := buildNewInput(t.Context(), plan)
	if diags.HasError() {
		t.Fatalf("diags: %v", diags)
	}
	if in.Name != "ext0" || in.SwitchType != "External" {
		t.Errorf("required fields wrong: %+v", in)
	}
	if len(in.NetAdapterNames) != 2 || in.NetAdapterNames[0] != "NIC1" || in.NetAdapterNames[1] != "NIC2" {
		t.Errorf("NetAdapterNames = %v, want [NIC1 NIC2]", in.NetAdapterNames)
	}
	if in.AllowManagementOS == nil || *in.AllowManagementOS != true {
		t.Errorf("AllowManagementOS = %v, want pointer to true", in.AllowManagementOS)
	}
	if in.Notes == nil || *in.Notes != "production" {
		t.Errorf("Notes = %v, want pointer to \"production\"", in.Notes)
	}
}

// Optional fields that are null in the plan must become nil pointers in the
// wire-level struct so omitempty drops them from the JSON entirely. The
// Pester contract treats absent and null as equivalent, but absent is the
// canonical wire form.
func TestBuildNewInput_OmitsNullOptionals(t *testing.T) {
	t.Parallel()

	plan := Model{
		Name:              types.StringValue("priv0"),
		SwitchType:        types.StringValue("Private"),
		NetAdapterNames:   types.ListNull(types.StringType),
		AllowManagementOS: types.BoolNull(),
		Notes:             types.StringNull(),
	}

	in, diags := buildNewInput(t.Context(), plan)
	if diags.HasError() {
		t.Fatalf("diags: %v", diags)
	}
	if in.NetAdapterNames != nil {
		t.Errorf("NetAdapterNames should be nil for null plan value; got %v", in.NetAdapterNames)
	}
	if in.AllowManagementOS != nil {
		t.Errorf("AllowManagementOS should be nil for null plan value; got %v", in.AllowManagementOS)
	}
	if in.Notes != nil {
		t.Errorf("Notes should be nil for null plan value; got %v", in.Notes)
	}
}

// buildSetInput sources SwitchType from STATE, not plan -- switch_type is
// RequiresReplace, so any change forces destroy+recreate before reaching
// Update. The script-side guard needs the prior switch type to fire.
func TestBuildSetInput_SourcesSwitchTypeFromState(t *testing.T) {
	t.Parallel()

	state := Model{
		Name:       types.StringValue("ext0"),
		SwitchType: types.StringValue("External"),
	}
	plan := Model{
		Name:              types.StringValue("ext0"),
		SwitchType:        types.StringValue("External"),
		AllowManagementOS: types.BoolValue(true),
	}

	in, diags := buildSetInput(t.Context(), plan, state)
	if diags.HasError() {
		t.Fatalf("diags: %v", diags)
	}
	if in.SwitchType != "External" {
		t.Errorf("SwitchType = %q, want \"External\" (sourced from state)", in.SwitchType)
	}
	if in.AllowManagementOS == nil || *in.AllowManagementOS != true {
		t.Errorf("AllowManagementOS = %v, want pointer to true", in.AllowManagementOS)
	}
}

// For Private switches, allow_management_os must NOT be forwarded even
// when plan carries a (Computed read-back) value. Forwarding it would trip
// set.ps1's Private + AllowManagementOS guard on every Update of a Private
// switch, since the attribute is Optional+Computed and plan inherits the
// prior-state value.
func TestBuildSetInput_OmitsAllowManagementOSForPrivate(t *testing.T) {
	t.Parallel()

	state := Model{
		Name:       types.StringValue("priv0"),
		SwitchType: types.StringValue("Private"),
	}
	plan := Model{
		Name:              types.StringValue("priv0"),
		SwitchType:        types.StringValue("Private"),
		AllowManagementOS: types.BoolValue(false), // Computed read-back
		Notes:             types.StringValue("updated notes"),
	}

	in, diags := buildSetInput(t.Context(), plan, state)
	if diags.HasError() {
		t.Fatalf("diags: %v", diags)
	}
	if in.AllowManagementOS != nil {
		t.Errorf("AllowManagementOS should be omitted for Private; got %v", *in.AllowManagementOS)
	}
	if in.Notes == nil || *in.Notes != "updated notes" {
		t.Error("Notes should still be forwarded for Private switches")
	}
}

// modelFromVMSwitch must take net_adapter_names from the caller, not the
// script's read shape -- the cmdlet's NetAdapterInterfaceDescription is a
// friendly NIC label, not the original adapter-name list the user passed.
// Caller-supplied list is the only source of truth for that attribute.
func TestModelFromVMSwitch_PreservesNetAdapterNames(t *testing.T) {
	t.Parallel()

	sw := &hyperv.VMSwitch{
		Name:                           "ext0",
		SwitchType:                     "External",
		AllowManagementOS:              true,
		NetAdapterInterfaceDescription: "Intel(R) Ethernet I210",
		Notes:                          "x",
		ID:                             "guid-here",
	}
	priorList, _ := types.ListValueFrom(t.Context(), types.StringType, []string{"Ethernet"})

	var diags diag.Diagnostics
	got := modelFromVMSwitch(t.Context(), sw, priorList, types.BoolNull(), &diags)
	if diags.HasError() {
		t.Fatalf("diags: %v", diags)
	}

	// net_adapter_names must round-trip the caller's value verbatim.
	elements := got.NetAdapterNames.Elements()
	if len(elements) != 1 {
		t.Fatalf("NetAdapterNames length = %d, want 1", len(elements))
	}
	str, ok := elements[0].(types.String)
	if !ok {
		t.Fatalf("NetAdapterNames[0] is %T, want types.String", elements[0])
	}
	if v := str.ValueString(); v != "Ethernet" {
		t.Errorf("NetAdapterNames[0] = %q, want %q", v, "Ethernet")
	}

	// And the read-back fields come from the script.
	if got.NetAdapterInterfaceDescription.ValueString() != "Intel(R) Ethernet I210" {
		t.Errorf("interface description not propagated from script return")
	}
	if got.ID.ValueString() != got.Name.ValueString() {
		t.Error("id should mirror name (Hyper-V switch names are unique per host)")
	}
}

// When the caller hasn't yet set net_adapter_names (e.g. import path), the
// function must produce a known-empty list -- not unknown -- so state
// doesn't carry a planning-time placeholder forward.
func TestModelFromVMSwitch_FillsEmptyListForUnknownNames(t *testing.T) {
	t.Parallel()

	sw := &hyperv.VMSwitch{
		Name:       "imported",
		SwitchType: "Private",
		ID:         "guid-here",
	}

	var diags diag.Diagnostics
	got := modelFromVMSwitch(t.Context(), sw, types.ListUnknown(types.StringType), types.BoolNull(), &diags)
	if diags.HasError() {
		t.Fatalf("diags: %v", diags)
	}
	if got.NetAdapterNames.IsUnknown() {
		t.Error("NetAdapterNames should be a known empty list, not unknown")
	}
	if !got.NetAdapterNames.IsNull() && len(got.NetAdapterNames.Elements()) != 0 {
		t.Errorf("NetAdapterNames should be empty; got %d elements", len(got.NetAdapterNames.Elements()))
	}
}

// NAT plan threads nat_name / nat_internal_address_prefix / nat_host_address
// into the wire shape. The script-side NAT branch reads each by snake_case
// key; missing any of the three for a NAT switch is a contract violation
// the Go-side validator already catches at plan time.
func TestBuildNewInput_NATForwardsAllNATFields(t *testing.T) {
	t.Parallel()

	plan := Model{
		Name:                     types.StringValue("windsor-nat"),
		SwitchType:               types.StringValue("NAT"),
		NetAdapterNames:          types.ListNull(types.StringType),
		AllowManagementOS:        types.BoolNull(),
		Notes:                    types.StringNull(),
		NatName:                  types.StringValue("windsor-nat"),
		NatInternalAddressPrefix: types.StringValue("192.168.100.0/24"),
		NatHostAddress:           types.StringValue("192.168.100.1"),
	}

	in, diags := buildNewInput(t.Context(), plan)
	if diags.HasError() {
		t.Fatalf("diags: %v", diags)
	}
	if in.SwitchType != "NAT" {
		t.Errorf("SwitchType = %q, want NAT", in.SwitchType)
	}
	if in.NatName != "windsor-nat" {
		t.Errorf("NatName = %q, want windsor-nat", in.NatName)
	}
	if in.NatInternalAddressPrefix != "192.168.100.0/24" {
		t.Errorf("NatInternalAddressPrefix = %q, want 192.168.100.0/24", in.NatInternalAddressPrefix)
	}
	if in.NatHostAddress != "192.168.100.1" {
		t.Errorf("NatHostAddress = %q, want 192.168.100.1", in.NatHostAddress)
	}
}

// NAT updates carry nat_name from state purely as read-back routing
// context for set.ps1 (so it can synthesize SwitchType=NAT). Every
// NAT-specific input is RequiresReplace at the schema layer:
// nat_internal_address_prefix forces replacement because Set-NetNat does
// not accept -InternalIPInterfaceAddressPrefix on the bench (verified
// against Server 2022 + PS 5.1), so the only in-place mutation that
// reaches Update for a NAT switch is Notes.
func TestBuildSetInput_NATForwardsNameForReadback(t *testing.T) {
	t.Parallel()

	state := Model{
		Name:                     types.StringValue("windsor-nat"),
		SwitchType:               types.StringValue("NAT"),
		NatName:                  types.StringValue("windsor-nat"),
		NatInternalAddressPrefix: types.StringValue("192.168.100.0/24"),
		NatHostAddress:           types.StringValue("192.168.100.1"),
	}
	plan := state
	plan.Notes = types.StringValue("updated notes")

	in, diags := buildSetInput(t.Context(), plan, state)
	if diags.HasError() {
		t.Fatalf("diags: %v", diags)
	}
	if in.NatName != "windsor-nat" {
		t.Errorf("NatName = %q, want windsor-nat (from state)", in.NatName)
	}
	if in.Notes == nil || *in.Notes != "updated notes" {
		t.Errorf("Notes = %v, want pointer to \"updated notes\"", in.Notes)
	}
	if in.AllowManagementOS != nil {
		t.Errorf("AllowManagementOS should be nil for NAT switch; got %v", in.AllowManagementOS)
	}
}

// NAT switches must not forward allow_management_os on Update. Mirrors the
// Private-switch case: the attribute is Optional+Computed, so plan carries
// the prior-state false value even when the user never set it; forwarding
// it would trip the script-side guard on every Update of a NAT switch.
func TestBuildSetInput_OmitsAllowManagementOSForNAT(t *testing.T) {
	t.Parallel()

	state := Model{
		Name:       types.StringValue("windsor-nat"),
		SwitchType: types.StringValue("NAT"),
		NatName:    types.StringValue("windsor-nat"),
	}
	plan := Model{
		Name:              types.StringValue("windsor-nat"),
		SwitchType:        types.StringValue("NAT"),
		NatName:           types.StringValue("windsor-nat"),
		AllowManagementOS: types.BoolValue(false), // Computed read-back
		Notes:             types.StringValue("updated notes"),
	}

	in, diags := buildSetInput(t.Context(), plan, state)
	if diags.HasError() {
		t.Fatalf("diags: %v", diags)
	}
	if in.AllowManagementOS != nil {
		t.Errorf("AllowManagementOS must not forward for NAT switch; got %v", in.AllowManagementOS)
	}
}

// modelFromVMSwitch hydrates NAT fields when the wire shape carries them
// (NAT switches), and leaves them null for non-NAT switches (empty wire
// strings). Locking this round-trip keeps the schema's Optional+Computed
// NAT attributes from drifting between the typed-client and resource
// layers.
func TestModelFromVMSwitch_PopulatesNatFieldsForNATSwitch(t *testing.T) {
	t.Parallel()

	sw := &hyperv.VMSwitch{
		Name:                     "windsor-nat",
		SwitchType:               "NAT",
		ID:                       "guid-here",
		NatName:                  "windsor-nat",
		NatInternalAddressPrefix: "192.168.100.0/24",
		NatHostAddress:           "192.168.100.1",
	}

	var diags diag.Diagnostics
	got := modelFromVMSwitch(t.Context(), sw, types.ListNull(types.StringType), types.BoolNull(), &diags)
	if diags.HasError() {
		t.Fatalf("diags: %v", diags)
	}
	if got.NatName.ValueString() != "windsor-nat" {
		t.Errorf("NatName = %q, want windsor-nat", got.NatName.ValueString())
	}
	if got.NatInternalAddressPrefix.ValueString() != "192.168.100.0/24" {
		t.Errorf("NatInternalAddressPrefix = %q, want 192.168.100.0/24", got.NatInternalAddressPrefix.ValueString())
	}
	if got.NatHostAddress.ValueString() != "192.168.100.1" {
		t.Errorf("NatHostAddress = %q, want 192.168.100.1", got.NatHostAddress.ValueString())
	}
}

func TestModelFromVMSwitch_NullsNatFieldsForNonNAT(t *testing.T) {
	t.Parallel()

	sw := &hyperv.VMSwitch{
		Name:       "ext0",
		SwitchType: "External",
		ID:         "guid-here",
		// NAT fields all empty (the script emits empty strings for
		// non-NAT switches).
	}

	var diags diag.Diagnostics
	got := modelFromVMSwitch(t.Context(), sw, types.ListNull(types.StringType), types.BoolNull(), &diags)
	if diags.HasError() {
		t.Fatalf("diags: %v", diags)
	}
	if !got.NatName.IsNull() {
		t.Errorf("NatName must be null for non-NAT switch; got %q", got.NatName.ValueString())
	}
	if !got.NatInternalAddressPrefix.IsNull() {
		t.Errorf("NatInternalAddressPrefix must be null for non-NAT switch; got %q", got.NatInternalAddressPrefix.ValueString())
	}
	if !got.NatHostAddress.IsNull() {
		t.Errorf("NatHostAddress must be null for non-NAT switch; got %q", got.NatHostAddress.ValueString())
	}
}
