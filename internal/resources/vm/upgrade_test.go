package vm

import (
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/types"
)

// TestUpgradeV0ToV1 exercises the rename / promote / null-out mapping
// for a representative v0 state struct.
func TestUpgradeV0ToV1(t *testing.T) {
	prior := priorModelV0{
		ID:          types.StringValue("node01"),
		Name:        types.StringValue("node01"),
		Generation:  types.Int64Value(2),
		VCPU:        types.Int64Value(4),
		MemoryBytes: types.Int64Value(4294967296),
		SecureBoot:  types.BoolValue(false),
		Notes:       types.StringValue("k8s control plane"),
		State:       types.StringValue("Running"),
		Path:        types.StringValue("C:/ProgramData/Microsoft/Windows/Hyper-V"),
	}

	got := upgradeV0ToV1(prior)

	// Carried-through scalars.
	if got.ID.ValueString() != "node01" {
		t.Errorf("ID: got %q, want %q", got.ID.ValueString(), "node01")
	}
	if got.Name.ValueString() != "node01" {
		t.Errorf("Name: got %q, want %q", got.Name.ValueString(), "node01")
	}
	if got.Generation.ValueInt64() != 2 {
		t.Errorf("Generation: got %d, want 2", got.Generation.ValueInt64())
	}
	if got.SecureBoot.ValueBool() != false {
		t.Errorf("SecureBoot: got %v, want false", got.SecureBoot.ValueBool())
	}
	if got.Notes.ValueString() != "k8s control plane" {
		t.Errorf("Notes: got %q", got.Notes.ValueString())
	}
	if got.Path.ValueString() != "C:/ProgramData/Microsoft/Windows/Hyper-V" {
		t.Errorf("Path: got %q", got.Path.ValueString())
	}

	// Renamed: vcpu -> cpu.count.
	if got.CPU == nil {
		t.Fatal("CPU: got nil, want &CPUModel")
	}
	if got.CPU.Count.ValueInt64() != 4 {
		t.Errorf("CPU.Count: got %d, want 4", got.CPU.Count.ValueInt64())
	}

	// Renamed: memory_bytes -> memory.startup_bytes.
	if got.Memory == nil {
		t.Fatal("Memory: got nil, want &MemoryModel")
	}
	if got.Memory.StartupBytes.ValueInt64() != 4294967296 {
		t.Errorf("Memory.StartupBytes: got %d, want 4294967296", got.Memory.StartupBytes.ValueInt64())
	}

	// Promoted: flat state string -> nested block, but left null
	// because v0 users had no way to manage power state.
	if got.State != nil {
		t.Errorf("State: got %+v, want nil (block left unmanaged)", got.State)
	}

	// New inline lists initialized empty (known, not null) so the
	// v1 state-shape constraint holds until the next refresh.
	if got.HardDiskDrives == nil || len(got.HardDiskDrives) != 0 {
		t.Errorf("HardDiskDrives: got %+v, want empty []HardDiskDriveModel{}", got.HardDiskDrives)
	}
	if got.NetworkAdapters == nil || len(got.NetworkAdapters) != 0 {
		t.Errorf("NetworkAdapters: got %+v, want empty", got.NetworkAdapters)
	}
	if got.DvdDrives == nil || len(got.DvdDrives) != 0 {
		t.Errorf("DvdDrives: got %+v, want empty", got.DvdDrives)
	}
	if got.BootOrder == nil || len(got.BootOrder) != 0 {
		t.Errorf("BootOrder: got %+v, want empty", got.BootOrder)
	}

	// IPAddresses left null (Computed; next refresh fills from host).
	if !got.IPAddresses.IsNull() {
		t.Errorf("IPAddresses: got %+v, want null", got.IPAddresses)
	}
}

// TestUpgradeV0ToV1_NullOptionals covers a v0 state where Optional
// fields (notes, secure_boot) were null -- e.g. a VM created with the
// minimum config. The mapping should preserve null-ness rather than
// fabricating placeholder values.
func TestUpgradeV0ToV1_NullOptionals(t *testing.T) {
	prior := priorModelV0{
		ID:          types.StringValue("legacy-app"),
		Name:        types.StringValue("legacy-app"),
		Generation:  types.Int64Value(1),
		VCPU:        types.Int64Value(1),
		MemoryBytes: types.Int64Value(2147483648),
		SecureBoot:  types.BoolNull(),
		Notes:       types.StringNull(),
		State:       types.StringValue("Off"),
		Path:        types.StringValue("C:/ProgramData/Microsoft/Windows/Hyper-V"),
	}

	got := upgradeV0ToV1(prior)

	if !got.SecureBoot.IsNull() {
		t.Errorf("SecureBoot: got %+v, want null", got.SecureBoot)
	}
	if !got.Notes.IsNull() {
		t.Errorf("Notes: got %+v, want null", got.Notes)
	}
	if got.CPU.Count.ValueInt64() != 1 {
		t.Errorf("CPU.Count: got %d, want 1", got.CPU.Count.ValueInt64())
	}
}

