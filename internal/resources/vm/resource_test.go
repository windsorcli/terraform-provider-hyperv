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

// TestResource_ConfigValidators_RegistersAll confirms the validator is
// wired into ConfigValidators().
func TestResource_ConfigValidators_RegistersAll(t *testing.T) {
	t.Parallel()

	r, ok := New().(*Resource)
	if !ok {
		t.Fatal("New() did not return *Resource")
	}
	got := r.ConfigValidators(t.Context())
	if len(got) != 1 {
		t.Fatalf("got %d ConfigValidators, want 1 (secure_boot rejected for gen 1)", len(got))
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
		CPU:        CPUModel{Count: types.Int64Value(4)},
		Memory:     MemoryModel{StartupBytes: types.Int64Value(8589934592)},
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
		CPU:        CPUModel{Count: types.Int64Value(1)},
		Memory:     MemoryModel{StartupBytes: types.Int64Value(2147483648)},
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
		CPU:        CPUModel{Count: types.Int64Value(2)},
		Memory:     MemoryModel{StartupBytes: types.Int64Value(4294967296)},
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

	state := Model{
		Name:       types.StringValue("vm01"),
		Generation: types.Int64Value(2),
	}
	plan := state
	plan.CPU = CPUModel{Count: types.Int64Value(4)}

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
