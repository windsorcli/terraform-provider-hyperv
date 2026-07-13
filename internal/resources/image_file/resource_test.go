package image_file

import (
	"context"
	"reflect"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/xeitu/terraform-provider-hyperv/internal/hyperv"
	pathtype "github.com/xeitu/terraform-provider-hyperv/internal/types/path"
)

// mustURLObject builds a known types.Object for the URL nested
// attribute from constant fixture values. The URL and checksum are
// inlined because every call site uses the same pair -- the values
// themselves aren't what's under test, only the Object's
// known-non-null state is. Panics via t.Fatal on a diag, since URL
// construction with known strings can't fail in practice.
func mustURLObject(t *testing.T) types.Object {
	t.Helper()
	const (
		fixtureURL      = "https://example.com/foo.iso"
		fixtureChecksum = "sha256:abc"
	)
	obj, diags := URLObjectFromConfig(context.Background(), &URLConfig{
		URL:      types.StringValue(fixtureURL),
		Checksum: types.StringValue(fixtureChecksum),
	})
	if diags.HasError() {
		t.Fatalf("URLObjectFromConfig(%q, %q): %v", fixtureURL, fixtureChecksum, diags)
	}
	return obj
}

// hasPlanModifier checks if any plan-modifier in `mods` has a type whose
// package-qualified name contains `keyword`. Same helper shape as the
// vswitch resource tests use.
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
		"url",
		"local_path",
		"content_base64",
		"replace_while_mounted",
		"sha256",
		"size_bytes",
		"keep_on_destroy",
		"force_destroy",
	}
	for _, name := range wantAttrs {
		if _, ok := resp.Schema.Attributes[name]; !ok {
			t.Errorf("missing attribute %q", name)
		}
	}
}

// destination_path and the url nested block are immutable -- changing
// either must trigger destroy+recreate, not an in-place edit (the resource
// has no in-place mutation path).
func TestResource_Schema_RequiresReplaceOnImmutableAttrs(t *testing.T) {
	t.Parallel()

	r := New()
	resp := &resource.SchemaResponse{}
	r.Schema(t.Context(), resource.SchemaRequest{}, resp)

	dp, ok := resp.Schema.Attributes["destination_path"].(schema.StringAttribute)
	if !ok {
		t.Fatalf("destination_path is not a StringAttribute (got %T)", resp.Schema.Attributes["destination_path"])
	}
	if !hasPlanModifier(dp.PlanModifiers, "RequiresReplace") {
		t.Error(`"destination_path" must carry the RequiresReplace plan modifier`)
	}

	url, ok := resp.Schema.Attributes["url"].(schema.SingleNestedAttribute)
	if !ok {
		t.Fatalf("url is not a SingleNestedAttribute (got %T)", resp.Schema.Attributes["url"])
	}
	if !hasPlanModifier(url.PlanModifiers, "RequiresReplace") {
		t.Error(`"url" must carry the RequiresReplace plan modifier (mode switch == replace)`)
	}
}

// id, sha256, size_bytes are Computed -- UseStateForUnknown keeps plan
// output clean when the user changes nothing relevant. Drift detection
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

// The url nested block requires its `url` sub-attribute when present.
// `checksum` is optional -- when omitted the download is trusted (TLS-only)
// and on-disk SHA still surfaces via the computed `sha256` attribute.
func TestResource_Schema_URLSubAttributesRequired(t *testing.T) {
	t.Parallel()

	r := New()
	resp := &resource.SchemaResponse{}
	r.Schema(t.Context(), resource.SchemaRequest{}, resp)

	url, ok := resp.Schema.Attributes["url"].(schema.SingleNestedAttribute)
	if !ok {
		t.Fatalf("url is not a SingleNestedAttribute (got %T)", resp.Schema.Attributes["url"])
	}

	urlAttr, ok := url.Attributes["url"].(schema.StringAttribute)
	if !ok {
		t.Fatalf("url.url is not a StringAttribute (got %T)", url.Attributes["url"])
	}
	if !urlAttr.Required {
		t.Errorf("url.url must be Required")
	}

	checksumAttr, ok := url.Attributes["checksum"].(schema.StringAttribute)
	if !ok {
		t.Fatalf("url.checksum is not a StringAttribute (got %T)", url.Attributes["checksum"])
	}
	if !checksumAttr.Optional {
		t.Errorf("url.checksum must be Optional (TLS-only trust when omitted)")
	}
	if checksumAttr.Required {
		t.Errorf("url.checksum must not be Required")
	}
}

