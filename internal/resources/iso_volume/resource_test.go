package iso_volume

import (
	"context"
	"reflect"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/windsorcli/terraform-provider-hyperv/internal/hyperv"
	pathtype "github.com/windsorcli/terraform-provider-hyperv/internal/types/path"
)

// hasPlanModifier reports whether any plan-modifier in `mods` has a
// type whose package-qualified name contains `keyword`. Same shape the
// vswitch and image_file resource tests use; the case-insensitive
// substring match keeps assertions robust to upstream renames between
// `RequiresReplace` and `RequiresReplaceIfConfigured`.
func hasPlanModifier[M any](mods []M, keyword string) bool {
	for _, pm := range mods {
		if strings.Contains(strings.ToLower(reflect.TypeOf(pm).String()), strings.ToLower(keyword)) {
			return true
		}
	}
	return false
}

// Schema test: every locked-in attribute is present. Drift here is a
// contract break for users.
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
		"destination_path",
		"volume_label",
		"files",
		"sha256",
		"size_bytes",
		"keep_on_destroy",
	}
	for _, name := range wantAttrs {
		if _, ok := resp.Schema.Attributes[name]; !ok {
			t.Errorf("missing attribute %q", name)
		}
	}
}

// destination_path is the only RequiresReplace attribute. volume_label
// and files must be in-place mutable -- a config edit rebuilds and
// re-streams via Update, never destroy+recreate. A regression that
// tagged either of those with RequiresReplace would silently force
// resource churn on every cloud-init config tweak.
func TestResource_Schema_OnlyDestinationPathRequiresReplace(t *testing.T) {
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

	vl, ok := resp.Schema.Attributes["volume_label"].(schema.StringAttribute)
	if !ok {
		t.Fatalf("volume_label is not a StringAttribute (got %T)", resp.Schema.Attributes["volume_label"])
	}
	if hasPlanModifier(vl.PlanModifiers, "RequiresReplace") {
		t.Error(`"volume_label" must NOT carry RequiresReplace -- in-place rebuild is the contract`)
	}

	files, ok := resp.Schema.Attributes["files"].(schema.MapAttribute)
	if !ok {
		t.Fatalf("files is not a MapAttribute (got %T)", resp.Schema.Attributes["files"])
	}
	if hasPlanModifier(files.PlanModifiers, "RequiresReplace") {
		t.Error(`"files" must NOT carry RequiresReplace -- in-place rebuild is the contract`)
	}
}

// id, sha256, size_bytes are Computed -- UseStateForUnknown keeps plan
// output clean across applies that don't touch them. Drift detection
// still works (Read writes fresh values during refresh).
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
	checkString("sha256")

	if intAttr, ok := resp.Schema.Attributes["size_bytes"].(schema.Int64Attribute); ok {
		if !hasPlanModifier(intAttr.PlanModifiers, "UseStateForUnknown") {
			t.Error(`"size_bytes" must carry UseStateForUnknown`)
		}
	} else {
		t.Errorf(`"size_bytes" missing or wrong type (got %T)`, resp.Schema.Attributes["size_bytes"])
	}
}

// keep_on_destroy mirrors hyperv_image_file's escape hatch: Optional +
// Computed (so users can omit; framework fills the default), Default
// false, UseStateForUnknown so plan stays clean across applies that
// don't touch the flag.
func TestResource_Schema_KeepOnDestroy(t *testing.T) {
	t.Parallel()

	r := New()
	resp := &resource.SchemaResponse{}
	r.Schema(t.Context(), resource.SchemaRequest{}, resp)

	raw, ok := resp.Schema.Attributes["keep_on_destroy"]
	if !ok {
		t.Fatal(`missing attribute "keep_on_destroy"`)
	}
	attr, ok := raw.(schema.BoolAttribute)
	if !ok {
		t.Fatalf("keep_on_destroy is not a BoolAttribute (got %T)", raw)
	}
	if !attr.Optional {
		t.Error(`"keep_on_destroy" must be Optional`)
	}
	if !attr.Computed {
		t.Error(`"keep_on_destroy" must be Computed (default carries through unset configs)`)
	}
	if attr.Default == nil {
		t.Error(`"keep_on_destroy" must carry a Default (false)`)
	}
	if !hasPlanModifier(attr.PlanModifiers, "UseStateForUnknown") {
		t.Error(`"keep_on_destroy" must carry UseStateForUnknown`)
	}
}

// Metadata pins the resource's TF type name. Any change here is a
// user-visible breaking rename.
func TestResource_Metadata(t *testing.T) {
	t.Parallel()

	r := New()
	resp := &resource.MetadataResponse{}
	r.Metadata(t.Context(), resource.MetadataRequest{ProviderTypeName: "hyperv"}, resp)
	if resp.TypeName != "hyperv_iso_volume" {
		t.Errorf("TypeName = %q, want %q", resp.TypeName, "hyperv_iso_volume")
	}
}

// Configure with nil ProviderData (validate-time invocation) must
// NOT panic and must NOT error.
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
// diagnostic that names *hyperv.Client so the operator can correct
// the provider wiring without spelunking the framework internals.
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

