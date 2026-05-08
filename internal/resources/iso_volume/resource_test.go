package iso_volume

import (
	"context"
	"reflect"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/windsorcli/terraform-provider-hyperv/internal/hyperv"
	pathtype "github.com/windsorcli/terraform-provider-hyperv/internal/types/path"
)

// hasPlanModifier checks if any plan-modifier in `mods` has a type whose
// package-qualified name contains `keyword`. Same helper shape as the
// image_file / vswitch resource tests use.
func hasPlanModifier[M any](mods []M, keyword string) bool {
	for _, pm := range mods {
		if strings.Contains(strings.ToLower(reflect.TypeOf(pm).String()), strings.ToLower(keyword)) {
			return true
		}
	}
	return false
}

// TestResource_Schema_HasAllAttributes pins the locked-in attribute set.
// Drift here is a contract break for users.
func TestResource_Schema_HasAllAttributes(t *testing.T) {
	t.Parallel()

	r := New()
	resp := &resource.SchemaResponse{}
	r.Schema(t.Context(), resource.SchemaRequest{}, resp)

	if resp.Diagnostics.HasError() {
		t.Fatalf("schema diagnostics: %v", resp.Diagnostics)
	}
	want := []string{
		"id",
		"destination_path",
		"volume_label",
		"files",
		"sha256",
		"size_bytes",
		"keep_on_destroy",
	}
	for _, name := range want {
		if _, ok := resp.Schema.Attributes[name]; !ok {
			t.Errorf("missing attribute %q", name)
		}
	}
}

// TestResource_Schema_DestinationPathRequiresReplace pins that changing
// the destination forces destroy+recreate -- the resource never moves
// files in place.
func TestResource_Schema_DestinationPathRequiresReplace(t *testing.T) {
	t.Parallel()

	r := New()
	resp := &resource.SchemaResponse{}
	r.Schema(t.Context(), resource.SchemaRequest{}, resp)

	dp, ok := resp.Schema.Attributes["destination_path"].(schema.StringAttribute)
	if !ok {
		t.Fatalf("destination_path is not a StringAttribute (got %T)", resp.Schema.Attributes["destination_path"])
	}
	if !hasPlanModifier(dp.PlanModifiers, "RequiresReplace") {
		t.Error(`"destination_path" must carry RequiresReplace`)
	}
}

// TestResource_Schema_VolumeLabelAndFilesAreUpdatable pins that
// volume_label and files are NOT RequiresReplace -- the resource's value
// over hyperv_image_file's local_path mode is in-place updates that
// rebuild + re-stream rather than destroy/recreate.
func TestResource_Schema_VolumeLabelAndFilesAreUpdatable(t *testing.T) {
	t.Parallel()

	r := New()
	resp := &resource.SchemaResponse{}
	r.Schema(t.Context(), resource.SchemaRequest{}, resp)

	label, ok := resp.Schema.Attributes["volume_label"].(schema.StringAttribute)
	if !ok {
		t.Fatalf("volume_label is not a StringAttribute (got %T)", resp.Schema.Attributes["volume_label"])
	}
	if hasPlanModifier(label.PlanModifiers, "RequiresReplace") {
		t.Error(`"volume_label" must NOT carry RequiresReplace -- changes rebuild+re-stream in place`)
	}

	files, ok := resp.Schema.Attributes["files"].(schema.MapAttribute)
	if !ok {
		t.Fatalf("files is not a MapAttribute (got %T)", resp.Schema.Attributes["files"])
	}
	if hasPlanModifier(files.PlanModifiers, "RequiresReplace") {
		t.Error(`"files" must NOT carry RequiresReplace -- changes rebuild+re-stream in place`)
	}
}

