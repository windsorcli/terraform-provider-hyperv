package vswitch

import (
	"errors"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/datasource"

	"github.com/xeitu/terraform-provider-hyperv/internal/hyperv"
	"github.com/xeitu/terraform-provider-hyperv/internal/testutil"
)

// Schema must expose the lookup key and the cmdlet's read shape; any drift
// here is a user-visible attribute rename.
func TestDataSource_Schema(t *testing.T) {
	t.Parallel()

	ds := New()
	resp := &datasource.SchemaResponse{}
	ds.Schema(t.Context(), datasource.SchemaRequest{}, resp)

	if resp.Diagnostics.HasError() {
		t.Fatalf("schema diagnostics: %v", resp.Diagnostics)
	}
	wantAttrs := []string{
		"name",
		"nat_name",
		"id",
		"switch_type",
		"allow_management_os",
		"notes",
		"net_adapter_interface_description",
		"nat_internal_address_prefix",
		"nat_host_address",
	}
	for _, name := range wantAttrs {
		if _, ok := resp.Schema.Attributes[name]; !ok {
			t.Errorf("missing attribute %q", name)
		}
	}
	// net_adapter_names is intentionally absent on the data source; the
	// resource preserves it as user intent but Get-VMSwitch can't reproduce it.
	if _, ok := resp.Schema.Attributes["net_adapter_names"]; ok {
		t.Error("net_adapter_names should NOT be on the data-source schema -- the cmdlet doesn't return it")
	}
}

// Metadata pins the data source's TF type name. Any change here is a
// user-visible breaking rename.
func TestDataSource_Metadata(t *testing.T) {
	t.Parallel()

	ds := New()
	resp := &datasource.MetadataResponse{}
	ds.Metadata(t.Context(), datasource.MetadataRequest{ProviderTypeName: "hyperv"}, resp)
	if resp.TypeName != "hyperv_virtual_switch" {
		t.Errorf("TypeName = %q, want %q", resp.TypeName, "hyperv_virtual_switch")
	}
}

// Configure with nil ProviderData (validate-time invocation before the
// provider has resolved) must NOT panic and must NOT error -- same
// framework gotcha as the hyperv_host data source.
func TestDataSource_Configure_NilProviderDataIsNoop(t *testing.T) {
	t.Parallel()

	ds, ok := New().(*DataSource)
	if !ok {
		t.Fatal("New() did not return *DataSource")
	}
	resp := &datasource.ConfigureResponse{}
	ds.Configure(t.Context(), datasource.ConfigureRequest{ProviderData: nil}, resp)
	if resp.Diagnostics.HasError() {
		t.Errorf("nil ProviderData should be a no-op; got diags: %v", resp.Diagnostics)
	}
	if ds.client != nil {
		t.Error("client should remain nil when ProviderData is nil")
	}
}

// Configure with the wrong ProviderData concrete type must produce a
// diagnostic that names *hyperv.Client so the operator can correct the
// provider wiring without spelunking the framework internals.
func TestDataSource_Configure_WrongTypeIsClearError(t *testing.T) {
	t.Parallel()

	ds, ok := New().(*DataSource)
	if !ok {
		t.Fatal("New() did not return *DataSource")
	}
	resp := &datasource.ConfigureResponse{}
	ds.Configure(t.Context(),
		datasource.ConfigureRequest{ProviderData: "not a client"},
		resp,
	)
	if !resp.Diagnostics.HasError() {
		t.Fatal("expected an error diagnostic")
	}
	if !strings.Contains(resp.Diagnostics[0].Detail(), "*hyperv.Client") {
		t.Errorf("diag detail should name the expected type; got %q", resp.Diagnostics[0].Detail())
	}
}

// Happy path: canned JSON from the fakeRunner becomes a fully-populated
// Model. Pins the cmdlet-shape -> tfsdk-attribute mapping.
func TestReadVSwitch_HappyPath(t *testing.T) {
	t.Parallel()

	fr := testutil.NewFakeRunner().
		On("function Get-HypervSwitch").Return(testutil.VMSwitchExternalFixtureJSON, "", 0)
	c := hyperv.NewClient(fr)

	state, diags := readVSwitch(t.Context(), c, "external-switch", "")
	if diags.HasError() {
		t.Fatalf("unexpected diagnostics: %v", diags)
	}
	if state.Name.ValueString() != "external-switch" {
		t.Errorf("Name = %q", state.Name.ValueString())
	}
	if state.ID.ValueString() != "external-switch" {
		t.Errorf("ID should mirror Name; got %q", state.ID.ValueString())
	}
	if state.SwitchType.ValueString() != "External" {
		t.Errorf("SwitchType = %q", state.SwitchType.ValueString())
	}
	if !state.AllowManagementOS.ValueBool() {
		t.Error("AllowManagementOS should be true on the External fixture")
	}
	if state.NetAdapterInterfaceDescription.ValueString() != "Intel(R) Ethernet I210" {
		t.Errorf("NetAdapterInterfaceDescription = %q", state.NetAdapterInterfaceDescription.ValueString())
	}
}

