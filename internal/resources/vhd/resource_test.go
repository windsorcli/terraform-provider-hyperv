package vhd

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
// vswitch / image_file resource tests use.
func hasPlanModifier[M any](mods []M, keyword string) bool {
	for _, pm := range mods {
		if strings.Contains(strings.ToLower(reflect.TypeOf(pm).String()), strings.ToLower(keyword)) {
			return true
		}
	}
	return false
}

// Schema test: every locked-in attribute is present.
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
		"path",
		"vhd_type",
		"size_bytes",
		"parent_path",
		"block_size_bytes",
		"file_size_bytes",
		"format",
		"attached",
	}
	for _, name := range wantAttrs {
		if _, ok := resp.Schema.Attributes[name]; !ok {
			t.Errorf("missing attribute %q", name)
		}
	}
}

// Immutable attributes carry RequiresReplace. path/vhd_type/parent_path/
// block_size_bytes are all immutable; size_bytes is the only in-place
// mutation (Resize-VHD).
func TestResource_Schema_RequiresReplaceOnImmutableAttrs(t *testing.T) {
	t.Parallel()

	r := New()
	resp := &resource.SchemaResponse{}
	r.Schema(t.Context(), resource.SchemaRequest{}, resp)

	for _, name := range []string{"path", "vhd_type", "parent_path"} {
		raw, ok := resp.Schema.Attributes[name]
		if !ok {
			t.Fatalf("missing attribute %q", name)
		}
		strAttr, ok := raw.(schema.StringAttribute)
		if !ok {
			t.Errorf("%q is not a StringAttribute (got %T)", name, raw)
			continue
		}
		if !hasPlanModifier(strAttr.PlanModifiers, "RequiresReplace") {
			t.Errorf("%q must carry RequiresReplace", name)
		}
	}

	if intAttr, ok := resp.Schema.Attributes["block_size_bytes"].(schema.Int64Attribute); ok {
		if !hasPlanModifier(intAttr.PlanModifiers, "RequiresReplace") {
			t.Error(`"block_size_bytes" must carry RequiresReplace`)
		}
	} else {
		t.Errorf(`"block_size_bytes" missing or wrong type`)
	}
}

// size_bytes is the in-place mutation -- it must NOT carry RequiresReplace.
func TestResource_Schema_SizeBytesIsInPlaceMutable(t *testing.T) {
	t.Parallel()

	r := New()
	resp := &resource.SchemaResponse{}
	r.Schema(t.Context(), resource.SchemaRequest{}, resp)

	intAttr, ok := resp.Schema.Attributes["size_bytes"].(schema.Int64Attribute)
	if !ok {
		t.Fatalf("size_bytes is not an Int64Attribute (got %T)", resp.Schema.Attributes["size_bytes"])
	}
	if hasPlanModifier(intAttr.PlanModifiers, "RequiresReplace") {
		t.Error(`"size_bytes" must NOT carry RequiresReplace -- Resize-VHD is the in-place path`)
	}
}

// Computed attrs carry UseStateForUnknown so plans don't show phantom
// (known after apply) diffs when nothing relevant changed.
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
	checkString("format")

	checkInt := func(attrName string) {
		raw, ok := resp.Schema.Attributes[attrName]
		if !ok {
			t.Fatalf("missing attribute %q", attrName)
		}
		intAttr, ok := raw.(schema.Int64Attribute)
		if !ok {
			t.Fatalf("%q is not an Int64Attribute (got %T)", attrName, raw)
		}
		if !hasPlanModifier(intAttr.PlanModifiers, "UseStateForUnknown") {
			t.Errorf("%q must carry UseStateForUnknown", attrName)
		}
	}
	checkInt("size_bytes")
	checkInt("block_size_bytes")
	checkInt("file_size_bytes")

	if boolAttr, ok := resp.Schema.Attributes["attached"].(schema.BoolAttribute); ok {
		if !hasPlanModifier(boolAttr.PlanModifiers, "UseStateForUnknown") {
			t.Error(`"attached" must carry UseStateForUnknown`)
		}
	} else {
		t.Errorf(`"attached" missing or wrong type`)
	}
}

