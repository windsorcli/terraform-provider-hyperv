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
	"github.com/windsorcli/terraform-provider-hyperv/internal/testutil"
	pathtype "github.com/windsorcli/terraform-provider-hyperv/internal/types/path"
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
	if len(got) != 4 {
		t.Fatalf("got %d ConfigValidators, want 4 (parent_path, size_bytes, block_size_bytes, source_path)", len(got))
	}
	if _, ok := got[3].(sourcePathModeValidator); !ok {
		t.Errorf("ConfigValidators[3] = %T, want sourcePathModeValidator", got[3])
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
				ParentPath: pathtype.NewPathValue("C:\\parent.vhdx"),
			},
		},
		{
			name: "fixed without parent_path -> ok",
			model: Model{
				VhdType:    types.StringValue("fixed"),
				ParentPath: pathtype.NewPathNull(),
			},
		},
		{
			name: "differencing without parent_path -> fires (required)",
			model: Model{
				VhdType:    types.StringValue("differencing"),
				ParentPath: pathtype.NewPathNull(),
			},
			wantError: true,
			wantPath:  "parent_path",
		},
		{
			name: "fixed with parent_path -> fires (rejected)",
			model: Model{
				VhdType:    types.StringValue("fixed"),
				ParentPath: pathtype.NewPathValue("C:\\parent.vhdx"),
			},
			wantError: true,
			wantPath:  "parent_path",
		},
		{
			name: "differencing with empty-string parent_path -> fires (treated as unset)",
			model: Model{
				VhdType:    types.StringValue("differencing"),
				ParentPath: pathtype.NewPathValue(""),
			},
			wantError: true,
			wantPath:  "parent_path",
		},
		{
			name: "vhd_type unknown -> skip (deferred dep)",
			model: Model{
				VhdType:    types.StringUnknown(),
				ParentPath: pathtype.NewPathValue("C:\\parent.vhdx"),
			},
		},
		{
			name: "parent_path unknown -> skip (deferred dep)",
			model: Model{
				VhdType:    types.StringValue("differencing"),
				ParentPath: pathtype.NewPathUnknown(),
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
		}, Model{})
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
	}, Model{})
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
	}, Model{})
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
	}, Model{})
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

// Case-preservation across Windows path canonicalization (Get-VHD
// returns "C:\..." for a config of "c:\..." -- uppercase drive letter,
// junction-point resolution, short-filename expansion) is no longer a
// resource-layer concern. The pathtype.Path custom type's
// StringSemanticEquals handles slash-style and case folding at the
// framework layer, so the previous preserveCaseOrNullify shim and the
// "preserves prior casing" tests against modelFromVHD are gone.
//
// Equivalent coverage now lives at:
//   internal/types/path/path_test.go::TestPath_StringSemanticEquals_equivalent
//   internal/types/path/path_test.go::TestPath_StringSemanticEquals_distinct

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

// Schema test: source_path and source_sha256 are present with the plan
// modifiers the mode depends on. source_path must force replacement (a
// different source is a different disk), and source_sha256 must be
// Computed-only -- users never set the hash themselves.
func TestResource_Schema_SourcePathAttrs(t *testing.T) {
	t.Parallel()

	r := New()
	resp := &resource.SchemaResponse{}
	r.Schema(t.Context(), resource.SchemaRequest{}, resp)

	sp, ok := resp.Schema.Attributes["source_path"].(schema.StringAttribute)
	if !ok {
		t.Fatalf("source_path is not a StringAttribute (got %T)", resp.Schema.Attributes["source_path"])
	}
	if !sp.Optional {
		t.Error(`"source_path" must be Optional`)
	}
	if sp.CustomType != pathtype.Type {
		t.Errorf("source_path CustomType = %v, want pathtype.Type", sp.CustomType)
	}
	if !hasPlanModifier(sp.PlanModifiers, "RequiresReplace") {
		t.Error(`"source_path" must carry RequiresReplace`)
	}

	sha, ok := resp.Schema.Attributes["source_sha256"].(schema.StringAttribute)
	if !ok {
		t.Fatalf("source_sha256 is not a StringAttribute (got %T)", resp.Schema.Attributes["source_sha256"])
	}
	if !sha.Computed || sha.Optional || sha.Required {
		t.Error(`"source_sha256" must be Computed-only`)
	}
}

