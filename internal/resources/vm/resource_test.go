package vm

import (
	"reflect"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/windsorcli/terraform-provider-hyperv/internal/hyperv"
	pathtype "github.com/windsorcli/terraform-provider-hyperv/internal/types/path"
)

// hasPlanModifier checks if any plan-modifier in `mods` has a type whose
// package-qualified name contains `keyword`. Same helper shape as the
// vswitch / image_file / vhd resource tests use.
func hasPlanModifier[M any](mods []M, keyword string) bool {
	for _, pm := range mods {
		if strings.Contains(strings.ToLower(reflect.TypeOf(pm).String()), strings.ToLower(keyword)) {
			return true
		}
	}
	return false
}

// TestResource_Schema verifies every locked-in attribute is present.
// Drift here is a contract break for users.
func TestResource_Schema(t *testing.T) {
	t.Parallel()

	r := New()
	resp := &resource.SchemaResponse{}
	r.Schema(t.Context(), resource.SchemaRequest{}, resp)

	if resp.Diagnostics.HasError() {
		t.Fatalf("schema diagnostics: %v", resp.Diagnostics)
	}
	wantAttrs := []string{
		"id", "name", "generation", "cpu", "memory",
		"hard_disk_drive", "network_adapter",
		"secure_boot", "notes", "state", "path",
	}
	for _, name := range wantAttrs {
		if _, ok := resp.Schema.Attributes[name]; !ok {
			t.Errorf("missing attribute %q", name)
		}
	}
}

// TestResource_Schema_RequiresReplaceOnImmutableAttrs locks the immutable
// attributes -- name and generation. Hyper-V can't rename a VM in place
// or convert between generations, so these must trigger destroy+recreate.
func TestResource_Schema_RequiresReplaceOnImmutableAttrs(t *testing.T) {
	t.Parallel()

	r := New()
	resp := &resource.SchemaResponse{}
	r.Schema(t.Context(), resource.SchemaRequest{}, resp)

	nameAttr, ok := resp.Schema.Attributes["name"].(schema.StringAttribute)
	if !ok {
		t.Fatalf("name is not a StringAttribute (got %T)", resp.Schema.Attributes["name"])
	}
	if !hasPlanModifier(nameAttr.PlanModifiers, "RequiresReplace") {
		t.Error(`"name" must carry RequiresReplace`)
	}

	genAttr, ok := resp.Schema.Attributes["generation"].(schema.Int64Attribute)
	if !ok {
		t.Fatalf("generation is not an Int64Attribute (got %T)", resp.Schema.Attributes["generation"])
	}
	if !hasPlanModifier(genAttr.PlanModifiers, "RequiresReplace") {
		t.Error(`"generation" must carry RequiresReplace`)
	}
}

// TestResource_Schema_CPUAndMemoryAreInPlaceMutable confirms the
// nested cpu/memory blocks don't carry RequiresReplace -- Set-VMProcessor
// and Set-VMMemory are the in-place paths. The check looks at the inner
// scalar attributes (cpu.count, memory.startup_bytes) since those are
// where the RequiresReplace would be expressed if it were ever added.
func TestResource_Schema_CPUAndMemoryAreInPlaceMutable(t *testing.T) {
	t.Parallel()

	r := New()
	resp := &resource.SchemaResponse{}
	r.Schema(t.Context(), resource.SchemaRequest{}, resp)

	cases := []struct {
		block, inner string
	}{
		{"cpu", "count"},
		{"memory", "startup_bytes"},
	}
	for _, tc := range cases {
		blockAttr, ok := resp.Schema.Attributes[tc.block].(schema.SingleNestedAttribute)
		if !ok {
			t.Fatalf("%q is not a SingleNestedAttribute (got %T)", tc.block, resp.Schema.Attributes[tc.block])
		}
		// Block-level RequiresReplace would propagate through the whole
		// thing, so check it doesn't carry one either.
		if hasPlanModifier(blockAttr.PlanModifiers, "RequiresReplace") {
			t.Errorf("%q (block) must NOT carry RequiresReplace", tc.block)
		}
		innerAttr, ok := blockAttr.Attributes[tc.inner].(schema.Int64Attribute)
		if !ok {
			t.Fatalf("%q.%q is not an Int64Attribute (got %T)", tc.block, tc.inner, blockAttr.Attributes[tc.inner])
		}
		if hasPlanModifier(innerAttr.PlanModifiers, "RequiresReplace") {
			t.Errorf("%q.%q must NOT carry RequiresReplace", tc.block, tc.inner)
		}
	}
}