// TestResource_Schema_ComputedHaveUseStateForUnknown keeps refresh from
// emitting `(known after apply)` for sha256/size_bytes when nothing
// relevant has changed.
func TestResource_Schema_ComputedHaveUseStateForUnknown(t *testing.T) {
	t.Parallel()

	r := New()
	resp := &resource.SchemaResponse{}
	r.Schema(t.Context(), resource.SchemaRequest{}, resp)

	id, ok := resp.Schema.Attributes["id"].(schema.StringAttribute)
	if !ok {
		t.Fatalf("id is not a StringAttribute (got %T)", resp.Schema.Attributes["id"])
	}
	if !hasPlanModifier(id.PlanModifiers, "UseStateForUnknown") {
		t.Error(`"id" must carry UseStateForUnknown`)
	}

	sha, ok := resp.Schema.Attributes["sha256"].(schema.StringAttribute)
	if !ok {
		t.Fatalf("sha256 is not a StringAttribute (got %T)", resp.Schema.Attributes["sha256"])
	}
	if !hasPlanModifier(sha.PlanModifiers, "UseStateForUnknown") {
		t.Error(`"sha256" must carry UseStateForUnknown`)
	}

	sb, ok := resp.Schema.Attributes["size_bytes"].(schema.Int64Attribute)
	if !ok {
		t.Fatalf("size_bytes is not an Int64Attribute (got %T)", resp.Schema.Attributes["size_bytes"])
	}
	if !hasPlanModifier(sb.PlanModifiers, "UseStateForUnknown") {
		t.Error(`"size_bytes" must carry UseStateForUnknown`)
	}
}

// TestResource_Schema_FilesIsSensitive pins the redaction flag on the
// files map. cloud-init user-data and autounattend.xml routinely carry
// SSH private keys, admin passwords, and cloud credentials; a regression
// that drops Sensitive would silently start emitting those in plan
// output (and CI logs).
func TestResource_Schema_FilesIsSensitive(t *testing.T) {
	t.Parallel()

	r := New()
	resp := &resource.SchemaResponse{}
	r.Schema(t.Context(), resource.SchemaRequest{}, resp)

	files, ok := resp.Schema.Attributes["files"].(schema.MapAttribute)
	if !ok {
		t.Fatalf("files is not a MapAttribute (got %T)", resp.Schema.Attributes["files"])
	}
	if !files.Sensitive {
		t.Error(`"files" must be Sensitive: true -- the map can carry cloud-init or autounattend secrets`)
	}
}

// TestResource_Schema_VolumeLabelHasRegexValidator pins the ISO9660
// d-character + uppercase regex at the schema layer so a typo'd label
// surfaces at plan time rather than as a builder error at apply.
func TestResource_Schema_VolumeLabelHasRegexValidator(t *testing.T) {
	t.Parallel()

	r := New()
	resp := &resource.SchemaResponse{}
	r.Schema(t.Context(), resource.SchemaRequest{}, resp)

	label, ok := resp.Schema.Attributes["volume_label"].(schema.StringAttribute)
	if !ok {
		t.Fatalf("volume_label is not a StringAttribute (got %T)", resp.Schema.Attributes["volume_label"])
	}
	if !label.Required {
		t.Error(`"volume_label" must be Required`)
	}
	if len(label.Validators) == 0 {
		t.Error(`"volume_label" must carry a validator (the [A-Z0-9_]{1,32} regex)`)
	}
}

// TestResource_Metadata pins the resource's TF type name. Any change here
// is a user-visible breaking rename.
func TestResource_Metadata(t *testing.T) {
	t.Parallel()

	r := New()
	resp := &resource.MetadataResponse{}
	r.Metadata(t.Context(), resource.MetadataRequest{ProviderTypeName: "hyperv"}, resp)
	if resp.TypeName != "hyperv_iso_volume" {
		t.Errorf("TypeName = %q, want %q", resp.TypeName, "hyperv_iso_volume")
	}
}

// TestResource_Configure_NilProviderDataIsNoop pins that validate-time
// invocations (which pass nil ProviderData) don't panic and don't error.
// Without this guard the framework panics during validation.
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

// TestResource_Configure_WrongTypeIsClearError pins the diagnostic shape
// for a mis-wired provider so the operator can fix it without spelunking
// framework internals.
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

// TestFilesTotalSizeValidator_UnderCapPasses asserts the validator no-ops
// when the summed bytes are within the cap. The cap exists to push large
// payloads onto hyperv_image_file -- the kilobyte-scale seeds this
// resource serves shouldn't trip it.
func TestFilesTotalSizeValidator_UnderCapPasses(t *testing.T) {
	t.Parallel()

	files, _ := types.MapValueFrom(context.Background(), types.StringType, map[string]string{
		"meta-data": strings.Repeat("a", 1024),
		"user-data": strings.Repeat("b", 1024),
	})
	v := filesTotalSizeValidator{}
	diags := v.validate(context.Background(), Model{Files: files})
	if diags.HasError() {
		t.Errorf("under-cap validate produced an error: %v", diags)
	}
}

