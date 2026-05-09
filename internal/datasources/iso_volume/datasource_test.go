package iso_volume

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/windsorcli/terraform-provider-hyperv/internal/iso"
)

// Schema test: every locked-in attribute is present. Drift here is a
// contract break for users.
func TestDataSource_Schema(t *testing.T) {
	t.Parallel()

	d := New()
	resp := &datasource.SchemaResponse{}
	d.Schema(t.Context(), datasource.SchemaRequest{}, resp)

	if resp.Diagnostics.HasError() {
		t.Fatalf("schema diagnostics: %v", resp.Diagnostics)
	}
	wantAttrs := map[string]bool{
		"volume_label":   false, // Required
		"files":          false, // Required
		"content_base64": true,  // Computed
		"sha256":         true,  // Computed
		"size_bytes":     true,  // Computed
		"id":             true,  // Computed
	}
	for name, wantComputed := range wantAttrs {
		attr, ok := resp.Schema.Attributes[name]
		if !ok {
			t.Errorf("missing attribute %q", name)
			continue
		}
		if attr.IsComputed() != wantComputed {
			t.Errorf("attr %q: IsComputed = %v, want %v", name, attr.IsComputed(), wantComputed)
		}
	}
}

// Metadata pins the data source's TF type name. Any change here is a
// user-visible breaking rename.
func TestDataSource_Metadata(t *testing.T) {
	t.Parallel()

	d := New()
	resp := &datasource.MetadataResponse{}
	d.Metadata(t.Context(), datasource.MetadataRequest{ProviderTypeName: "hyperv"}, resp)
	if resp.TypeName != "hyperv_iso_volume" {
		t.Errorf("TypeName = %q, want %q", resp.TypeName, "hyperv_iso_volume")
	}
}

// content_base64, sha256, size_bytes, id are Computed and emit the
// canonical shape iso.Build produces. Pins the data source's wire
// promise -- byte-identical bytes for byte-identical inputs.
func TestDataSource_Read_HappyPath(t *testing.T) {
	t.Parallel()

	files := []iso.File{
		{Name: "meta-data", Content: []byte("instance-id: tfacc-basic\nlocal-hostname: tfacc\n")},
		{Name: "user-data", Content: []byte("#cloud-config\nhostname: tfacc\n")},
	}
	bytesBuilt, err := iso.Build("CIDATA", files)
	if err != nil {
		t.Fatalf("iso.Build: %v", err)
	}
	sum := sha256.Sum256(bytesBuilt)
	wantSha := hex.EncodeToString(sum[:])
	wantB64 := base64.StdEncoding.EncodeToString(bytesBuilt)
	wantSize := int64(len(bytesBuilt))

	// Drive Read at the unit level via the helper that does not need
	// a framework round-trip. The data source's Read body is a thin
	// wrapper over (filesFromMap + iso.Build + base64/sha encoding);
	// exercising that pipeline here gives us confidence without the
	// acc-test machinery.
	filesMap, diags := types.MapValueFrom(context.Background(), types.StringType, map[string]string{
		"meta-data": "instance-id: tfacc-basic\nlocal-hostname: tfacc\n",
		"user-data": "#cloud-config\nhostname: tfacc\n",
	})
	if diags.HasError() {
		t.Fatalf("MapValueFrom: %v", diags)
	}

	got, fdiags := filesFromMap(context.Background(), filesMap)
	if fdiags.HasError() {
		t.Fatalf("filesFromMap: %v", fdiags)
	}
	gotBytes, err := iso.Build("CIDATA", got)
	if err != nil {
		t.Fatalf("iso.Build: %v", err)
	}
	gotSum := sha256.Sum256(gotBytes)
	gotSha := hex.EncodeToString(gotSum[:])
	gotB64 := base64.StdEncoding.EncodeToString(gotBytes)
	gotSize := int64(len(gotBytes))

	if gotSha != wantSha {
		t.Errorf("sha256 = %q, want %q", gotSha, wantSha)
	}
	if gotB64 != wantB64 {
		t.Errorf("content_base64 round-trip mismatch (lengths: got %d, want %d)", len(gotB64), len(wantB64))
	}
	if gotSize != wantSize {
		t.Errorf("size_bytes = %d, want %d", gotSize, wantSize)
	}
}