// TestResource_Schema_UseStateForUnknownOnComputedAttrs prevents phantom
// (known after apply) diffs when the user changes nothing relevant.
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
	checkString("state")
	checkString("path")

	if boolAttr, ok := resp.Schema.Attributes["secure_boot"].(schema.BoolAttribute); ok {
		if !hasPlanModifier(boolAttr.PlanModifiers, "UseStateForUnknown") {
			t.Error(`"secure_boot" must carry UseStateForUnknown`)
		}
	} else {
		t.Errorf(`"secure_boot" missing or wrong type`)
	}
}

// TestResource_Schema_GenerationOneOf locks the OneOf(1, 2) constraint.
func TestResource_Schema_GenerationOneOf(t *testing.T) {
	t.Parallel()

	r := New()
	resp := &resource.SchemaResponse{}
	r.Schema(t.Context(), resource.SchemaRequest{}, resp)

	intAttr, ok := resp.Schema.Attributes["generation"].(schema.Int64Attribute)
	if !ok {
		t.Fatalf("generation is not an Int64Attribute (got %T)", resp.Schema.Attributes["generation"])
	}
	if len(intAttr.Validators) == 0 {
		t.Fatal("generation must carry at least one validator (OneOf 1, 2)")
	}
	desc := intAttr.Validators[0].Description(t.Context())
	for _, want := range []string{"1", "2"} {
		if !strings.Contains(desc, want) {
			t.Errorf("OneOf description should mention %q; got %q", want, desc)
		}
	}
}

// TestResource_Metadata pins the resource's TF type name. Any change here
// is a user-visible breaking rename.
func TestResource_Metadata(t *testing.T) {
	t.Parallel()

	r := New()
	resp := &resource.MetadataResponse{}
	r.Metadata(t.Context(), resource.MetadataRequest{ProviderTypeName: "hyperv"}, resp)
	if resp.TypeName != "hyperv_vm" {
		t.Errorf("TypeName = %q, want %q", resp.TypeName, "hyperv_vm")
	}
}

// TestResource_Configure_NilProviderDataIsNoop confirms validate-time
// Configure (which passes nil ProviderData) doesn't panic.
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

// TestResource_Configure_WrongTypeIsClearError ensures a misconfigured
// provider produces a diagnostic that names *hyperv.Client.
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

// TestResource_ConfigValidators_RegistersAll confirms every validator
// is wired into ConfigValidators(). Drift here means the resource is
// shipping without a documented validator; usually the cmdlet-error
// path still catches the problem at apply time, but plan-time
// diagnostics are the contract.
func TestResource_ConfigValidators_RegistersAll(t *testing.T) {
	t.Parallel()

	r, ok := New().(*Resource)
	if !ok {
		t.Fatal("New() did not return *Resource")
	}
	got := r.ConfigValidators(t.Context())
	if len(got) != 2 {
		t.Fatalf("got %d ConfigValidators, want 2 (secure_boot rejected for gen 1, network_adapter unique names)", len(got))
	}
}