// vhd_type accepts only fixed/dynamic/differencing.
func TestResource_Schema_VhdTypeOneOf(t *testing.T) {
	t.Parallel()

	r := New()
	resp := &resource.SchemaResponse{}
	r.Schema(t.Context(), resource.SchemaRequest{}, resp)

	strAttr, ok := resp.Schema.Attributes["vhd_type"].(schema.StringAttribute)
	if !ok {
		t.Fatalf("vhd_type is not a StringAttribute (got %T)", resp.Schema.Attributes["vhd_type"])
	}
	if len(strAttr.Validators) == 0 {
		t.Fatal("vhd_type must carry at least one validator (OneOf fixed/dynamic/differencing)")
	}
	// The validator's Description() exposes the configured set; compare
	// against the literal expected list. Lowercase mirrors the schema's
	// chosen casing (the wire-stdin contract for new.ps1).
	desc := strAttr.Validators[0].Description(t.Context())
	for _, want := range []string{"fixed", "dynamic", "differencing"} {
		if !strings.Contains(desc, want) {
			t.Errorf("OneOf description should mention %q; got %q", want, desc)
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
	if resp.TypeName != "hyperv_vhd" {
		t.Errorf("TypeName = %q, want %q", resp.TypeName, "hyperv_vhd")
	}
}

// Configure with nil ProviderData (validate-time invocation) must NOT
// panic and must NOT error.
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

// TestResource_ConfigValidators_RegistersAll confirms all three
// cross-attribute checks are wired in. The validate() exercises that
// follow lock the actual rule behavior for each.
func TestResource_ConfigValidators_RegistersAll(t *testing.T) {
	t.Parallel()

	r, ok := New().(*Resource)
	if !ok {
		t.Fatal("New() did not return *Resource")
	}
	got := r.ConfigValidators(t.Context())
	if len(got) != 3 {
		t.Fatalf("got %d ConfigValidators, want 3 (parent_path, size_bytes, block_size_bytes)", len(got))
	}
}

// TestParentPathValidator exercises the symmetric rule: parent_path must
// be set IFF vhd_type=differencing. Cases cover both fire directions and
// both unknown-skip cases.
func TestParentPathValidator(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name      string
		model     Model
		wantError bool
		wantPath  string
	}{
		{
			name: "differencing with parent_path -> ok",
			model: Model{
				VhdType:    types.StringValue("differencing"),
				ParentPath: types.StringValue("C:\\parent.vhdx"),
			},
		},
		{
			name: "fixed without parent_path -> ok",
			model: Model{
				VhdType:    types.StringValue("fixed"),
				ParentPath: types.StringNull(),
			},
		},
		{
			name: "differencing without parent_path -> fires (required)",
			model: Model{
				VhdType:    types.StringValue("differencing"),
				ParentPath: types.StringNull(),
			},
			wantError: true,
			wantPath:  "parent_path",
		},
		{
			name: "fixed with parent_path -> fires (rejected)",
			model: Model{
				VhdType:    types.StringValue("fixed"),
				ParentPath: types.StringValue("C:\\parent.vhdx"),
			},
			wantError: true,
			wantPath:  "parent_path",
		},
		{
			name: "differencing with empty-string parent_path -> fires (treated as unset)",
			model: Model{
				VhdType:    types.StringValue("differencing"),
				ParentPath: types.StringValue(""),
			},
			wantError: true,
			wantPath:  "parent_path",
		},
		{
			name: "vhd_type unknown -> skip (deferred dep)",
			model: Model{
				VhdType:    types.StringUnknown(),
				ParentPath: types.StringValue("C:\\parent.vhdx"),
			},
		},
		{
			name: "parent_path unknown -> skip (deferred dep)",
			model: Model{
				VhdType:    types.StringValue("differencing"),
				ParentPath: types.StringUnknown(),
			},
		},
	}
	v := parentPathRequiresDifferencingValidator{}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			diags := v.validate(tc.model)
			assertValidatorDiags(t, diags, tc.wantError, tc.wantPath)
		})
	}
}