// filesFromMap converts the model's Map<string, string> into the
// internal/iso File slice the synthesizer consumes, sorted by name.
// Pins the runner-side determinism contract: iteration order on a
// Go map is unspecified, so unsorted input would non-deterministically
// permute the file list and the resulting bytes / sha256.
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
// Mirrors the resource's behavior; the framework defers Read on
// unknown-required, so this path is mostly belt-and-suspenders.
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

// Ensure the schema's MarkdownDescription is populated on every Required
// and Computed attribute (Registry doc generation reads these). A blank
// description would render as an empty cell in the published doc.
func TestDataSource_Schema_AllAttributesDocumented(t *testing.T) {
	t.Parallel()

	d := New()
	resp := &datasource.SchemaResponse{}
	d.Schema(t.Context(), datasource.SchemaRequest{}, resp)

	for name, attr := range resp.Schema.Attributes {
		md := attr.GetMarkdownDescription()
		if md == "" {
			t.Errorf("attr %q has empty MarkdownDescription", name)
		}
	}
}

// Trivial top-level type assertion that catches a future refactor that
// silently changes the framework interface the data source implements.
// Without this, a missing Read method would only surface during a real
// `terraform apply` against the bench.
func TestDataSource_ImplementsFrameworkInterface(t *testing.T) {
	t.Parallel()

	var _ datasource.DataSource = (*DataSource)(nil)
	if _, ok := New().(*DataSource); !ok {
		t.Errorf("New() did not return *DataSource")
	}
}

// Attribute IsRequired() rules: volume_label and files must be Required;
// content_base64 / sha256 / size_bytes / id must be Computed (not
// Required, not Optional).
func TestDataSource_Schema_RequiredVsComputed(t *testing.T) {
	t.Parallel()

	d := New()
	resp := &datasource.SchemaResponse{}
	d.Schema(t.Context(), datasource.SchemaRequest{}, resp)

	required := []string{"volume_label", "files"}
	computed := []string{"content_base64", "sha256", "size_bytes", "id"}

	for _, name := range required {
		attr := resp.Schema.Attributes[name]
		if !attr.IsRequired() {
			t.Errorf("attr %q must be Required", name)
		}
		if attr.IsComputed() {
			t.Errorf("attr %q must NOT be Computed (it's user input)", name)
		}
	}
	for _, name := range computed {
		attr := resp.Schema.Attributes[name]
		if !attr.IsComputed() {
			t.Errorf("attr %q must be Computed", name)
		}
		if attr.IsRequired() {
			t.Errorf("attr %q must NOT be Required (it's a synthesis output)", name)
		}
	}
}

// Belt-and-suspenders: ensure schema.MapAttribute and StringAttribute
// type assertions hold for the documented attributes. A schema-shape
// drift (e.g. files becomes a SetAttribute) would surface here.
func TestDataSource_Schema_AttributeTypes(t *testing.T) {
	t.Parallel()

	d := New()
	resp := &datasource.SchemaResponse{}
	d.Schema(t.Context(), datasource.SchemaRequest{}, resp)

	if _, ok := resp.Schema.Attributes["volume_label"].(schema.StringAttribute); !ok {
		t.Errorf("volume_label is not StringAttribute (got %T)", resp.Schema.Attributes["volume_label"])
	}
	if _, ok := resp.Schema.Attributes["files"].(schema.MapAttribute); !ok {
		t.Errorf("files is not MapAttribute (got %T)", resp.Schema.Attributes["files"])
	}
	if _, ok := resp.Schema.Attributes["sha256"].(schema.StringAttribute); !ok {
		t.Errorf("sha256 is not StringAttribute (got %T)", resp.Schema.Attributes["sha256"])
	}
	if _, ok := resp.Schema.Attributes["size_bytes"].(schema.Int64Attribute); !ok {
		t.Errorf("size_bytes is not Int64Attribute (got %T)", resp.Schema.Attributes["size_bytes"])
	}
}