// TestSecureBootValidator exercises the one-directional rule: secure_boot
// is rejected for generation=1 (BIOS, no Secure Boot concept). Optional
// for generation=2 (host's default applies when omitted).
func TestSecureBootValidator(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name      string
		model     Model
		wantError bool
		wantPath  string
	}{
		{
			name: "gen 2 with secure_boot=true -> ok",
			model: Model{
				Generation: types.Int64Value(2),
				SecureBoot: types.BoolValue(true),
			},
		},
		{
			name: "gen 2 with secure_boot=false -> ok",
			model: Model{
				Generation: types.Int64Value(2),
				SecureBoot: types.BoolValue(false),
			},
		},
		{
			name: "gen 2 without secure_boot -> ok (host default applies)",
			model: Model{
				Generation: types.Int64Value(2),
				SecureBoot: types.BoolNull(),
			},
		},
		{
			name: "gen 1 without secure_boot -> ok (omitted is fine)",
			model: Model{
				Generation: types.Int64Value(1),
				SecureBoot: types.BoolNull(),
			},
		},
		{
			name: "gen 1 with secure_boot=true -> fires",
			model: Model{
				Generation: types.Int64Value(1),
				SecureBoot: types.BoolValue(true),
			},
			wantError: true,
			wantPath:  "secure_boot",
		},
		{
			name: "gen 1 with secure_boot=false -> fires (still rejected, even when explicitly off)",
			model: Model{
				Generation: types.Int64Value(1),
				SecureBoot: types.BoolValue(false),
			},
			wantError: true,
			wantPath:  "secure_boot",
		},
		{
			name: "generation unknown -> skip (deferred dep)",
			model: Model{
				Generation: types.Int64Unknown(),
				SecureBoot: types.BoolValue(true),
			},
		},
		{
			name: "secure_boot unknown -> skip (deferred dep)",
			model: Model{
				Generation: types.Int64Value(1),
				SecureBoot: types.BoolUnknown(),
			},
		},
	}
	v := secureBootRejectedForGen1Validator{}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			diags := v.validate(tc.model)
			assertValidatorDiags(t, diags, tc.wantError, tc.wantPath)
		})
	}
}

// assertValidatorDiags is the shared assertion shape for validator-table
// tests. Verifies presence/absence of an error and, when expected, that
// the error is anchored to the right attribute path via the
// DiagnosticWithPath interface (matches Terraform's plan-output highlight
// path, not just the message text).
func assertValidatorDiags(t *testing.T, diags diag.Diagnostics, wantError bool, wantPath string) {
	t.Helper()
	if !wantError {
		if diags.HasError() {
			t.Errorf("expected validator to pass; got error(s): %v", diags.Errors())
		}
		return
	}
	if !diags.HasError() {
		t.Fatalf("expected validator to fire on attribute %q; got no error", wantPath)
	}
	first := diags.Errors()[0]
	withPath, ok := first.(diag.DiagnosticWithPath)
	if !ok {
		t.Fatalf("expected first error to be DiagnosticWithPath; got %T", first)
	}
	want := path.Root(wantPath)
	if !withPath.Path().Equal(want) {
		t.Errorf("diagnostic path mismatch: got %s, want %s", withPath.Path(), want)
	}
}

// TestBuildNewInput_AllFieldsForwarded locks the gen 2 + all-optionals
// happy path.
func TestBuildNewInput_AllFieldsForwarded(t *testing.T) {
	t.Parallel()

	plan := Model{
		Name:       types.StringValue("vm01"),
		Generation: types.Int64Value(2),
		CPU:        &CPUModel{Count: types.Int64Value(4)},
		Memory:     &MemoryModel{StartupBytes: types.Int64Value(8589934592)},
		SecureBoot: types.BoolValue(true),
		Notes:      types.StringValue("production"),
	}
	in := buildNewInput(plan)

	if in.Name != "vm01" || in.Generation != 2 || in.Vcpu != 4 || in.MemoryBytes != 8589934592 {
		t.Errorf("required fields wrong: %+v", in)
	}
	if in.SecureBoot == nil || *in.SecureBoot != true {
		t.Errorf("SecureBoot = %v, want pointer to true", in.SecureBoot)
	}
	if in.Notes == nil || *in.Notes != "production" {
		t.Errorf("Notes = %v, want pointer to \"production\"", in.Notes)
	}
}

// TestBuildNewInput_OmitsNullOptionals confirms nil-pointer optionals
// drop out of the wire payload (omitempty + nil pointer).
func TestBuildNewInput_OmitsNullOptionals(t *testing.T) {
	t.Parallel()

	plan := Model{
		Name:       types.StringValue("legacy-vm"),
		Generation: types.Int64Value(1),
		CPU:        &CPUModel{Count: types.Int64Value(1)},
		Memory:     &MemoryModel{StartupBytes: types.Int64Value(2147483648)},
		SecureBoot: types.BoolNull(),
		Notes:      types.StringNull(),
	}
	in := buildNewInput(plan)

	if in.SecureBoot != nil {
		t.Errorf("SecureBoot should be nil for null plan value; got %v", in.SecureBoot)
	}
	if in.Notes != nil {
		t.Errorf("Notes should be nil for null plan value; got %v", in.Notes)
	}
}