// TestSizeBytesValidator exercises the symmetric rule: size_bytes must be
// set IFF vhd_type in (fixed, dynamic). Cases cover both fire directions
// and both unknown-skip cases.
func TestSizeBytesValidator(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name      string
		model     Model
		wantError bool
		wantPath  string
	}{
		{
			name: "fixed with size_bytes -> ok",
			model: Model{
				VhdType:   types.StringValue("fixed"),
				SizeBytes: types.Int64Value(1073741824),
			},
		},
		{
			name: "dynamic with size_bytes -> ok",
			model: Model{
				VhdType:   types.StringValue("dynamic"),
				SizeBytes: types.Int64Value(34359738368),
			},
		},
		{
			name: "differencing without size_bytes -> ok (inherited from parent)",
			model: Model{
				VhdType:   types.StringValue("differencing"),
				SizeBytes: types.Int64Null(),
			},
		},
		{
			name: "fixed without size_bytes -> fires (required)",
			model: Model{
				VhdType:   types.StringValue("fixed"),
				SizeBytes: types.Int64Null(),
			},
			wantError: true,
			wantPath:  "size_bytes",
		},
		{
			name: "differencing with size_bytes -> fires (rejected)",
			model: Model{
				VhdType:   types.StringValue("differencing"),
				SizeBytes: types.Int64Value(1073741824),
			},
			wantError: true,
			wantPath:  "size_bytes",
		},
		{
			name: "vhd_type unknown -> skip (deferred dep)",
			model: Model{
				VhdType:   types.StringUnknown(),
				SizeBytes: types.Int64Value(1073741824),
			},
		},
		{
			name: "size_bytes unknown -> skip (deferred dep)",
			model: Model{
				VhdType:   types.StringValue("fixed"),
				SizeBytes: types.Int64Unknown(),
			},
		},
	}
	v := sizeBytesRequiresFixedOrDynamicValidator{}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			diags := v.validate(tc.model)
			assertValidatorDiags(t, diags, tc.wantError, tc.wantPath)
		})
	}
}

// TestBlockSizeBytesValidator exercises the one-directional rule:
// block_size_bytes is rejected for vhd_type=differencing (where Hyper-V
// would silently drop the user's value, then re-detect it as drift on
// every subsequent plan, producing an infinite replace loop). Optional
// for fixed/dynamic.
func TestBlockSizeBytesValidator(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name      string
		model     Model
		wantError bool
		wantPath  string
	}{
		{
			name: "fixed without block_size_bytes -> ok (Hyper-V default)",
			model: Model{
				VhdType:        types.StringValue("fixed"),
				BlockSizeBytes: types.Int64Null(),
			},
		},
		{
			name: "fixed with block_size_bytes -> ok",
			model: Model{
				VhdType:        types.StringValue("fixed"),
				BlockSizeBytes: types.Int64Value(33554432),
			},
		},
		{
			name: "dynamic with block_size_bytes -> ok",
			model: Model{
				VhdType:        types.StringValue("dynamic"),
				BlockSizeBytes: types.Int64Value(33554432),
			},
		},
		{
			name: "differencing without block_size_bytes -> ok (inherited)",
			model: Model{
				VhdType:        types.StringValue("differencing"),
				BlockSizeBytes: types.Int64Null(),
			},
		},
		{
			name: "differencing with block_size_bytes -> fires (would loop replace)",
			model: Model{
				VhdType:        types.StringValue("differencing"),
				BlockSizeBytes: types.Int64Value(33554432),
			},
			wantError: true,
			wantPath:  "block_size_bytes",
		},
		{
			name: "vhd_type unknown -> skip (deferred dep)",
			model: Model{
				VhdType:        types.StringUnknown(),
				BlockSizeBytes: types.Int64Value(33554432),
			},
		},
		{
			name: "block_size_bytes unknown -> skip (deferred dep)",
			model: Model{
				VhdType:        types.StringValue("differencing"),
				BlockSizeBytes: types.Int64Unknown(),
			},
		},
	}
	v := blockSizeBytesRejectedForDifferencingValidator{}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			diags := v.validate(tc.model)
			assertValidatorDiags(t, diags, tc.wantError, tc.wantPath)
		})
	}
}

// assertValidatorDiags is the shared assertion shape for all three
// validator-table tests. Verifies presence/absence of an error and, when
// expected, that the error is anchored to the right attribute path.
//
// "Anchored" means the diagnostic carries a path.Path attached via
// AddAttributeError -- that's what Terraform uses to highlight the
// offending line in plan output. Checking only the message text would
// pass a buggy validator that called AddAttributeError(path.Root("foo"))
// while writing "bar" in the message; the type assertion to
// diag.DiagnosticWithPath catches that mismatch.
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
		t.Fatalf("expected first error to be DiagnosticWithPath (i.e., from AddAttributeError); got %T", first)
	}
	want := path.Root(wantPath)
	if !withPath.Path().Equal(want) {
		t.Errorf("diagnostic path mismatch: got %s, want %s", withPath.Path(), want)
	}
}