// modelFromImageFile preserves caller-supplied volume_label, files,
// and keep_on_destroy through Read/Create/Update. None of those are
// derivable from the file on disk -- they're user intent kept in
// Terraform state. Without this preservation, Read after refresh
// would zero them out and the next plan would show a phantom diff
// (or, for keep_on_destroy, silently switch destroy semantics).
func TestModelFromImageFile_PreservesUserIntent(t *testing.T) {
	t.Parallel()

	f := &hyperv.ImageFile{
		Path:      "C:\\hyperv\\seeds\\node1.iso",
		SizeBytes: 387072,
		Sha256:    "abcdef0123456789abcdef0123456789abcdef0123456789abcdef0123456789",
	}
	files, diags := types.MapValueFrom(context.Background(), types.StringType, map[string]string{
		"meta-data": "instance-id: iid-001\n",
		"user-data": "#cloud-config\n",
	})
	if diags.HasError() {
		t.Fatalf("MapValueFrom: %v", diags)
	}

	got := modelFromImageFile(f, types.StringValue("CIDATA"), files, types.BoolValue(true))

	if got.ID.ValueString() != f.Path {
		t.Errorf("ID = %q, want %q", got.ID.ValueString(), f.Path)
	}
	if got.DestinationPath.ValueString() != f.Path {
		t.Errorf("DestinationPath = %q, want %q", got.DestinationPath.ValueString(), f.Path)
	}
	if got.Sha256.ValueString() != f.Sha256 {
		t.Errorf("Sha256 = %q, want %q", got.Sha256.ValueString(), f.Sha256)
	}
	if got.SizeBytes.ValueInt64() != f.SizeBytes {
		t.Errorf("SizeBytes = %d, want %d", got.SizeBytes.ValueInt64(), f.SizeBytes)
	}
	if got.VolumeLabel.ValueString() != "CIDATA" {
		t.Errorf("VolumeLabel = %q, want passthrough", got.VolumeLabel.ValueString())
	}
	if !got.KeepOnDestroy.ValueBool() {
		t.Error("KeepOnDestroy = false, want true (caller passed types.BoolValue(true))")
	}
	if got.Files.IsNull() || got.Files.IsUnknown() {
		t.Error("Files = null/unknown, want passthrough of caller-supplied map")
	}
}

// filesFromMap converts the model's Map<string, string> into the
// internal/iso File slice the synthesizer consumes, sorted by name.
// The sort is the seam that makes the resource bytes reproducible
// regardless of HCL Map iteration order. iso.Build re-sorts as
// defense in depth; this test pins the resource-side guarantee.
func TestFilesFromMap_SortsByName(t *testing.T) {
	t.Parallel()

	files, diags := types.MapValueFrom(context.Background(), types.StringType, map[string]string{
		"user-data":      "b",
		"network-config": "c",
		"meta-data":      "a",
	})
	if diags.HasError() {
		t.Fatalf("MapValueFrom: %v", diags)
	}

	got, fdiags := filesFromMap(context.Background(), files)
	if fdiags.HasError() {
		t.Fatalf("filesFromMap: %v", fdiags)
	}
	if len(got) != 3 {
		t.Fatalf("len = %d, want 3", len(got))
	}
	wantOrder := []string{"meta-data", "network-config", "user-data"}
	for i, want := range wantOrder {
		if got[i].Name != want {
			t.Errorf("[%d].Name = %q, want %q", i, got[i].Name, want)
		}
	}
}

// filesFromMap returns nil-and-no-diags for null and unknown maps.
// This path matters: in Read on an Imported resource the state Files
// is null, and filesFromMap is a no-op; ditto for plan-time when the
// map is unknown (driven from a not-yet-resolved variable).
func TestFilesFromMap_NullAndUnknownAreNoop(t *testing.T) {
	t.Parallel()

	for _, m := range []types.Map{
		types.MapNull(types.StringType),
		types.MapUnknown(types.StringType),
	} {
		got, diags := filesFromMap(context.Background(), m)
		if diags.HasError() {
			t.Errorf("filesFromMap(%v): unexpected diags %v", m, diags)
		}
		if got != nil {
			t.Errorf("filesFromMap(%v): got %v, want nil slice", m, got)
		}
	}
}

// modelFromImageFile must round-trip the path through pathtype.Path
// for both ID and DestinationPath -- the framework's plan-vs-apply
// consistency check fires a "Provider produced inconsistent result"
// when the cmdlet returns canonical-form (backslash) and state holds
// the user's HCL form (forward slash). The Path custom type
// neutralizes the diff via StringSemanticEquals.
func TestModelFromImageFile_PathTypeOnIDAndDest(t *testing.T) {
	t.Parallel()

	f := &hyperv.ImageFile{
		Path:      "C:\\hyperv\\seeds\\x.iso",
		SizeBytes: 100,
		Sha256:    "0000000000000000000000000000000000000000000000000000000000000000",
	}
	got := modelFromImageFile(f, types.StringValue("CIDATA"), types.MapNull(types.StringType), types.BoolValue(false))

	if _, ok := any(got.ID).(pathtype.Path); !ok {
		t.Errorf("ID is not pathtype.Path (got %T)", got.ID)
	}
	if _, ok := any(got.DestinationPath).(pathtype.Path); !ok {
		t.Errorf("DestinationPath is not pathtype.Path (got %T)", got.DestinationPath)
	}
}