// TestBuildSetInput_OnlyChangedFieldsForwarded is the load-bearing test
// for the partial-update optimization. Set-VMMemory / Set-VMProcessor
// validate state by parameter set, not value -- a no-op call to either
// on a running VM errors. Only-changed-fields-forwarded keeps Update
// from triggering false alarms.
func TestBuildSetInput_OnlyChangedFieldsForwarded(t *testing.T) {
	t.Parallel()

	state := Model{
		Name:       types.StringValue("vm01"),
		Generation: types.Int64Value(2),
		CPU:        &CPUModel{Count: types.Int64Value(2)},
		Memory:     &MemoryModel{StartupBytes: types.Int64Value(4294967296)},
		SecureBoot: types.BoolValue(true),
		Notes:      types.StringValue("old"),
	}
	plan := state                         // start identical...
	plan.Notes = types.StringValue("new") // ...change just notes

	in := buildSetInput(plan, state)

	if in.Vcpu != nil {
		t.Error("Vcpu should be omitted when unchanged")
	}
	if in.MemoryBytes != nil {
		t.Error("MemoryBytes should be omitted when unchanged")
	}
	if in.SecureBoot != nil {
		t.Error("SecureBoot should be omitted when unchanged")
	}
	if in.Notes == nil || *in.Notes != "new" {
		t.Errorf("Notes should be forwarded with the new value; got %v", in.Notes)
	}
}

// TestBuildSetInput_GenerationSourcedFromState pins the script-side
// SecureBoot guard hint -- generation is RequiresReplace, so plan and
// state always agree, but sourcing from state matches the convention.
func TestBuildSetInput_GenerationSourcedFromState(t *testing.T) {
	t.Parallel()

	// CPU and Memory are *CPUModel/*MemoryModel pointers per the
	// import-time null requirement; tests must populate them since
	// buildSetInput dereferences both unconditionally (the schema's
	// Required guarantee makes that safe in production but not in
	// hand-built test literals).
	state := Model{
		Name:       types.StringValue("vm01"),
		Generation: types.Int64Value(2),
		CPU:        &CPUModel{Count: types.Int64Value(2)},
		Memory:     &MemoryModel{StartupBytes: types.Int64Value(4294967296)},
	}
	plan := state
	plan.CPU = &CPUModel{Count: types.Int64Value(4)}

	in := buildSetInput(plan, state)
	if in.Generation != 2 {
		t.Errorf("Generation = %d, want 2 (sourced from state)", in.Generation)
	}
}

// TestModelFromVM_Gen2HasSecureBoot confirms the *bool wire decode
// becomes a typed bool in state for gen 2.
func TestModelFromVM_Gen2HasSecureBoot(t *testing.T) {
	t.Parallel()

	secureBoot := true
	got := modelFromVM(&hyperv.VM{
		Name:               "vm01",
		Generation:         2,
		ProcessorCount:     2,
		MemoryStartupBytes: 4294967296,
		State:              "Off",
		SecureBootEnabled:  &secureBoot,
	})

	if got.SecureBoot.IsNull() {
		t.Error("SecureBoot should not be null for gen 2 with non-nil pointer")
	}
	if !got.SecureBoot.ValueBool() {
		t.Error("SecureBoot should be true")
	}
}

// TestModelFromVM_Gen1SecureBootIsNull confirms gen 1 (where the wire
// returns null) maps to types.BoolNull() -- so the schema's
// Optional+Computed semantics work cleanly on gen 1.
func TestModelFromVM_Gen1SecureBootIsNull(t *testing.T) {
	t.Parallel()

	got := modelFromVM(&hyperv.VM{
		Name:              "legacy-vm",
		Generation:        1,
		SecureBootEnabled: nil,
	})

	if !got.SecureBoot.IsNull() {
		t.Errorf("SecureBoot = %v, want null for gen 1", got.SecureBoot)
	}
}

// TestModelFromVM_EmptyNotesBecomesNull confirms the empty-vs-null
// collapse for Notes. Without this, omitting `notes` from config would
// produce a phantom diff every plan (config null vs state "").
func TestModelFromVM_EmptyNotesBecomesNull(t *testing.T) {
	t.Parallel()

	got := modelFromVM(&hyperv.VM{
		Name:  "vm01",
		Notes: "",
	})
	if !got.Notes.IsNull() {
		t.Errorf("Notes = %v, want null when host returns empty string", got.Notes)
	}
}

