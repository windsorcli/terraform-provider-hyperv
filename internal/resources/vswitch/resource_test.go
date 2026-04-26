package vswitch

import (
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/windsorcli/terraform-provider-hyperv/internal/hyperv"
)

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
//
// Plan-modifier instances don't have stable identity (each call to
// stringplanmodifier.RequiresReplace() returns a new pointer), so we check
// via the modifier's Description text instead.
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
		var found bool
		for _, pm := range strAttr.PlanModifiers {
			if strings.Contains(pm.Description(t.Context()), "destroy and recreate") {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("%q must carry the RequiresReplace plan modifier", attrName)
		}
	}
}

// id should carry UseStateForUnknown so refreshes don't cause a phantom
// `(known after apply)` flag and trigger noisy plan output.
func TestResource_Schema_IdUsesStateForUnknown(t *testing.T) {
	t.Parallel()

	r := New()
	resp := &resource.SchemaResponse{}
	r.Schema(t.Context(), resource.SchemaRequest{}, resp)

	raw, ok := resp.Schema.Attributes["id"]
	if !ok {
		t.Fatal("missing id attribute")
	}
	strAttr, ok := raw.(schema.StringAttribute)
	if !ok {
		t.Fatalf("id is not a StringAttribute (got %T)", raw)
	}
	var found bool
	for _, pm := range strAttr.PlanModifiers {
		if strings.Contains(pm.Description(t.Context()), "Once set") {
			found = true
			break
		}
	}
	if !found {
		t.Error("id must carry UseStateForUnknown to avoid (known after apply) churn on refresh")
	}
}

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
		Name:       types.StringValue("priv0"),
		SwitchType: types.StringValue("Private"),
	}
	plan := Model{
		Name:              types.StringValue("priv0"),
		SwitchType:        types.StringValue("Private"),
		AllowManagementOS: types.BoolValue(true),
	}

	in, diags := buildSetInput(t.Context(), plan, state)
	if diags.HasError() {
		t.Fatalf("diags: %v", diags)
	}
	if in.SwitchType != "Private" {
		t.Errorf("SwitchType = %q, want \"Private\" (sourced from state)", in.SwitchType)
	}
	if in.AllowManagementOS == nil || *in.AllowManagementOS != true {
		t.Errorf("AllowManagementOS = %v, want pointer to true", in.AllowManagementOS)
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
	got := modelFromVMSwitch(t.Context(), sw, priorList, &diags)
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
	got := modelFromVMSwitch(t.Context(), sw, types.ListUnknown(types.StringType), &diags)
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
