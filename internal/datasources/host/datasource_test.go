package host

import (
	"errors"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/datasource"

	"github.com/windsorcli/terraform-provider-hyperv/internal/hyperv"
	"github.com/windsorcli/terraform-provider-hyperv/internal/testutil"
)

func TestHostDataSource_Schema(t *testing.T) {
	t.Parallel()

	ds := New()
	resp := &datasource.SchemaResponse{}
	ds.Schema(t.Context(), datasource.SchemaRequest{}, resp)

	if resp.Diagnostics.HasError() {
		t.Fatalf("schema diagnostics: %v", resp.Diagnostics)
	}
	wantAttrs := []string{"computer_name", "logical_processor_count", "memory_capacity_bytes", "virtual_machine_path", "virtual_hard_disk_path"}
	for _, name := range wantAttrs {
		if _, ok := resp.Schema.Attributes[name]; !ok {
			t.Errorf("missing attribute %q", name)
		}
	}
}

func TestHostDataSource_Metadata(t *testing.T) {
	t.Parallel()

	ds := New()
	resp := &datasource.MetadataResponse{}
	ds.Metadata(t.Context(), datasource.MetadataRequest{ProviderTypeName: "hyperv"}, resp)
	if resp.TypeName != "hyperv_host" {
		t.Errorf("TypeName = %q, want %q", resp.TypeName, "hyperv_host")
	}
}

func TestHostDataSource_Configure_NilProviderDataIsNoop(t *testing.T) {
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

func TestHostDataSource_Configure_WrongTypeIsClearError(t *testing.T) {
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

func TestHostDataSource_Read_NilClientReturnsDiagnosticNotPanic(t *testing.T) {
	t.Parallel()

	ds := &DataSource{}
	resp := &datasource.ReadResponse{}
	ds.Read(t.Context(), datasource.ReadRequest{}, resp)

	if !resp.Diagnostics.HasError() {
		t.Fatal("expected an error diagnostic when client is nil")
	}
	if !strings.Contains(resp.Diagnostics[0].Summary(), "not configured") {
		t.Errorf("diag summary should name the failure mode; got %q", resp.Diagnostics[0].Summary())
	}
}

// readHost is now a thin transformer over hyperv.Client.GetVMHost — the
// failure modes (transport, envelope, empty stdout, bad JSON) live in
// internal/hyperv/host_test.go and internal/hyperv/errors_test.go. These
// tests just verify the VMHost → Model field mapping and that errors
// surface as diagnostics.

func TestReadHost_HappyPath(t *testing.T) {
	t.Parallel()

	fr := testutil.NewFakeRunner().On("Get-VMHost").Return(testutil.VMHostFixtureJSON, "", 0)
	c := hyperv.NewClient(fr)

	model, diags := readHost(t.Context(), c)
	if diags.HasError() {
		t.Fatalf("unexpected diagnostics: %v", diags)
	}

	if model.ComputerName.ValueString() != "WIN-IUNE600K56E" {
		t.Errorf("ComputerName = %q, want %q", model.ComputerName.ValueString(), "WIN-IUNE600K56E")
	}
	if model.LogicalProcessorCount.ValueInt64() != 20 {
		t.Errorf("LogicalProcessorCount = %d, want 20", model.LogicalProcessorCount.ValueInt64())
	}
	if model.MemoryCapacityBytes.ValueInt64() != 102795845632 {
		t.Errorf("MemoryCapacityBytes = %d, want 102795845632", model.MemoryCapacityBytes.ValueInt64())
	}
	if !strings.Contains(model.VirtualMachinePath.ValueString(), "Hyper-V") {
		t.Errorf("VirtualMachinePath = %q, want substring 'Hyper-V'", model.VirtualMachinePath.ValueString())
	}
}

func TestReadHost_NotFoundGetsRoleSpecificDiagnostic(t *testing.T) {
	// On a singleton like Get-VMHost, ErrNotFound means the Hyper-V role
	// isn't installed — distinct from a transient transport failure.
	// Surface it with a category-specific summary so operators don't
	// confuse it with auth/network problems.
	t.Parallel()

	envelope := `{"category":"ObjectNotFound","message":"Get-VMHost : term not recognized","cmdlet":"Get-VMHost"}`
	fr := testutil.NewFakeRunner().On("Get-VMHost").Return("", envelope, 1)
	c := hyperv.NewClient(fr)

	_, diags := readHost(t.Context(), c)
	if !diags.HasError() {
		t.Fatal("expected an error diagnostic")
	}
	if !strings.Contains(diags[0].Summary(), "Hyper-V is not available") {
		t.Errorf("diag summary should call out the unavailability; got %q", diags[0].Summary())
	}
}

func TestReadHost_ClientErrorBecomesDiagnostic(t *testing.T) {
	t.Parallel()

	fr := testutil.NewFakeRunner().On("Get-VMHost").ReturnErr(errors.New("connection refused"))
	c := hyperv.NewClient(fr)

	_, diags := readHost(t.Context(), c)
	if !diags.HasError() {
		t.Fatal("expected an error diagnostic when the client errors")
	}
	if !strings.Contains(diags[0].Detail(), "connection refused") {
		t.Errorf("diag detail should propagate the underlying error; got %q", diags[0].Detail())
	}
}