// TestModelFromVM_NonEmptyNotesPreserved confirms the collapse only
// applies to empty -- a real notes value round-trips verbatim.
func TestModelFromVM_NonEmptyNotesPreserved(t *testing.T) {
	t.Parallel()

	got := modelFromVM(&hyperv.VM{
		Name:  "vm01",
		Notes: "production cluster",
	})
	if got.Notes.ValueString() != "production cluster" {
		t.Errorf("Notes = %q, want preserved", got.Notes.ValueString())
	}
}

// TestSetInputHasChanges is the seam Update uses to skip needless host
// round-trips when the framework re-ran Update for a Computed-only diff
// (e.g., an out-of-band `state` change picked up at refresh). Without
// the short-circuit, every refresh-driven Update would fire two Get-VM
// calls (existence pre-check + read-back) for nothing.
func TestSetInputHasChanges(t *testing.T) {
	t.Parallel()

	intVal := 4
	int64Val := int64(8589934592)
	boolVal := true
	stringVal := "updated"

	cases := []struct {
		name string
		in   hyperv.SetVMInput
		want bool
	}{
		{
			name: "no mutable fields populated -> no changes (Name/Generation alone)",
			in:   hyperv.SetVMInput{Name: "vm01", Generation: 2},
			want: false,
		},
		{
			name: "vcpu set -> has changes",
			in:   hyperv.SetVMInput{Name: "vm01", Generation: 2, Vcpu: &intVal},
			want: true,
		},
		{
			name: "memory_bytes set -> has changes",
			in:   hyperv.SetVMInput{Name: "vm01", Generation: 2, MemoryBytes: &int64Val},
			want: true,
		},
		{
			name: "secure_boot set -> has changes",
			in:   hyperv.SetVMInput{Name: "vm01", Generation: 2, SecureBoot: &boolVal},
			want: true,
		},
		{
			name: "notes set -> has changes",
			in:   hyperv.SetVMInput{Name: "vm01", Generation: 2, Notes: &stringVal},
			want: true,
		},
		{
			name: "all four set -> has changes",
			in: hyperv.SetVMInput{
				Name: "vm01", Generation: 2,
				Vcpu: &intVal, MemoryBytes: &int64Val, SecureBoot: &boolVal, Notes: &stringVal,
			},
			want: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := setInputHasChanges(tc.in); got != tc.want {
				t.Errorf("setInputHasChanges(%+v) = %v, want %v", tc.in, got, tc.want)
			}
		})
	}
}

// TestModelFromVM_PreservesInt64MemoryAndProcessorCount round-trips the
// numeric fields without precision loss for multi-GiB/multi-vcpu VMs.
func TestModelFromVM_PreservesInt64MemoryAndProcessorCount(t *testing.T) {
	t.Parallel()

	got := modelFromVM(&hyperv.VM{
		Name:               "vm01",
		ProcessorCount:     16,
		MemoryStartupBytes: 68719476736, // 64 GiB
	})
	if got.CPU.Count.ValueInt64() != 16 {
		t.Errorf("CPU.Count = %d, want 16", got.CPU.Count.ValueInt64())
	}
	if got.Memory.StartupBytes.ValueInt64() != 68719476736 {
		t.Errorf("Memory.StartupBytes = %d, want 68719476736", got.Memory.StartupBytes.ValueInt64())
	}
}