// TestUpgradeStateRegistration verifies the Resource declares an
// upgrader for v0 -- the only registered version today, asserted by
// key so a future v1->v2 entry can land without rewriting the test.
func TestUpgradeStateRegistration(t *testing.T) {
	r := &Resource{}
	upgraders := r.UpgradeState(t.Context())
	if _, ok := upgraders[0]; !ok {
		t.Fatalf("UpgradeState: missing v0 upgrader; got versions %+v", keysOf(upgraders))
	}
	if upgraders[0].PriorSchema == nil {
		t.Error("UpgradeState[0].PriorSchema: got nil, want priorSchemaV0()")
	}
	if upgraders[0].StateUpgrader == nil {
		t.Error("UpgradeState[0].StateUpgrader: got nil, want non-nil migration func")
	}
}

func keysOf[V any](m map[int64]V) []int64 {
	out := make([]int64, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

// TestUpgradeV1ToV2_LeavesShutdownModeNull locks the only v1 -> v2
// shape change: state.shutdown_mode is added to the nested state
// block but populated with null (not a "turn_off" placeholder).
// v1 users never had a chance to choose a value; storing null after
// upgrade preserves the "user didn't manage" semantic, and the
// script defaults to turn_off on absent input -- same on-host
// behavior as v1.
func TestUpgradeV1ToV2_LeavesShutdownModeNull(t *testing.T) {
	prior := priorModelV1{
		ID:         types.StringValue("vm01"),
		Name:       types.StringValue("vm01"),
		Generation: types.Int64Value(2),
		CPU:        &CPUModel{Count: types.Int64Value(2)},
		Memory:     &MemoryModel{StartupBytes: types.Int64Value(4294967296)},
		SecureBoot: types.BoolValue(true),
		Notes:      types.StringNull(),
		Path:       types.StringValue("C:/foo"),
		State: &priorStateModelV1{
			Desired: types.StringValue("Running"),
			Current: types.StringValue("Running"),
		},
	}

	got := upgradeV1ToV2(prior)

	if got.State == nil {
		t.Fatal("State: got nil, want populated v2 block")
	}
	if got.State.Desired.ValueString() != "Running" {
		t.Errorf("State.Desired: got %q, want Running", got.State.Desired.ValueString())
	}
	if got.State.Current.ValueString() != "Running" {
		t.Errorf("State.Current: got %q, want Running", got.State.Current.ValueString())
	}
	if !got.State.ShutdownMode.IsNull() {
		t.Errorf("State.ShutdownMode: got %+v, want null (v1 users never managed shutdown_mode)",
			got.State.ShutdownMode)
	}
}

// TestUpgradeV1ToV2_PreservesNullState covers the v1 case where the
// user never opted into managing power state (state block null on
// disk). The v2 state block stays nil; the user opts in by writing
// `state = {}` later.
func TestUpgradeV1ToV2_PreservesNullState(t *testing.T) {
	prior := priorModelV1{
		ID:         types.StringValue("vm01"),
		Name:       types.StringValue("vm01"),
		Generation: types.Int64Value(2),
		CPU:        &CPUModel{Count: types.Int64Value(2)},
		Memory:     &MemoryModel{StartupBytes: types.Int64Value(4294967296)},
		SecureBoot: types.BoolNull(),
		Notes:      types.StringNull(),
		Path:       types.StringValue("C:/foo"),
		State:      nil,
	}

	got := upgradeV1ToV2(prior)

	if got.State != nil {
		t.Errorf("State: got %+v, want nil (user never opted into power-state management)", got.State)
	}
}

// TestUpgradeStateRegistration_V1Entry verifies the v1 upgrader is
// registered alongside the v0 one. Sister test of
// TestUpgradeStateRegistration but pinned to the v1 source.
func TestUpgradeStateRegistration_V1Entry(t *testing.T) {
	r := &Resource{}
	upgraders := r.UpgradeState(t.Context())
	if _, ok := upgraders[1]; !ok {
		t.Fatalf("UpgradeState: missing v1 upgrader; got versions %+v", keysOf(upgraders))
	}
	if upgraders[1].PriorSchema == nil {
		t.Error("UpgradeState[1].PriorSchema: got nil, want priorSchemaV1()")
	}
	if upgraders[1].StateUpgrader == nil {
		t.Error("UpgradeState[1].StateUpgrader: got nil, want non-nil migration func")
	}
}