// TestFilesTotalSizeValidator_OverCapFailsOnFiles asserts the cap check
// surfaces an attribute-anchored diagnostic on `files` so the CLI lands
// the operator on the right line.
func TestFilesTotalSizeValidator_OverCapFailsOnFiles(t *testing.T) {
	t.Parallel()

	files, _ := types.MapValueFrom(context.Background(), types.StringType, map[string]string{
		"big": strings.Repeat("x", totalFilesByteCap+1),
	})
	v := filesTotalSizeValidator{}
	diags := v.validate(context.Background(), Model{Files: files})
	if !diags.HasError() {
		t.Fatal("over-cap validate did not produce an error")
	}
	got := diags[0].Summary()
	if !strings.Contains(strings.ToLower(got), "files total size") {
		t.Errorf("diag summary = %q, want it to mention 'files total size'", got)
	}
}

// TestFilesTotalSizeValidator_NullOrUnknownNoOps asserts the validator
// doesn't fire on null / unknown maps -- the schema-layer SizeAtLeast(1)
// is what handles the missing case, and an unknown driven by a variable
// must round-trip without error.
func TestFilesTotalSizeValidator_NullOrUnknownNoOps(t *testing.T) {
	t.Parallel()

	v := filesTotalSizeValidator{}

	if diags := v.validate(context.Background(), Model{Files: types.MapNull(types.StringType)}); diags.HasError() {
		t.Errorf("null files: %v", diags)
	}
	if diags := v.validate(context.Background(), Model{Files: types.MapUnknown(types.StringType)}); diags.HasError() {
		t.Errorf("unknown files: %v", diags)
	}
}

// TestFilesTotalSizeValidator_PartialUnknownDefers is the Bug #4
// regression: a for_each-driven config where each.value carries unknown
// values produces a Map whose structure is known but whose element
// values are not -- IsUnknown() returns false on the outer Map. Without
// the wholly-known gate the validator falls through into ElementsAs,
// which rejects unknown StringValue elements with a confusing reflect-
// time conversion error during `terraform validate`. The validator must
// defer until the parent variables resolve.
func TestFilesTotalSizeValidator_PartialUnknownDefers(t *testing.T) {
	t.Parallel()

	partial, d := types.MapValue(types.StringType, map[string]attr.Value{
		"meta-data": types.StringValue("instance-id: foo\n"),
		"user-data": types.StringUnknown(),
	})
	if d.HasError() {
		t.Fatalf("MapValue: %v", d)
	}

	v := filesTotalSizeValidator{}
	if diags := v.validate(context.Background(), Model{Files: partial}); diags.HasError() {
		t.Errorf("partial-unknown files must defer (no error); got: %v", diags)
	}
}

// TestFilesMapWhollyKnown is the helper-level regression matching the
// validator-level test above. Pinned separately because ModifyPlan
// also keys on this helper -- a regression that broke wholly-known
// detection would surface in apply (ModifyPlan crash on unknown
// element) before it surfaced in validate.
func TestFilesMapWhollyKnown(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		m    types.Map
		want bool
	}{
		{
			name: "null map",
			m:    types.MapNull(types.StringType),
			want: false,
		},
		{
			name: "wholly unknown map",
			m:    types.MapUnknown(types.StringType),
			want: false,
		},
		{
			name: "known map with all known string values",
			m: func() types.Map {
				m, _ := types.MapValue(types.StringType, map[string]attr.Value{
					"meta-data": types.StringValue("a"),
					"user-data": types.StringValue("b"),
				})
				return m
			}(),
			want: true,
		},
		{
			name: "known map with one unknown value (Bug #4 case)",
			m: func() types.Map {
				m, _ := types.MapValue(types.StringType, map[string]attr.Value{
					"meta-data": types.StringValue("a"),
					"user-data": types.StringUnknown(),
				})
				return m
			}(),
			want: false,
		},
		{
			name: "empty known map",
			m: func() types.Map {
				m, _ := types.MapValue(types.StringType, map[string]attr.Value{})
				return m
			}(),
			want: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := filesMapWhollyKnown(tc.m); got != tc.want {
				t.Errorf("filesMapWhollyKnown(%s) = %v, want %v", tc.name, got, tc.want)
			}
		})
	}
}