// vhd_type had to relax from Required to Optional+Computed so source_path
// mode can omit it. The framework no longer enforces presence, so
// sourcePathModeValidator does -- this pins the schema half of that pair.
func TestResource_Schema_VhdTypeOptionalForSourcePathMode(t *testing.T) {
	t.Parallel()

	r := New()
	resp := &resource.SchemaResponse{}
	r.Schema(t.Context(), resource.SchemaRequest{}, resp)

	vt, ok := resp.Schema.Attributes["vhd_type"].(schema.StringAttribute)
	if !ok {
		t.Fatalf("vhd_type is not a StringAttribute (got %T)", resp.Schema.Attributes["vhd_type"])
	}
	if vt.Required {
		t.Error(`"vhd_type" must not be Required -- source_path mode inherits the layout from the source`)
	}
	if !vt.Optional || !vt.Computed {
		t.Error(`"vhd_type" must be Optional+Computed`)
	}
	if !hasPlanModifier(vt.PlanModifiers, "RequiresReplace") {
		t.Error(`"vhd_type" must still carry RequiresReplace`)
	}
}

// TestSourcePathModeValidator covers the rules the mode owns: the three
// layout attributes are inherited and rejected, vhd_type is required
// without source_path, and source must differ from destination.
func TestSourcePathModeValidator(t *testing.T) {
	t.Parallel()

	const src = "D:/images/fcos.vhdx"
	const dst = "C:/vms/cp1/boot.vhdx"

	cases := []struct {
		name      string
		model     Model
		wantError bool
		wantMatch string
	}{
		{
			name: "source_path alone allows",
			model: Model{
				Path: pathtype.NewPathValue(dst), SourcePath: pathtype.NewPathValue(src),
				VhdType: types.StringNull(), ParentPath: pathtype.NewPathNull(),
				BlockSizeBytes: types.Int64Null(),
			},
		},
		{
			name: "source_path with size_bytes allows (grow after copy)",
			model: Model{
				Path: pathtype.NewPathValue(dst), SourcePath: pathtype.NewPathValue(src),
				VhdType: types.StringNull(), ParentPath: pathtype.NewPathNull(),
				BlockSizeBytes: types.Int64Null(), SizeBytes: types.Int64Value(21474836480),
			},
		},
		{
			name: "source_path with vhd_type rejects",
			model: Model{
				Path: pathtype.NewPathValue(dst), SourcePath: pathtype.NewPathValue(src),
				VhdType: types.StringValue("dynamic"), ParentPath: pathtype.NewPathNull(),
				BlockSizeBytes: types.Int64Null(),
			},
			wantError: true, wantMatch: "vhd_type is not valid with source_path",
		},
		{
			name: "source_path with parent_path rejects",
			model: Model{
				Path: pathtype.NewPathValue(dst), SourcePath: pathtype.NewPathValue(src),
				VhdType: types.StringNull(), ParentPath: pathtype.NewPathValue("C:/base.vhdx"),
				BlockSizeBytes: types.Int64Null(),
			},
			wantError: true, wantMatch: "parent_path is not valid with source_path",
		},
		{
			name: "source_path with block_size_bytes rejects",
			model: Model{
				Path: pathtype.NewPathValue(dst), SourcePath: pathtype.NewPathValue(src),
				VhdType: types.StringNull(), ParentPath: pathtype.NewPathNull(),
				BlockSizeBytes: types.Int64Value(33554432),
			},
			wantError: true, wantMatch: "block_size_bytes is not valid with source_path",
		},
		{
			name: "no source_path and no vhd_type rejects",
			model: Model{
				Path: pathtype.NewPathValue(dst), SourcePath: pathtype.NewPathNull(),
				VhdType: types.StringNull(), ParentPath: pathtype.NewPathNull(),
				BlockSizeBytes: types.Int64Null(),
			},
			wantError: true, wantMatch: "vhd_type is required",
		},
		{
			name: "no source_path with vhd_type allows",
			model: Model{
				Path: pathtype.NewPathValue(dst), SourcePath: pathtype.NewPathNull(),
				VhdType: types.StringValue("dynamic"), ParentPath: pathtype.NewPathNull(),
				BlockSizeBytes: types.Int64Null(),
			},
		},
		{
			name: "source_path equal to path rejects",
			model: Model{
				Path: pathtype.NewPathValue(dst), SourcePath: pathtype.NewPathValue(dst),
				VhdType: types.StringNull(), ParentPath: pathtype.NewPathNull(),
				BlockSizeBytes: types.Int64Null(),
			},
			wantError: true, wantMatch: "must differ",
		},
		{
			name: "source_path equal to path modulo slashes and case rejects",
			model: Model{
				Path:       pathtype.NewPathValue(`C:\VMs\CP1\BOOT.VHDX`),
				SourcePath: pathtype.NewPathValue(dst),
				VhdType:    types.StringNull(), ParentPath: pathtype.NewPathNull(),
				BlockSizeBytes: types.Int64Null(),
			},
			wantError: true, wantMatch: "must differ",
		},
		{
			name: "unknown source_path skips (deferred dependency)",
			model: Model{
				Path: pathtype.NewPathValue(dst), SourcePath: pathtype.NewPathUnknown(),
				VhdType: types.StringNull(), ParentPath: pathtype.NewPathNull(),
				BlockSizeBytes: types.Int64Null(),
			},
		},
		{
			name: "unknown path skips the same-file check",
			model: Model{
				Path: pathtype.NewPathUnknown(), SourcePath: pathtype.NewPathValue(src),
				VhdType: types.StringNull(), ParentPath: pathtype.NewPathNull(),
				BlockSizeBytes: types.Int64Null(),
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := sourcePathModeValidator{}.validate(t.Context(), tc.model)
			if got.HasError() != tc.wantError {
				t.Fatalf("validate(...).HasError() = %v, want %v\nfull diags: %v", got.HasError(), tc.wantError, got)
			}
			if tc.wantError && !strings.Contains(got[0].Summary(), tc.wantMatch) {
				t.Errorf("diag summary = %q, want substring %q", got[0].Summary(), tc.wantMatch)
			}
		})
	}
}