// `compression` is the new optional codec selector under the url block.
// It must be Optional (not Required -- absence is "no compression, host
// fetches directly"), and it must carry a OneOf validator so a typo like
// "tar.gz" surfaces at plan time, not as a typed-client unsupported-
// codec error at apply.
func TestResource_Schema_URLCompressionOptional(t *testing.T) {
	t.Parallel()

	r := New()
	resp := &resource.SchemaResponse{}
	r.Schema(t.Context(), resource.SchemaRequest{}, resp)

	url, ok := resp.Schema.Attributes["url"].(schema.SingleNestedAttribute)
	if !ok {
		t.Fatalf("url is not a SingleNestedAttribute (got %T)", resp.Schema.Attributes["url"])
	}
	raw, ok := url.Attributes["compression"]
	if !ok {
		t.Fatal("url block missing 'compression' sub-attribute")
	}
	strAttr, ok := raw.(schema.StringAttribute)
	if !ok {
		t.Fatalf("url.compression is not a StringAttribute (got %T)", raw)
	}
	if !strAttr.Optional {
		t.Error("url.compression must be Optional (absence == no compression)")
	}
	if strAttr.Required {
		t.Error("url.compression must NOT be Required")
	}
	if len(strAttr.Validators) == 0 {
		t.Error("url.compression must carry a validator (OneOf gz/gzip/xz/zst/zstd/bz2/bzip2)")
	}
}

// Metadata pins the resource's TF type name. Any change here is a
// user-visible breaking rename.
func TestResource_Metadata(t *testing.T) {
	t.Parallel()

	r := New()
	resp := &resource.MetadataResponse{}
	r.Metadata(t.Context(), resource.MetadataRequest{ProviderTypeName: "hyperv"}, resp)
	if resp.TypeName != "hyperv_image_file" {
		t.Errorf("TypeName = %q, want %q", resp.TypeName, "hyperv_image_file")
	}
}

// Configure with nil ProviderData (validate-time invocation) must NOT panic
// and must NOT error.
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