// readVSwitch with a non-empty natName threads it through GetVMSwitch
// so the script's joined read populates the NAT fields and reports
// SwitchType=NAT. Pins the data-source path users will follow when
// they want the NAT-augmented view rather than the bare Internal one.
func TestReadVSwitch_NATAugmentedRead(t *testing.T) {
	t.Parallel()

	fr := testutil.NewFakeRunner().
		On("function Get-HypervSwitch").Return(testutil.VMSwitchNATFixtureJSON, "", 0)
	c := hyperv.NewClient(fr)

	state, diags := readVSwitch(t.Context(), c, "windsor-nat", "windsor-nat")
	if diags.HasError() {
		t.Fatalf("unexpected diagnostics: %v", diags)
	}
	if state.SwitchType.ValueString() != "NAT" {
		t.Errorf("SwitchType = %q, want NAT", state.SwitchType.ValueString())
	}
	if state.NatInternalAddressPrefix.ValueString() != "192.168.100.0/24" {
		t.Errorf("NatInternalAddressPrefix = %q", state.NatInternalAddressPrefix.ValueString())
	}
	if state.NatHostAddress.ValueString() != "192.168.100.1" {
		t.Errorf("NatHostAddress = %q", state.NatHostAddress.ValueString())
	}
	// readVSwitch leaves NatName at its zero value -- the caller (Read)
	// echoes config.NatName back into state. Pin that here so a future
	// refactor that populates NatName from the wire shape doesn't slip
	// through the round-trip contract.
	if !state.NatName.IsNull() {
		t.Errorf("NatName should be null (echoed by Read, not readVSwitch); got %q",
			state.NatName.ValueString())
	}
}

// readVSwitch with empty natName returns the bare VMSwitch shape and
// leaves nat_* fields null. Locks the "non-NAT switches don't get
// NAT augmentation" half of the contract.
func TestReadVSwitch_NoNatNameLeavesNATFieldsNull(t *testing.T) {
	t.Parallel()

	fr := testutil.NewFakeRunner().
		On("function Get-HypervSwitch").Return(testutil.VMSwitchExternalFixtureJSON, "", 0)
	c := hyperv.NewClient(fr)

	state, diags := readVSwitch(t.Context(), c, "external-switch", "")
	if diags.HasError() {
		t.Fatalf("unexpected diagnostics: %v", diags)
	}
	if !state.NatInternalAddressPrefix.IsNull() {
		t.Errorf("NatInternalAddressPrefix should be null for non-NAT switch; got %q",
			state.NatInternalAddressPrefix.ValueString())
	}
	if !state.NatHostAddress.IsNull() {
		t.Errorf("NatHostAddress should be null for non-NAT switch; got %q",
			state.NatHostAddress.ValueString())
	}
}

// ErrNotFound from the typed client surfaces as an attribute-anchored
// diagnostic so the operator sees which `name` value didn't resolve. Data
// sources don't have RemoveResource semantics -- a missing switch is a
// hard error, not state reconciliation.
func TestReadVSwitch_NotFoundIsAttributeAnchoredDiagnostic(t *testing.T) {
	t.Parallel()

	envelope := `{"category":"ObjectNotFound","message":"VM switch not found","cmdlet":"Get-VMSwitch"}`
	fr := testutil.NewFakeRunner().
		On("function Get-HypervSwitch").Return("", envelope, 1)
	c := hyperv.NewClient(fr)

	_, diags := readVSwitch(t.Context(), c, "missing", "")
	if !diags.HasError() {
		t.Fatal("expected an error diagnostic")
	}
	if !strings.Contains(diags[0].Summary(), "not found") {
		t.Errorf("summary = %q, want substring 'not found'", diags[0].Summary())
	}
	if !strings.Contains(diags[0].Detail(), `"missing"`) {
		t.Errorf("detail should echo the lookup name for the operator; got %q", diags[0].Detail())
	}
}

// ErrUnavailable surfaces as a transient diagnostic with vmms-specific
// phrasing -- not "switch not found" -- so a vmms restart during a plan
// doesn't masquerade as a deletion.
func TestReadVSwitch_UnavailableIsTransientDiagnostic(t *testing.T) {
	t.Parallel()

	envelope := `{"category":"ResourceUnavailable","message":"vmms not running","cmdlet":"Get-VMSwitch"}`
	fr := testutil.NewFakeRunner().
		On("function Get-HypervSwitch").Return("", envelope, 1)
	c := hyperv.NewClient(fr)

	_, diags := readVSwitch(t.Context(), c, "any-name", "")
	if !diags.HasError() {
		t.Fatal("expected an error diagnostic")
	}
	if !strings.Contains(diags[0].Summary(), "management service") {
		t.Errorf("summary = %q, want substring 'management service'", diags[0].Summary())
	}
}

// Transport-level errors (connection refused, ctx canceled, etc.) bypass
// the typed-error sentinels and surface as a generic "Read failed"
// diagnostic; locking the diagnostic at least propagates the underlying
// message so operators can see what went wrong.
func TestReadVSwitch_GenericClientErrorBecomesDiagnostic(t *testing.T) {
	t.Parallel()

	fr := testutil.NewFakeRunner().
		On("function Get-HypervSwitch").ReturnErr(errors.New("connection refused"))
	c := hyperv.NewClient(fr)

	_, diags := readVSwitch(t.Context(), c, "any-name", "")
	if !diags.HasError() {
		t.Fatal("expected an error diagnostic")
	}
	if !strings.Contains(diags[0].Detail(), "connection refused") {
		t.Errorf("diag should propagate the underlying error; got %q", diags[0].Detail())
	}
}
