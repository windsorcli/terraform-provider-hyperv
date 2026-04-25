package host

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/datasource"

	"github.com/windsorcli/terraform-provider-hyperv/internal/testutil"
)

// JSON shape captured from spike #2's Get-VMHost probe against a real
// Server 2022 host. See docs/spikes/02-json-contract.md.
const vmHostFixtureJSON = `{
	"ComputerName": "WIN-IUNE600K56E",
	"LogicalProcessorCount": 20,
	"MemoryCapacity": 102795845632,
	"VirtualMachinePath": "C:\\ProgramData\\Microsoft\\Windows\\Hyper-V",
	"VirtualHardDiskPath": "C:\\ProgramData\\Microsoft\\Windows\\Virtual Hard Disks"
}`

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
	if ds.runner != nil {
		t.Error("runner should remain nil when ProviderData is nil")
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
		datasource.ConfigureRequest{ProviderData: "not a connection"},
		resp,
	)
	if !resp.Diagnostics.HasError() {
		t.Fatal("expected an error diagnostic")
	}
	if !strings.Contains(resp.Diagnostics[0].Detail(), "connection.Connection") {
		t.Errorf("diag detail should name the expected type; got %q", resp.Diagnostics[0].Detail())
	}
}

// readHost is the testable core of the data source's Read — these tests
// exercise every branch (happy path, transport error, non-zero exit, empty
// stdout, malformed JSON) without needing a Terraform framework harness.
//
// Pester tests for the underlying Get-VMHost cmdlet are deferred until the
// next PR extracts the inline body into internal/scripts/host/get.ps1; the
// JSON shape used here is what spike #2 captured against a real Server
// 2022 host (docs/spikes/02-json-contract.md), and the demo against the
// real test box confirmed the round-trip end-to-end.
//
// Acceptance tests (TF_ACC=1) are deferred until the acceptance.yaml
// workflow lands with self-hosted runners (PLAN.md §16.8).

func TestReadHost_HappyPath(t *testing.T) {
	t.Parallel()

	fr := testutil.NewFakeRunner().On("Get-VMHost").Return(vmHostFixtureJSON, "", 0)

	model, diags := readHost(t.Context(), fr)
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

func TestReadHost_TransportErrorIsClearDiagnostic(t *testing.T) {
	t.Parallel()

	fr := testutil.NewFakeRunner().On("Get-VMHost").ReturnErr(errors.New("connection refused"))

	_, diags := readHost(t.Context(), fr)
	if !diags.HasError() {
		t.Fatal("expected an error diagnostic on transport failure")
	}
	d := diags[0]
	if !strings.Contains(d.Detail(), "Transport error") {
		t.Errorf("diag detail should label transport error; got %q", d.Detail())
	}
	if !strings.Contains(d.Detail(), "connection refused") {
		t.Errorf("diag detail should propagate the underlying error; got %q", d.Detail())
	}
}

func TestReadHost_NonZeroExitSurfacesEnvelope(t *testing.T) {
	t.Parallel()

	envelope := `{"category":"PermissionDenied","message":"Access denied"}`
	fr := testutil.NewFakeRunner().On("Get-VMHost").Return("", envelope, 1)

	_, diags := readHost(t.Context(), fr)
	if !diags.HasError() {
		t.Fatal("expected an error diagnostic on non-zero exit")
	}
	d := diags[0]
	if !strings.Contains(d.Detail(), "exited 1") {
		t.Errorf("diag detail should report the exit code; got %q", d.Detail())
	}
	if !strings.Contains(d.Detail(), "PermissionDenied") {
		t.Errorf("diag detail should include the stderr envelope; got %q", d.Detail())
	}
}

func TestReadHost_EmptyStdoutFailsLoudly(t *testing.T) {
	t.Parallel()

	// Exit 0 but stdout silent — usually means the preamble or encoding
	// pin didn't apply. A clear diagnostic is more useful than a generic
	// JSON parse error.
	fr := testutil.NewFakeRunner().On("Get-VMHost").Return("   \n  ", "", 0)

	_, diags := readHost(t.Context(), fr)
	if !diags.HasError() {
		t.Fatal("expected an error diagnostic on empty stdout")
	}
	if !strings.Contains(diags[0].Detail(), "empty stdout") {
		t.Errorf("diag detail should call out empty stdout specifically; got %q", diags[0].Detail())
	}
}

func TestReadHost_MalformedJSONSurfacesParseError(t *testing.T) {
	t.Parallel()

	fr := testutil.NewFakeRunner().On("Get-VMHost").Return(`{"ComputerName": "incomplete`, "", 0)

	_, diags := readHost(t.Context(), fr)
	if !diags.HasError() {
		t.Fatal("expected an error diagnostic on bad JSON")
	}
	d := diags[0]
	if !strings.Contains(d.Summary(), "JSON parse") {
		t.Errorf("diag summary should label parse failure; got %q", d.Summary())
	}
	if !strings.Contains(d.Detail(), "ComputerName") {
		t.Errorf("diag detail should echo the offending stdout; got %q", d.Detail())
	}
}

func TestReadHost_PreservesJSONShapeFromSpike(t *testing.T) {
	// Pin the spike #2 JSON shape: if Get-VMHost's output ever changes
	// shape and the fixture above is stale, this test fails immediately,
	// surfacing the contract drift at unit-test time before acceptance.
	t.Parallel()

	var h vmHostJSON
	if err := json.Unmarshal([]byte(vmHostFixtureJSON), &h); err != nil {
		t.Fatalf("fixture JSON didn't unmarshal — spike #2 shape may have drifted: %v", err)
	}
	if h.ComputerName == "" || h.LogicalProcessorCount == 0 || h.MemoryCapacity == 0 {
		t.Errorf("required fields missing after unmarshal: %+v", h)
	}
}