// sanitizeURLForLog redacts the userinfo on URLs the user supplies, so an
// `https://user:pass@cdn/...` doesn't leak the embedded credentials into
// tflog output (which CI captures, support tickets paste, etc.). The state
// file separately exposes the raw URL -- that's the user's encryption
// concern, not ours -- but logs are a distinct surface.
func TestSanitizeURLForLog(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name, in, want string
	}{
		{"plain https passthrough", "https://example.com/foo.vhdx", "https://example.com/foo.vhdx"},
		{"with userinfo, password redacted", "https://user:pass@cdn.example.com/image.vhdx", "https://REDACTED@cdn.example.com/image.vhdx"},
		{"with userinfo, no password", "https://user@cdn.example.com/image.vhdx", "https://REDACTED@cdn.example.com/image.vhdx"},
		{"http", "http://internal.lan/foo.iso", "http://internal.lan/foo.iso"},
		{"unparsable returns sentinel", "://not a url", "(unparsable url)"},
		// Any query string is redacted wholesale -- pre-signed URLs across
		// every cloud carry their auth in the query, and a known-keys
		// allowlist can't keep up. A bare ?token=abc, an AWS S3 X-Amz-*
		// bundle, an Azure SAS sig/se/sp/sv, a GCP Signature -- all collapse
		// to the same "?REDACTED" output. Even harmless cache-busters get
		// dropped, which is the right tradeoff for fail-closed logging.
		{"generic token query redacted", "https://cdn.example.com/foo.vhdx?token=abc", "https://cdn.example.com/foo.vhdx?REDACTED"},
		{"AWS S3 pre-signed URL redacted", "https://bucket.s3.amazonaws.com/key.vhdx?X-Amz-Algorithm=AWS4-HMAC-SHA256&X-Amz-Credential=AKIA%2F20260427%2Fus-east-1&X-Amz-Signature=deadbeef", "https://bucket.s3.amazonaws.com/key.vhdx?REDACTED"},
		{"Azure Blob SAS token redacted", "https://account.blob.core.windows.net/container/file.vhdx?sv=2024-01-01&se=2026-04-27T00:00:00Z&sp=r&sig=deadbeef%3D", "https://account.blob.core.windows.net/container/file.vhdx?REDACTED"},
		{"GCP signed URL redacted", "https://storage.googleapis.com/bucket/file.vhdx?GoogleAccessId=acc&Expires=1777000000&Signature=deadbeef", "https://storage.googleapis.com/bucket/file.vhdx?REDACTED"},
		{"harmless cache-buster also redacted (acceptable tradeoff)", "https://cdn.example.com/foo.vhdx?v=2", "https://cdn.example.com/foo.vhdx?REDACTED"},
		{"userinfo and query redacted together", "https://user:pass@cdn.example.com/foo.vhdx?sig=abc", "https://REDACTED@cdn.example.com/foo.vhdx?REDACTED"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := sanitizeURLForLog(tc.in)
			if got != tc.want {
				t.Errorf("sanitizeURLForLog(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// stripSha256Prefix is the seam between the user-facing "sha256:<hex>"
// format the schema regex pins and the raw-hex form the wire contract
// expects. Drift here means the wire contract starts seeing the prefix.
func TestStripSha256Prefix(t *testing.T) {
	t.Parallel()

	cases := []struct {
		in, want string
	}{
		{"sha256:abcdef", "abcdef"},
		{"sha256:", "sha256:"},             // too short to be a real prefix-stripped value; passthrough
		{"abcdef", "abcdef"},               // no prefix; passthrough
		{"SHA256:abcdef", "SHA256:abcdef"}, // case-sensitive; the schema regex pins lowercase
		{"sha256:abcdef0123456789abcdef0123456789abcdef0123456789abcdef0123456789", "abcdef0123456789abcdef0123456789abcdef0123456789abcdef0123456789"},
	}
	for _, tc := range cases {
		got := stripSha256Prefix(tc.in)
		if got != tc.want {
			t.Errorf("stripSha256Prefix(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// modelFromImageFile preserves the caller-supplied url block (user intent,
// not derivable from the file on disk) and writes the freshly-read sha256
// + size_bytes to state. URL=nil must round-trip as host_path mode.
func TestModelFromImageFile_PreservesURLBlock(t *testing.T) {
	t.Parallel()

	f := &hyperv.ImageFile{
		Path:      "C:\\images\\foo.vhdx",
		SizeBytes: 5368709120,
		Sha256:    "abcdef0123456789abcdef0123456789abcdef0123456789abcdef0123456789",
	}
	urlObj, diags := URLObjectFromConfig(context.Background(), &URLConfig{
		URL:      types.StringValue("https://example.com/foo.vhdx"),
		Checksum: types.StringValue("sha256:abcdef0123456789abcdef0123456789abcdef0123456789abcdef0123456789"),
	})
	if diags.HasError() {
		t.Fatalf("URLObjectFromConfig: %v", diags)
	}

	got := modelFromImageFile(f, urlObj, pathtype.NewPathNull(), types.StringNull(), types.BoolNull(), types.BoolNull(), types.BoolNull())

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
	gotURL, diags := got.URLConfig(context.Background())
	if diags.HasError() {
		t.Fatalf("got.URLConfig: %v", diags)
	}
	if gotURL == nil {
		t.Fatal("URLConfig() = nil; caller-supplied url block must be preserved")
	}
	if gotURL.URL.ValueString() != "https://example.com/foo.vhdx" {
		t.Errorf("URLConfig().URL = %q, want passthrough", gotURL.URL.ValueString())
	}
}

func TestModelFromImageFile_HostPathModePreservesNilURL(t *testing.T) {
	t.Parallel()

	f := &hyperv.ImageFile{
		Path:      "C:\\share\\already.vhdx",
		SizeBytes: 1024,
		Sha256:    "0000000000000000000000000000000000000000000000000000000000000000",
	}

	got := modelFromImageFile(f, types.ObjectNull(URLAttrTypes), pathtype.NewPathNull(), types.StringNull(), types.BoolNull(), types.BoolNull(), types.BoolNull())

	if !got.URL.IsNull() {
		t.Errorf("URL = %+v, want null Object (host_path mode)", got.URL)
	}
	if !got.LocalPath.IsNull() {
		t.Errorf("LocalPath = %v, want null (host_path mode)", got.LocalPath)
	}
}

// TestModelFromImageFile_PreservesLocalPath round-trips the user-supplied
// local_path through Read, parallel to TestModelFromImageFile_PreservesURLBlock
// for url-mode. The file's Path on disk matches DestinationPath (canonical
// form), but local_path comes from caller config and isn't reconstructible
// from disk -- it has to be threaded through.
func TestModelFromImageFile_PreservesLocalPath(t *testing.T) {
	t.Parallel()

	f := &hyperv.ImageFile{
		Path:      "C:\\images\\foo.iso",
		SizeBytes: 387072,
		Sha256:    "abcdef0123456789abcdef0123456789abcdef0123456789abcdef0123456789",
	}
	localPath := pathtype.NewPathValue("/Users/me/dist/foo.iso")

	got := modelFromImageFile(f, types.ObjectNull(URLAttrTypes), localPath, types.StringNull(), types.BoolNull(), types.BoolNull(), types.BoolNull())

	if !got.URL.IsNull() {
		t.Errorf("URL = %+v, want null Object (local_path mode)", got.URL)
	}
	if got.LocalPath.ValueString() != "/Users/me/dist/foo.iso" {
		t.Errorf("LocalPath = %q, want %q", got.LocalPath.ValueString(), "/Users/me/dist/foo.iso")
	}
}

// TestModelFromImageFile_PreservesForceDestroy round-trips the caller-
// supplied force_destroy through Read/Create/Update. Symmetric with
// keep_on_destroy: the bench has no concept of this flag, so
// modelFromImageFile must thread the caller's value. Without it, state
// holds null, Delete reads null, ValueBool() returns false, and the
// detach-then-retry branch in remove.ps1 never fires -- the feature
// is silently a no-op.
func TestModelFromImageFile_PreservesForceDestroy(t *testing.T) {
	t.Parallel()

	f := &hyperv.ImageFile{
		Path:      "C:\\images\\seed.iso",
		SizeBytes: 387072,
		Sha256:    "abcdef0123456789abcdef0123456789abcdef0123456789abcdef0123456789",
	}

	got := modelFromImageFile(f, types.ObjectNull(URLAttrTypes), pathtype.NewPathValue("/Users/me/dist/seed.iso"), types.StringNull(), types.BoolNull(), types.BoolNull(), types.BoolValue(true))

	if got.ForceDestroy.IsNull() {
		t.Fatal("ForceDestroy = null; caller-supplied value must be preserved (Delete reads ValueBool() on this)")
	}
	if !got.ForceDestroy.ValueBool() {
		t.Errorf("ForceDestroy = false, want true (caller passed types.BoolValue(true))")
	}
}

// TestModelFromImageFile_PreservesKeepOnDestroy round-trips the caller-
// supplied keep_on_destroy through Read/Create/Update. The bench has
// no concept of this flag (it's a Terraform-only destroy-behavior
// switch), so modelFromImageFile must thread the caller's value into
// the returned model. Without this, state holds null after every
// Create, Delete reads null, ValueBool() returns false, the early-
// return branch never fires, and the file is deleted regardless of
// the user's config -- the entire feature is silently a no-op.
func TestModelFromImageFile_PreservesKeepOnDestroy(t *testing.T) {
	t.Parallel()

	f := &hyperv.ImageFile{
		Path:      "C:\\images\\cached.iso",
		SizeBytes: 5044094976,
		Sha256:    "abcdef0123456789abcdef0123456789abcdef0123456789abcdef0123456789",
	}

	got := modelFromImageFile(f, types.ObjectNull(URLAttrTypes), pathtype.NewPathValue("/Users/me/dist/cached.iso"), types.StringNull(), types.BoolNull(), types.BoolValue(true), types.BoolNull())

	if got.KeepOnDestroy.IsNull() {
		t.Fatal("KeepOnDestroy = null; caller-supplied value must be preserved (Delete reads ValueBool() on this)")
	}
	if !got.KeepOnDestroy.ValueBool() {
		t.Errorf("KeepOnDestroy = false, want true (caller passed types.BoolValue(true))")
	}
}

// Schema test: local_path is present and carries RequiresReplace. The
// path-string change forcing replace is load-bearing -- without it,
// switching to a different source file would silently re-stream into
// the same destination, conflating identity and content.
func TestResource_Schema_LocalPathRequiresReplace(t *testing.T) {
	t.Parallel()

	r := New()
	resp := &resource.SchemaResponse{}
	r.Schema(t.Context(), resource.SchemaRequest{}, resp)

	lp, ok := resp.Schema.Attributes["local_path"].(schema.StringAttribute)
	if !ok {
		t.Fatalf("local_path is not a StringAttribute (got %T)", resp.Schema.Attributes["local_path"])
	}
	if !lp.Optional {
		t.Error(`"local_path" must be Optional`)
	}
	if !hasPlanModifier(lp.PlanModifiers, "RequiresReplace") {
		t.Error(`"local_path" must carry RequiresReplace (path-string change forces replace)`)
	}
}

// TestResource_Schema_ForceDestroy pins the force_destroy attribute's
// shape: Optional+Computed with a static-false default and
// UseStateForUnknown -- symmetric with keep_on_destroy. Critically
// asserts the absence of a RequiresReplace modifier: toggling
// force_destroy is an in-place attribute change, not a destroy+recreate
// (which would defeat the purpose of the flag in the cross-module-
// destroy case that motivates it).
func TestResource_Schema_ForceDestroy(t *testing.T) {
	t.Parallel()

	r := New()
	resp := &resource.SchemaResponse{}
	r.Schema(t.Context(), resource.SchemaRequest{}, resp)

	raw, ok := resp.Schema.Attributes["force_destroy"]
	if !ok {
		t.Fatal(`missing attribute "force_destroy"`)
	}
	attr, ok := raw.(schema.BoolAttribute)
	if !ok {
		t.Fatalf("force_destroy is not a BoolAttribute (got %T)", raw)
	}
	if !attr.Optional {
		t.Error(`"force_destroy" must be Optional`)
	}
	if !attr.Computed {
		t.Error(`"force_destroy" must be Computed (default carries through unset configs)`)
	}
	if attr.Default == nil {
		t.Error(`"force_destroy" must carry a Default (false), or null configs surface as null instead of false`)
	}
	if !hasPlanModifier(attr.PlanModifiers, "UseStateForUnknown") {
		t.Error(`"force_destroy" must carry UseStateForUnknown`)
	}
	if hasPlanModifier(attr.PlanModifiers, "RequiresReplace") {
		t.Error(`"force_destroy" must NOT carry RequiresReplace -- toggling the flag is an in-place change`)
	}
}

// TestResource_Schema_KeepOnDestroy pins the keep_on_destroy attribute's
// shape: Optional+Computed (so users can omit it; framework fills in the
// default) with a static-false default and UseStateForUnknown so plan
// stays clean across applies that don't touch the flag.
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
		t.Error(`"keep_on_destroy" must carry a Default (false), or null configs surface as null instead of false`)
	}
	if !hasPlanModifier(attr.PlanModifiers, "UseStateForUnknown") {
		t.Error(`"keep_on_destroy" must carry UseStateForUnknown`)
	}
}

// ConfigValidators registers exactly the validators the resource relies on.
// Drift here means a validator was silently dropped (or one was added
// without the corresponding plan-time guard test).
func TestResource_ConfigValidators_RegistersAll(t *testing.T) {
	t.Parallel()

	r, ok := New().(*Resource)
	if !ok {
		t.Fatal("New() did not return *Resource")
	}
	got := r.ConfigValidators(t.Context())
	if len(got) != 1 {
		t.Fatalf("ConfigValidators = %d, want 1 (url + local_path conflict)", len(got))
	}
	if _, ok := got[0].(sourceModeExclusivityValidator); !ok {
		t.Errorf("ConfigValidators[0] = %T, want sourceModeExclusivityValidator", got[0])
	}
}

// TestSourceModeExclusivityValidator covers the config shapes the
// validator must distinguish across the three mutually-exclusive
// placement-mode discriminators (url, local_path, content_base64).
// Each pairwise conflict and the all-three-set case must reject;
// any single-set or all-unset case must allow.
func TestSourceModeExclusivityValidator(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name      string
		model     Model
		wantError bool
	}{
		{
			name: "both url and local_path set rejects",
			model: Model{
				URL:       mustURLObject(t),
				LocalPath: pathtype.NewPathValue("/tmp/foo.iso"),
			},
			wantError: true,
		},
		{
			name: "only url set allows",
			model: Model{
				URL:       mustURLObject(t),
				LocalPath: pathtype.NewPathNull(),
			},
			wantError: false,
		},
		{
			name: "only local_path set allows",
			model: Model{
				URL:       types.ObjectNull(URLAttrTypes),
				LocalPath: pathtype.NewPathValue("/tmp/foo.iso"),
			},
			wantError: false,
		},
		{
			name: "neither set allows (host_path mode)",
			model: Model{
				URL:       types.ObjectNull(URLAttrTypes),
				LocalPath: pathtype.NewPathNull(),
			},
			wantError: false,
		},
		{
			name: "unknown local_path treated as unset (deferred dependency)",
			model: Model{
				URL:       mustURLObject(t),
				LocalPath: pathtype.NewPathUnknown(),
			},
			wantError: false,
		},
		{
			name: "unknown url treated as unset (deferred dependency)",
			model: Model{
				URL:       types.ObjectUnknown(URLAttrTypes),
				LocalPath: pathtype.NewPathValue("/tmp/foo.iso"),
			},
			wantError: false,
		},
		{
			name: "url + content_base64 rejects",
			model: Model{
				URL:           mustURLObject(t),
				LocalPath:     pathtype.NewPathNull(),
				ContentBase64: types.StringValue("Zm9v"),
			},
			wantError: true,
		},
		{
			name: "local_path + content_base64 rejects",
			model: Model{
				URL:           types.ObjectNull(URLAttrTypes),
				LocalPath:     pathtype.NewPathValue("/tmp/foo.iso"),
				ContentBase64: types.StringValue("Zm9v"),
			},
			wantError: true,
		},
		{
			name: "all three set rejects",
			model: Model{
				URL:           mustURLObject(t),
				LocalPath:     pathtype.NewPathValue("/tmp/foo.iso"),
				ContentBase64: types.StringValue("Zm9v"),
			},
			wantError: true,
		},
		{
			name: "only content_base64 set allows (literal_bytes mode)",
			model: Model{
				URL:           types.ObjectNull(URLAttrTypes),
				LocalPath:     pathtype.NewPathNull(),
				ContentBase64: types.StringValue("Zm9v"),
			},
			wantError: false,
		},
		{
			name: "unknown content_base64 treated as unset (deferred dependency)",
			model: Model{
				URL:           types.ObjectNull(URLAttrTypes),
				LocalPath:     pathtype.NewPathValue("/tmp/foo.iso"),
				ContentBase64: types.StringUnknown(),
			},
			wantError: false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := sourceModeExclusivityValidator{}.validate(tc.model)
			if got.HasError() != tc.wantError {
				t.Errorf("validate(...).HasError() = %v, want %v\nfull diags: %v",
					got.HasError(), tc.wantError, got)
			}
			if tc.wantError && len(got) > 0 {
				// Diagnostic should anchor to local_path so the user lands on
				// the more recently introduced surface and reads "remove
				// local_path or remove url" rather than the inverse.
				if got[0].Summary() == "" || !strings.Contains(got[0].Summary(), "mutually exclusive") {
					t.Errorf("diag summary = %q, want substring 'mutually exclusive'", got[0].Summary())
				}
			}
		})
	}
}