// TestDiffHardDiskDrives_AddsRemovesAndPathSwaps exercises the slot-keyed
// diff that drives Update reconciliation. The five cases below are the
// only ones that matter:
//   - new slot: attach
//   - removed slot: detach
//   - same slot, same path: no-op
//   - same slot, different path: detach + attach (path swap)
//   - same slot, slash-style-different path (under StringSemanticEquals):
//     no-op (the custom type folds C:/foo and C:\foo)
func TestDiffHardDiskDrives_AddsRemovesAndPathSwaps(t *testing.T) {
	t.Parallel()

	hdd := func(p, ct string, n, l int64) HardDiskDriveModel {
		return HardDiskDriveModel{
			Path:               pathtype.NewPathValue(p),
			ControllerType:     types.StringValue(ct),
			ControllerNumber:   types.Int64Value(n),
			ControllerLocation: types.Int64Value(l),
		}
	}

	t.Run("new slot in plan -> attach", func(t *testing.T) {
		t.Parallel()
		add, rm := diffHardDiskDrives(
			[]HardDiskDriveModel{hdd("C:\\a.vhdx", "SCSI", 0, 0)},
			nil,
		)
		if len(add) != 1 || len(rm) != 0 {
			t.Errorf("got attach=%d detach=%d, want 1/0", len(add), len(rm))
		}
	})

	t.Run("removed slot -> detach", func(t *testing.T) {
		t.Parallel()
		add, rm := diffHardDiskDrives(
			nil,
			[]HardDiskDriveModel{hdd("C:\\a.vhdx", "SCSI", 0, 0)},
		)
		if len(add) != 0 || len(rm) != 1 {
			t.Errorf("got attach=%d detach=%d, want 0/1", len(add), len(rm))
		}
	})

	t.Run("same slot same path -> no-op", func(t *testing.T) {
		t.Parallel()
		h := hdd("C:\\a.vhdx", "SCSI", 0, 0)
		add, rm := diffHardDiskDrives([]HardDiskDriveModel{h}, []HardDiskDriveModel{h})
		if len(add) != 0 || len(rm) != 0 {
			t.Errorf("got attach=%d detach=%d, want 0/0", len(add), len(rm))
		}
	})

	t.Run("same slot, different path -> detach + attach", func(t *testing.T) {
		t.Parallel()
		add, rm := diffHardDiskDrives(
			[]HardDiskDriveModel{hdd("C:\\new.vhdx", "SCSI", 0, 0)},
			[]HardDiskDriveModel{hdd("C:\\old.vhdx", "SCSI", 0, 0)},
		)
		if len(add) != 1 || len(rm) != 1 {
			t.Errorf("got attach=%d detach=%d, want 1/1", len(add), len(rm))
		}
		if add[0].Path.ValueString() != "C:\\new.vhdx" {
			t.Errorf("attach path = %q, want C:\\new.vhdx", add[0].Path.ValueString())
		}
		if rm[0].Path.ValueString() != "C:\\old.vhdx" {
			t.Errorf("detach path = %q, want C:\\old.vhdx", rm[0].Path.ValueString())
		}
	})

	t.Run("same slot, slash-style differs -> no-op (semantic equals)", func(t *testing.T) {
		t.Parallel()
		// pathtype.Path's StringSemanticEquals folds slash style;
		// without that, the change here would falsely look like a
		// path swap and trigger detach+attach on every plan.
		add, rm := diffHardDiskDrives(
			[]HardDiskDriveModel{hdd("C:/foo/disk.vhdx", "SCSI", 0, 0)},
			[]HardDiskDriveModel{hdd("C:\\foo\\disk.vhdx", "SCSI", 0, 0)},
		)
		if len(add) != 0 || len(rm) != 0 {
			t.Errorf("got attach=%d detach=%d, want 0/0 (slash-style differences must not trigger detach+attach)", len(add), len(rm))
		}
	})
}

// TestAttachInputFor_DefaultsControllerTypeToSCSI pins the schema-default
// behaviour: a HardDiskDriveModel with null ControllerType (the
// schema's StaticString default kicks in at plan time, but defensive
// guards in attachInputFor handle the unknown-during-plan-modification
// case too) still produces a valid AttachHardDiskInput targeting SCSI.
func TestAttachInputFor_DefaultsControllerTypeToSCSI(t *testing.T) {
	t.Parallel()

	h := HardDiskDriveModel{
		Path:               pathtype.NewPathValue("C:\\a.vhdx"),
		ControllerType:     types.StringNull(),
		ControllerNumber:   types.Int64Value(0),
		ControllerLocation: types.Int64Value(0),
	}
	got := attachInputFor("vm01", h)
	if got.ControllerType != "SCSI" {
		t.Errorf("ControllerType = %q, want SCSI", got.ControllerType)
	}
	if got.Name != "vm01" || got.Path != "C:\\a.vhdx" {
		t.Errorf("attach input round-trip wrong: %+v", got)
	}
}

