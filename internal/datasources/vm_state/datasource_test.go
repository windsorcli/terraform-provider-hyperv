package vm_state

import (
	"errors"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/datasource"

	"github.com/windsorcli/terraform-provider-hyperv/internal/hyperv"
	"github.com/windsorcli/terraform-provider-hyperv/internal/testutil"
)

// TestDataSource_Schema pins the lookup key and the four-attribute read
// shape; any drift here is a user-visible attribute rename.
func TestDataSource_Schema(t *testing.T) {
	t.Parallel()

	ds := New()
	resp := &datasource.SchemaResponse{}
	ds.Schema(t.Context(), datasource.SchemaRequest{}, resp)

	if resp.Diagnostics.HasError() {
		t.Fatalf("schema diagnostics: %v", resp.Diagnostics)
	}
	wantAttrs := []string{"name", "id", "current", "ip_addresses"}
	for _, name := range wantAttrs {
		if _, ok := resp.Schema.Attributes[name]; !ok {
			t.Errorf("missing attribute %q", name)
		}
	}
	// Deliberately narrow surface: callers needing the full read shape
	// (memory, attachments, boot order) use the resource. Pins the
	// "not on this data source" half of the design.
	for _, omit := range []string{"memory", "cpu", "secure_boot", "hard_disk_drive", "boot_order"} {
		if _, ok := resp.Schema.Attributes[omit]; ok {
			t.Errorf("attribute %q should NOT be on the data source -- it's resource-only", omit)
		}
	}
}

// TestDataSource_Metadata pins the TF type name. Any change is a
// user-visible breaking rename.
func TestDataSource_Metadata(t *testing.T) {
	t.Parallel()

	ds := New()
	resp := &datasource.MetadataResponse{}
	ds.Metadata(t.Context(), datasource.MetadataRequest{ProviderTypeName: "hyperv"}, resp)
	if resp.TypeName != "hyperv_vm_state" {
		t.Errorf("TypeName = %q, want %q", resp.TypeName, "hyperv_vm_state")
	}
}

// TestDataSource_Configure_NilProviderDataIsNoop covers the validate-time
// invocation path -- the provider hasn't resolved a client yet, so
// Configure must NOT panic and must NOT error. Same framework gotcha
// as the other data sources.
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

// TestDataSource_Configure_WrongTypeIsClearError covers the case where
// some future refactor accidentally hands the data source the wrong
// concrete type. The diag must name *hyperv.Client so the operator
// can correct the wiring without spelunking framework internals.
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

// TestReadVMState_HappyPath uses the canned Gen 2 fixture. Pins the
// cmdlet-shape -> tfsdk-attribute mapping for current and ip_addresses.
// The fixture has no NICs, so ip_addresses is a known empty list.
func TestReadVMState_HappyPath(t *testing.T) {
	t.Parallel()

	fr := testutil.NewFakeRunner().
		On("function Get-HypervVM").Return(testutil.VMGen2FixtureJSON, "", 0)
	c := hyperv.NewClient(fr)

	state, diags := readVMState(t.Context(), c, "sample-vm")
	if diags.HasError() {
		t.Fatalf("unexpected diagnostics: %v", diags)
	}
	if state.Name.ValueString() != "sample-vm" {
		t.Errorf("Name = %q", state.Name.ValueString())
	}
	if state.ID.ValueString() != "sample-vm" {
		t.Errorf("ID should mirror Name; got %q", state.ID.ValueString())
	}
	if state.Current.ValueString() != "Off" {
		t.Errorf("Current = %q, want Off", state.Current.ValueString())
	}
	if state.IPAddresses.IsNull() {
		t.Error("IPAddresses should be a known empty list, not null")
	}
	if l := len(state.IPAddresses.Elements()); l != 0 {
		t.Errorf("IPAddresses len = %d, want 0 (fixture has no NICs)", l)
	}
}