// modelFromVHD lowercases vhd_type (Get-VHD emits PascalCase; the schema
// uses lowercase). Drift here means the schema's OneOf would reject the
// value the resource just wrote to state, breaking refresh.
func TestModelFromVHD_LowercasesVhdType(t *testing.T) {
	t.Parallel()

	cases := []struct {
		in, want string
	}{
		{"Fixed", "fixed"},
		{"Dynamic", "dynamic"},
		{"Differencing", "differencing"},
	}
	for _, tc := range cases {
		got := modelFromVHD(&hyperv.VHD{
			Path:    "C:\\vhds\\foo.vhdx",
			VhdType: tc.in,
		})
		if got.VhdType.ValueString() != tc.want {
			t.Errorf("modelFromVHD VhdType=%q -> %q, want %q",
				tc.in, got.VhdType.ValueString(), tc.want)
		}
	}
}

// modelFromVHD collapses an empty ParentPath to null so non-differencing
// disks don't carry a phantom empty string in state. Subsequent plans
// would compare config (null) against state (empty string) and report a
// phantom diff otherwise.
func TestModelFromVHD_EmptyParentPathBecomesNull(t *testing.T) {
	t.Parallel()

	got := modelFromVHD(&hyperv.VHD{
		Path:       "C:\\vhds\\dyn.vhdx",
		VhdType:    "Dynamic",
		ParentPath: "",
	})
	if !got.ParentPath.IsNull() {
		t.Errorf("ParentPath = %v, want null when source is empty", got.ParentPath)
	}
}

// modelFromVHD preserves a non-empty ParentPath verbatim for differencing
// disks -- this is the load-bearing field for Flow B (boot-from-cloud-image).
func TestModelFromVHD_DifferencingPreservesParentPath(t *testing.T) {
	t.Parallel()

	got := modelFromVHD(&hyperv.VHD{
		Path:       "C:\\vhds\\child.vhdx",
		VhdType:    "Differencing",
		ParentPath: "C:\\vhds\\parent.vhdx",
	})
	if got.ParentPath.ValueString() != "C:\\vhds\\parent.vhdx" {
		t.Errorf("ParentPath = %q, want preserved", got.ParentPath.ValueString())
	}
}

// modelFromVHD round-trips int64 fields without precision loss for
// multi-GiB VHDXs. A careless int32 decode would lose data above ~2 GiB.
func TestModelFromVHD_PreservesInt64Sizes(t *testing.T) {
	t.Parallel()

	got := modelFromVHD(&hyperv.VHD{
		Path:           "C:\\vhds\\big.vhdx",
		VhdType:        "Dynamic",
		SizeBytes:      53687091200, // 50 GiB
		FileSizeBytes:  21474836480, // 20 GiB sparse
		BlockSizeBytes: 33554432,
	})
	if got.SizeBytes.ValueInt64() != 53687091200 {
		t.Errorf("SizeBytes = %d, want 53687091200", got.SizeBytes.ValueInt64())
	}
	if got.FileSizeBytes.ValueInt64() != 21474836480 {
		t.Errorf("FileSizeBytes = %d, want 21474836480", got.FileSizeBytes.ValueInt64())
	}
	if got.BlockSizeBytes.ValueInt64() != 33554432 {
		t.Errorf("BlockSizeBytes = %d, want 33554432", got.BlockSizeBytes.ValueInt64())
	}
}

// optionalInt64 returns nil for null/unknown framework values so the
// typed client's *int64 + omitempty drops the field from the wire JSON.
func TestOptionalInt64(t *testing.T) {
	t.Parallel()

	if optionalInt64(types.Int64Null()) != nil {
		t.Error("Int64Null should map to nil pointer")
	}
	if optionalInt64(types.Int64Unknown()) != nil {
		t.Error("Int64Unknown should map to nil pointer")
	}
	got := optionalInt64(types.Int64Value(33554432))
	if got == nil || *got != 33554432 {
		t.Errorf("Int64Value(33554432) = %v, want pointer to 33554432", got)
	}
}