// TestDetachInputFor_OmitsPath confirms the wire payload for a detach
// doesn't carry path -- the slot tuple alone identifies the
// attachment, and DetachHardDiskInput is intentionally pathless to
// match the cmdlet's contract.
func TestDetachInputFor_OmitsPath(t *testing.T) {
	t.Parallel()

	h := HardDiskDriveModel{
		Path:               pathtype.NewPathValue("C:\\a.vhdx"),
		ControllerType:     types.StringValue("SCSI"),
		ControllerNumber:   types.Int64Value(0),
		ControllerLocation: types.Int64Value(0),
	}
	got := detachInputFor("vm01", h)
	if got.Name != "vm01" || got.ControllerType != "SCSI" {
		t.Errorf("detach input wrong: %+v", got)
	}
	// Compile-time check: DetachHardDiskInput has no Path field. If
	// someone adds one, this test still passes -- but its existence
	// is the schema-level invariant we care about; the JSON-tag
	// pinning test in internal/hyperv/vm_test.go enforces wire shape.
}

// TestModelFromVM_PopulatesHardDiskDrives confirms the cmdlet's
// HardDiskDrives slice round-trips into the framework's HardDiskDriveModel
// list, with Path going through pathtype.Path.
func TestModelFromVM_PopulatesHardDiskDrives(t *testing.T) {
	t.Parallel()

	got := modelFromVM(&hyperv.VM{
		Name:       "vm01",
		Generation: 2,
		HardDiskDrives: []hyperv.HardDiskDrive{
			{Path: "C:\\a.vhdx", ControllerType: "SCSI", ControllerNumber: 0, ControllerLocation: 0},
			{Path: "C:\\b.vhdx", ControllerType: "SCSI", ControllerNumber: 0, ControllerLocation: 1},
		},
	})
	if len(got.HardDiskDrives) != 2 {
		t.Fatalf("len(HardDiskDrives) = %d, want 2", len(got.HardDiskDrives))
	}
	if got.HardDiskDrives[0].Path.ValueString() != "C:\\a.vhdx" {
		t.Errorf("first HDD path = %q, want C:\\a.vhdx", got.HardDiskDrives[0].Path.ValueString())
	}
	if got.HardDiskDrives[1].ControllerLocation.ValueInt64() != 1 {
		t.Errorf("second HDD location = %d, want 1", got.HardDiskDrives[1].ControllerLocation.ValueInt64())
	}
}

// TestModelFromVM_EmptyHardDiskDrivesIsEmptySlice locks the script-side
// @() wrapper guarantee on the Go side: an empty cmdlet result becomes
// an empty (but non-nil) slice in the model. Nil here would make the
// framework treat the attribute as null/unset, triggering a phantom
// diff against a config that explicitly writes hard_disk_drive = [].
func TestModelFromVM_EmptyHardDiskDrivesIsEmptySlice(t *testing.T) {
	t.Parallel()

	got := modelFromVM(&hyperv.VM{
		Name:           "vm01",
		Generation:     2,
		HardDiskDrives: []hyperv.HardDiskDrive{},
	})
	if got.HardDiskDrives == nil {
		t.Error("HardDiskDrives = nil, want empty []HardDiskDriveModel")
	}
	if len(got.HardDiskDrives) != 0 {
		t.Errorf("HardDiskDrives length = %d, want 0", len(got.HardDiskDrives))
	}
}

// TestNetworkAdapterUniqueNamesValidator pins the plan-time uniqueness
// check on network_adapter[].name. Hyper-V allows duplicate-named NICs
// at the cmdlet level; the validator is what makes the slot key
// well-defined for our diff logic.
func TestNetworkAdapterUniqueNamesValidator(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name      string
		nics      []NetworkAdapterModel
		wantError bool
	}{
		{
			name: "two NICs with distinct names -> ok",
			nics: []NetworkAdapterModel{
				{Name: types.StringValue("primary"), SwitchName: types.StringValue("a")},
				{Name: types.StringValue("secondary"), SwitchName: types.StringValue("b")},
			},
		},
		{
			name: "two NICs sharing the same name -> fires",
			nics: []NetworkAdapterModel{
				{Name: types.StringValue("primary"), SwitchName: types.StringValue("a")},
				{Name: types.StringValue("primary"), SwitchName: types.StringValue("b")},
			},
			wantError: true,
		},
		{
			name: "single NIC -> ok (trivially unique)",
			nics: []NetworkAdapterModel{
				{Name: types.StringValue("only"), SwitchName: types.StringValue("a")},
			},
		},
		{
			name: "empty list -> ok",
			nics: []NetworkAdapterModel{},
		},
		{
			name: "unknown name in second slot -> skip (deferred dep)",
			nics: []NetworkAdapterModel{
				{Name: types.StringValue("primary"), SwitchName: types.StringValue("a")},
				{Name: types.StringUnknown(), SwitchName: types.StringValue("b")},
			},
		},
	}
	v := networkAdapterUniqueNamesValidator{}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			diags := v.validate(Model{NetworkAdapters: tc.nics})
			if got := diags.HasError(); got != tc.wantError {
				t.Errorf("HasError = %v, want %v; diags: %v", got, tc.wantError, diags)
			}
		})
	}
}