// The pre-existing size_bytes and parent_path validators both key on
// vhd_type, which is absent in source_path mode. Without an explicit
// bail-out the size_bytes rule would demand a size for every copied disk
// and the parent_path rule would misreport its own message, so this pins
// that they stand down.
func TestExistingValidators_StandDownInSourcePathMode(t *testing.T) {
	t.Parallel()

	model := Model{
		Path:       pathtype.NewPathValue("C:/vms/cp1/boot.vhdx"),
		SourcePath: pathtype.NewPathValue("D:/images/fcos.vhdx"),
		VhdType:    types.StringNull(),
		ParentPath: pathtype.NewPathNull(),
		SizeBytes:  types.Int64Null(),
	}

	if got := (sizeBytesRequiresFixedOrDynamicValidator{}).validate(model); got.HasError() {
		t.Errorf("sizeBytesRequiresFixedOrDynamicValidator fired in source_path mode: %v", got)
	}
	if got := (parentPathRequiresDifferencingValidator{}).validate(model); got.HasError() {
		t.Errorf("parentPathRequiresDifferencingValidator fired in source_path mode: %v", got)
	}

	// ConfigValidators run against Config, not Plan, so a source_path wired
	// to another resource's attribute is *unknown* there rather than known.
	// Treating unknown as "not set" makes the size_bytes rule demand a size
	// for a copied disk and breaks the chained-source config outright.
	unknownSource := model
	unknownSource.SourcePath = pathtype.NewPathUnknown()

	if got := (sizeBytesRequiresFixedOrDynamicValidator{}).validate(unknownSource); got.HasError() {
		t.Errorf("sizeBytesRequiresFixedOrDynamicValidator fired for unknown source_path: %v", got)
	}
	if got := (parentPathRequiresDifferencingValidator{}).validate(unknownSource); got.HasError() {
		t.Errorf("parentPathRequiresDifferencingValidator fired for unknown source_path: %v", got)
	}
}