// TestReadVMState_FlattenIPAddresses verifies the per-NIC, per-IP order
// preservation: NIC 1's IPs first, NIC 2's IPs second, in cmdlet order
// within each NIC. Pins the mirror-of-resource flatten so a downstream
// consumer that keys off ip_addresses[0] doesn't see drift.
func TestReadVMState_FlattenIPAddresses(t *testing.T) {
	t.Parallel()

	envelope := `{
		"Name":"web","Id":"00000000-0000-0000-0000-000000000000","Generation":2,
		"ProcessorCount":2,"MemoryStartupBytes":4294967296,"MemoryAssignedBytes":4294967296,
		"MemoryDynamicEnabled":false,"MemoryMinimumBytes":null,"MemoryMaximumBytes":null,
		"State":"Running","Notes":"","Path":"C:\\foo","SecureBootEnabled":true,
		"HardDiskDrives":[],
		"NetworkAdapters":[
			{"Name":"primary","SwitchName":"lab","IPAddresses":["10.0.0.5","fe80::1"]},
			{"Name":"backup","SwitchName":"mgmt","IPAddresses":["10.99.0.5"]}
		],
		"DvdDrives":[],"BootOrder":[]
	}`
	fr := testutil.NewFakeRunner().
		On("function Get-HypervVM").Return(envelope, "", 0)
	c := hyperv.NewClient(fr)

	state, diags := readVMState(t.Context(), c, "web")
	if diags.HasError() {
		t.Fatalf("unexpected diagnostics: %v", diags)
	}
	if state.Current.ValueString() != "Running" {
		t.Errorf("Current = %q", state.Current.ValueString())
	}
	if l := len(state.IPAddresses.Elements()); l != 3 {
		t.Fatalf("IPAddresses len = %d, want 3", l)
	}
	// Order matters -- per-NIC then per-IP within a NIC, NICs in cmdlet
	// order. A regression that lex-sorts would surface here.
	got := make([]string, 0, 3)
	for _, e := range state.IPAddresses.Elements() {
		got = append(got, e.String())
	}
	want := []string{`"10.0.0.5"`, `"fe80::1"`, `"10.99.0.5"`}
	for i, w := range want {
		if got[i] != w {
			t.Errorf("IPAddresses[%d] = %s, want %s", i, got[i], w)
		}
	}
}

// TestReadVMState_NotFoundIsAttributeAnchoredDiagnostic covers the
// missing-VM case. Anchored on `name` so the operator sees which
// lookup value didn't resolve. Data sources have no RemoveResource
// semantics; a missing VM is a hard error.
func TestReadVMState_NotFoundIsAttributeAnchoredDiagnostic(t *testing.T) {
	t.Parallel()

	envelope := `{"category":"ObjectNotFound","message":"VM not found","cmdlet":"Get-VM"}`
	fr := testutil.NewFakeRunner().
		On("function Get-HypervVM").Return("", envelope, 1)
	c := hyperv.NewClient(fr)

	_, diags := readVMState(t.Context(), c, "missing-vm")
	if !diags.HasError() {
		t.Fatal("expected an error diagnostic")
	}
	if !strings.Contains(diags[0].Summary(), "not found") {
		t.Errorf("summary = %q, want substring 'not found'", diags[0].Summary())
	}
	if !strings.Contains(diags[0].Detail(), `"missing-vm"`) {
		t.Errorf("detail should echo the lookup name; got %q", diags[0].Detail())
	}
}

// TestReadVMState_UnavailableIsTransientDiagnostic covers the vmms-down
// case. Phrased so a vmms restart during a plan doesn't read like
// "the VM is gone".
func TestReadVMState_UnavailableIsTransientDiagnostic(t *testing.T) {
	t.Parallel()

	envelope := `{"category":"ResourceUnavailable","message":"vmms not running","cmdlet":"Get-VM"}`
	fr := testutil.NewFakeRunner().
		On("function Get-HypervVM").Return("", envelope, 1)
	c := hyperv.NewClient(fr)

	_, diags := readVMState(t.Context(), c, "any-name")
	if !diags.HasError() {
		t.Fatal("expected an error diagnostic")
	}
	if !strings.Contains(diags[0].Summary(), "management service") {
		t.Errorf("summary = %q, want substring 'management service'", diags[0].Summary())
	}
}

// TestReadVMState_GenericClientErrorBecomesDiagnostic covers transport
// failures (connection refused, ctx canceled). The diagnostic must
// propagate the underlying message so operators can see the cause.
func TestReadVMState_GenericClientErrorBecomesDiagnostic(t *testing.T) {
	t.Parallel()

	fr := testutil.NewFakeRunner().
		On("function Get-HypervVM").ReturnErr(errors.New("connection refused"))
	c := hyperv.NewClient(fr)

	_, diags := readVMState(t.Context(), c, "any-name")
	if !diags.HasError() {
		t.Fatal("expected an error diagnostic")
	}
	if !strings.Contains(diags[0].Detail(), "connection refused") {
		t.Errorf("diag should propagate the underlying error; got %q", diags[0].Detail())
	}
}