// TestDiffNetworkAdapters mirrors the HDD diff test shape: same name +
// same switch is no-op; same name + different switch is detach +
// attach; new name is attach; removed name is detach.
func TestDiffNetworkAdapters(t *testing.T) {
	t.Parallel()

	nic := func(name, sw string) NetworkAdapterModel {
		return NetworkAdapterModel{
			Name:       types.StringValue(name),
			SwitchName: types.StringValue(sw),
		}
	}

	t.Run("new name -> attach", func(t *testing.T) {
		t.Parallel()
		add, rm := diffNetworkAdapters([]NetworkAdapterModel{nic("a", "lab")}, nil)
		if len(add) != 1 || len(rm) != 0 {
			t.Errorf("got attach=%d detach=%d, want 1/0", len(add), len(rm))
		}
	})
	t.Run("removed name -> detach", func(t *testing.T) {
		t.Parallel()
		add, rm := diffNetworkAdapters(nil, []NetworkAdapterModel{nic("a", "lab")})
		if len(add) != 0 || len(rm) != 1 {
			t.Errorf("got attach=%d detach=%d, want 0/1", len(add), len(rm))
		}
	})
	t.Run("same name same switch -> no-op", func(t *testing.T) {
		t.Parallel()
		n := nic("a", "lab")
		add, rm := diffNetworkAdapters([]NetworkAdapterModel{n}, []NetworkAdapterModel{n})
		if len(add) != 0 || len(rm) != 0 {
			t.Errorf("got attach=%d detach=%d, want 0/0", len(add), len(rm))
		}
	})
	t.Run("same name different switch -> detach + attach", func(t *testing.T) {
		t.Parallel()
		add, rm := diffNetworkAdapters(
			[]NetworkAdapterModel{nic("a", "new-switch")},
			[]NetworkAdapterModel{nic("a", "old-switch")},
		)
		if len(add) != 1 || len(rm) != 1 {
			t.Errorf("got attach=%d detach=%d, want 1/1", len(add), len(rm))
		}
		if add[0].SwitchName.ValueString() != "new-switch" {
			t.Errorf("attach switch = %q, want new-switch", add[0].SwitchName.ValueString())
		}
		if rm[0].SwitchName.ValueString() != "old-switch" {
			t.Errorf("detach switch = %q, want old-switch", rm[0].SwitchName.ValueString())
		}
	})
}

// TestModelFromVM_PopulatesNetworkAdapters confirms the cmdlet's NIC
// list round-trips through modelFromVM, sorted by Name.
func TestModelFromVM_PopulatesNetworkAdapters(t *testing.T) {
	t.Parallel()

	got := modelFromVM(&hyperv.VM{
		Name:       "vm01",
		Generation: 2,
		NetworkAdapters: []hyperv.NetworkAdapter{
			// Cmdlet emits in some order; modelFromVM sorts by Name.
			{Name: "secondary", SwitchName: "lab-external"},
			{Name: "primary", SwitchName: "lab-internal"},
		},
	})
	if len(got.NetworkAdapters) != 2 {
		t.Fatalf("got %d NICs, want 2", len(got.NetworkAdapters))
	}
	// After sort, primary comes first.
	if got.NetworkAdapters[0].Name.ValueString() != "primary" {
		t.Errorf("first NIC name = %q, want primary (sorted)", got.NetworkAdapters[0].Name.ValueString())
	}
	if got.NetworkAdapters[1].SwitchName.ValueString() != "lab-external" {
		t.Errorf("second NIC switch = %q, want lab-external", got.NetworkAdapters[1].SwitchName.ValueString())
	}
}