// modelFromVHD must carry source_path and source_sha256 across from
// intent: Get-VHD has no notion of either, and Update keys on
// source_sha256 to decide whether the upstream image changed. Dropping it
// would make every apply either re-copy or never re-copy.
func TestModelFromVHD_PreservesSourceIntent(t *testing.T) {
	t.Parallel()

	got := modelFromVHD(&hyperv.VHD{
		Path:      "C:\\vms\\cp1\\boot.vhdx",
		VhdType:   "Dynamic",
		SizeBytes: 21474836480,
	}, Model{
		SourcePath:   pathtype.NewPathValue("D:/images/fcos.vhdx"),
		SourceSha256: types.StringValue("abc123"),
	})

	if got.SourcePath.ValueString() != "D:/images/fcos.vhdx" {
		t.Errorf("SourcePath = %q, want passthrough", got.SourcePath.ValueString())
	}
	if got.SourceSha256.ValueString() != "abc123" {
		t.Errorf("SourceSha256 = %q, want passthrough", got.SourceSha256.ValueString())
	}
}

// source_sha256 is Computed, so it arrives unknown on every create --
// including fixed/dynamic/differencing, which have no source to hash.
// Leaving the unknown in place makes Terraform reject the apply with
// "provider returned invalid result object", so it has to collapse to
// null whenever there is no source.
func TestModelFromVHD_CollapsesSourceShaToNullWithoutSource(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name   string
		intent Model
	}{
		{
			name:   "create mode: no source, unknown sha",
			intent: Model{SourcePath: pathtype.NewPathNull(), SourceSha256: types.StringUnknown()},
		},
		{
			name:   "unknown source_path also yields a null sha",
			intent: Model{SourcePath: pathtype.NewPathUnknown(), SourceSha256: types.StringUnknown()},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := modelFromVHD(&hyperv.VHD{
				Path:    "C:\\vhds\\plain.vhdx",
				VhdType: "Dynamic",
			}, tc.intent)

			if got.SourceSha256.IsUnknown() {
				t.Error("SourceSha256 is unknown after apply; Terraform rejects the result object")
			}
			if !got.SourceSha256.IsNull() {
				t.Errorf("SourceSha256 = %v, want null", got.SourceSha256)
			}
			if got.SourcePath.IsUnknown() {
				t.Error("SourcePath is unknown after apply; Terraform rejects the result object")
			}
		})
	}
}

// A resize that fails after the copy has already been renamed into place
// leaves a disk on the host that no state describes -- wrong size, and
// invisible to `terraform state`. copyAndResize removes it so a failed
// apply leaves nothing behind, matching the cleanup contract new.ps1's
// finally blocks give the staging file.
func TestCopyAndResize_RemovesCopyWhenResizeFails(t *testing.T) {
	t.Parallel()

	fr := testutil.NewFakeRunner().
		On("function New-HypervImageFileFromSourcePath").Return(testutil.ImageFileFixtureJSON, "", 0).
		On("function Set-HypervVHD").Return("", `{"category":"InvalidArgument","message":"shrink requires empty trailing blocks","cmdlet":"Resize-VHD"}`, 1).
		On("function Remove-HypervVHD").Return("", "", 0)

	r := &Resource{client: hyperv.NewClient(fr)}

	_, _, err := r.copyAndResize(t.Context(), Model{
		Path:         pathtype.NewPathValue("C:/vms/cp1/boot.vhdx"),
		SourcePath:   pathtype.NewPathValue("D:/images/fcos.vhdx"),
		SourceSha256: types.StringValue("abc123"),
		SizeBytes:    types.Int64Value(1024),
	})
	if err == nil {
		t.Fatal("copyAndResize returned nil error; the resize failure must surface")
	}
	if !strings.Contains(err.Error(), "shrink requires empty trailing blocks") {
		t.Errorf("err = %v, want the resize error preserved (not replaced by a cleanup error)", err)
	}

	var sawRemove bool
	for _, call := range fr.Calls() {
		if strings.Contains(call.Script, "function Remove-HypervVHD") {
			sawRemove = true
		}
	}
	if !sawRemove {
		t.Error("no remove.ps1 call; the orphaned copy is left on the host after a failed resize")
	}
}