// TestModelFromIsoVolume_PreservesUserIntent round-trips volume_label /
// files / keep_on_destroy through the Read shape. Without this, those
// values would be lost on every refresh -- they aren't reconstructible
// from the file on disk.
func TestModelFromIsoVolume_PreservesUserIntent(t *testing.T) {
	t.Parallel()

	v := &hyperv.IsoVolume{
		Path:      "C:\\hyperv\\seed\\cidata.iso",
		SizeBytes: 43008,
		Sha256:    "abcdef0123456789abcdef0123456789abcdef0123456789abcdef0123456789",
	}
	files, _ := types.MapValueFrom(context.Background(), types.StringType, map[string]string{
		"meta-data": "instance-id: foo\n",
	})

	got := modelFromIsoVolume(v, types.StringValue("CIDATA"), files, types.BoolValue(true))

	if got.ID.ValueString() != v.Path {
		t.Errorf("ID = %q, want %q", got.ID.ValueString(), v.Path)
	}
	if got.DestinationPath.ValueString() != v.Path {
		t.Errorf("DestinationPath = %q, want %q", got.DestinationPath.ValueString(), v.Path)
	}
	if got.VolumeLabel.ValueString() != "CIDATA" {
		t.Errorf("VolumeLabel = %q, want CIDATA (must round-trip from caller)", got.VolumeLabel.ValueString())
	}
	if !got.KeepOnDestroy.ValueBool() {
		t.Error("KeepOnDestroy = false, want true (must round-trip from caller)")
	}
	if got.Sha256.ValueString() != v.Sha256 {
		t.Errorf("Sha256 = %q, want %q", got.Sha256.ValueString(), v.Sha256)
	}
	if got.SizeBytes.ValueInt64() != v.SizeBytes {
		t.Errorf("SizeBytes = %d, want %d", got.SizeBytes.ValueInt64(), v.SizeBytes)
	}
	if got.Files.IsNull() {
		t.Error("Files null; expected user-supplied map preserved")
	}
}

// TestDecodeFilesMap_RoundTripsKeysAndValues asserts the helper doesn't
// silently drop entries -- a regression here would mean BuildISO sees a
// truncated file set and the ISO ships missing entries the user
// configured.
func TestDecodeFilesMap_RoundTripsKeysAndValues(t *testing.T) {
	t.Parallel()

	src := map[string]string{
		"meta-data":      "instance-id: foo\n",
		"user-data":      "#cloud-config\n",
		"network-config": "version: 2\n",
	}
	tfMap, diags := types.MapValueFrom(context.Background(), types.StringType, src)
	if diags.HasError() {
		t.Fatalf("MapValueFrom: %v", diags)
	}

	got, diags := decodeFilesMap(context.Background(), tfMap)
	if diags.HasError() {
		t.Fatalf("decodeFilesMap: %v", diags)
	}
	if !reflect.DeepEqual(got, src) {
		t.Errorf("got %+v, want %+v", got, src)
	}
}

// TestDecodeFilesMap_NullReturnsNil pins the null/unknown handling so the
// validator's early-return branch behaves correctly.
func TestDecodeFilesMap_NullReturnsNil(t *testing.T) {
	t.Parallel()

	got, diags := decodeFilesMap(context.Background(), types.MapNull(types.StringType))
	if diags.HasError() {
		t.Fatalf("decodeFilesMap: %v", diags)
	}
	if got != nil {
		t.Errorf("got %v, want nil for null map", got)
	}
}

// Build the resource and shake out any wiring issues that show up only
// when interfaces are exercised.
func TestNew_ReturnsResource(t *testing.T) {
	t.Parallel()
	if r := New(); r == nil {
		t.Error("New() returned nil")
	}
	if _, ok := New().(*Resource); !ok {
		t.Error("New() did not return *Resource")
	}
}

// TestResource_BuildISO_ProducesNonEmptyBytes ensures the resource-layer
// adapter actually drives BuildISO -- a regression to nil/empty would
// silently make every Create stream zero bytes and surface as a
// host-side checksum mismatch with no clue why.
func TestResource_BuildISO_ProducesNonEmptyBytes(t *testing.T) {
	t.Parallel()

	r, ok := New().(*Resource)
	if !ok {
		t.Fatal("New() did not return *Resource")
	}
	files, _ := types.MapValueFrom(context.Background(), types.StringType, map[string]string{
		"meta-data": "instance-id: foo\n",
	})
	plan := &Model{
		DestinationPath: pathtype.NewPathValue("C:\\hyperv\\seed\\cidata.iso"),
		VolumeLabel:     types.StringValue("CIDATA"),
		Files:           files,
	}
	body, diags := r.buildISO(context.Background(), plan)
	if diags.HasError() {
		t.Fatalf("buildISO: %v", diags)
	}
	if len(body) == 0 {
		t.Error("buildISO produced empty bytes")
	}
}