// A cleanup that itself fails must not displace the resize error -- that
// is the one the operator needs in order to understand what went wrong.
func TestCopyAndResize_CleanupFailureDoesNotMaskResizeError(t *testing.T) {
	t.Parallel()

	fr := testutil.NewFakeRunner().
		On("function New-HypervImageFileFromSourcePath").Return(testutil.ImageFileFixtureJSON, "", 0).
		On("function Set-HypervVHD").Return("", `{"category":"InvalidArgument","message":"resize boom","cmdlet":"Resize-VHD"}`, 1).
		On("function Remove-HypervVHD").Return("", `{"category":"PermissionDenied","message":"cleanup boom","cmdlet":"Remove-Item"}`, 1)

	r := &Resource{client: hyperv.NewClient(fr)}

	_, _, err := r.copyAndResize(t.Context(), Model{
		Path:         pathtype.NewPathValue("C:/vms/cp1/boot.vhdx"),
		SourcePath:   pathtype.NewPathValue("D:/images/fcos.vhdx"),
		SourceSha256: types.StringValue("abc123"),
		SizeBytes:    types.Int64Value(1024),
	})
	if err == nil {
		t.Fatal("copyAndResize returned nil error")
	}
	if strings.Contains(err.Error(), "cleanup boom") {
		t.Errorf("err = %v, want the resize error; the cleanup failure must only be logged", err)
	}
	if !strings.Contains(err.Error(), "resize boom") {
		t.Errorf("err = %v, want the resize error preserved", err)
	}
}

// Without size_bytes there is no resize step, so a successful copy must
// not trigger the failure-path cleanup that would delete the disk it just
// made.
func TestCopyAndResize_NoResizeWhenSizeUnset(t *testing.T) {
	t.Parallel()

	fr := testutil.NewFakeRunner().
		On("function New-HypervImageFileFromSourcePath").Return(testutil.ImageFileFixtureJSON, "", 0).
		On("function Get-HypervVHD").Return(`{"Path":"C:\\vms\\cp1\\boot.vhdx","VhdType":"Dynamic","SizeBytes":1024,"FileSizeBytes":512,"BlockSizeBytes":2097152,"ParentPath":"","Format":"VHDX","Attached":false}`, "", 0)

	r := &Resource{client: hyperv.NewClient(fr)}

	_, sourceSha, err := r.copyAndResize(t.Context(), Model{
		Path:         pathtype.NewPathValue("C:/vms/cp1/boot.vhdx"),
		SourcePath:   pathtype.NewPathValue("D:/images/fcos.vhdx"),
		SourceSha256: types.StringValue("abc123"),
		SizeBytes:    types.Int64Null(),
	})
	if err != nil {
		t.Fatalf("copyAndResize: %v", err)
	}
	if sourceSha == "" {
		t.Error("sourceSha is empty; the copy's hash is what source_sha256 records")
	}
	for _, call := range fr.Calls() {
		if strings.Contains(call.Script, "function Set-HypervVHD") {
			t.Error("resize ran without size_bytes set")
		}
		if strings.Contains(call.Script, "function Remove-HypervVHD") {
			t.Error("cleanup ran on the success path; the copy would be deleted")
		}
	}
}
