package image_file

import (
	"reflect"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/windsorcli/terraform-provider-hyperv/internal/hyperv"
	pathtype "github.com/windsorcli/terraform-provider-hyperv/internal/types/path"
)

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

// The url nested block requires both sub-attributes when present. The
// schema-layer Required flag enforces this without a separate validator.
func TestResource_Schema_URLSubAttributesRequired(t *testing.T) {
	t.Parallel()

	r := New()
	resp := &resource.SchemaResponse{}
	r.Schema(t.Context(), resource.SchemaRequest{}, resp)

	url, ok := resp.Schema.Attributes["url"].(schema.SingleNestedAttribute)
	if !ok {
		t.Fatalf("url is not a SingleNestedAttribute (got %T)", resp.Schema.Attributes["url"])
	}
	for _, sub := range []string{"url", "checksum"} {
		raw, ok := url.Attributes[sub]
		if !ok {
			t.Errorf("url block missing sub-attribute %q", sub)
			continue
		}
		strAttr, ok := raw.(schema.StringAttribute)
		if !ok {
			t.Errorf("url.%s is not a StringAttribute (got %T)", sub, raw)
			continue
		}
		if !strAttr.Required {
			t.Errorf("url.%s must be Required (regex validators key on its presence)", sub)
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
	url := &URLConfig{
		URL:      types.StringValue("https://example.com/foo.vhdx"),
		Checksum: types.StringValue("sha256:abcdef0123456789abcdef0123456789abcdef0123456789abcdef0123456789"),
	}

	got := modelFromImageFile(f, url, pathtype.NewPathNull())

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
	if got.URL == nil {
		t.Fatal("URL = nil; caller-supplied url block must be preserved")
	}
	if got.URL.URL.ValueString() != "https://example.com/foo.vhdx" {
		t.Errorf("URL.URL = %q, want passthrough", got.URL.URL.ValueString())
	}
}

func TestModelFromImageFile_HostPathModePreservesNilURL(t *testing.T) {
	t.Parallel()

	f := &hyperv.ImageFile{
		Path:      "C:\\share\\already.vhdx",
		SizeBytes: 1024,
		Sha256:    "0000000000000000000000000000000000000000000000000000000000000000",
	}

	got := modelFromImageFile(f, nil, pathtype.NewPathNull())

	if got.URL != nil {
		t.Errorf("URL = %+v, want nil (host_path mode)", got.URL)
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

	got := modelFromImageFile(f, nil, localPath)

	if got.URL != nil {
		t.Errorf("URL = %+v, want nil (local_path mode)", got.URL)
	}
	if got.LocalPath.ValueString() != "/Users/me/dist/foo.iso" {
		t.Errorf("LocalPath = %q, want %q", got.LocalPath.ValueString(), "/Users/me/dist/foo.iso")
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
	if _, ok := got[0].(urlAndLocalPathConflictValidator); !ok {
		t.Errorf("ConfigValidators[0] = %T, want urlAndLocalPathConflictValidator", got[0])
	}
}

// TestUrlAndLocalPathConflictValidator covers the four config shapes the
// validator must distinguish: both set (rejected), only url (allowed),
// only local_path (allowed), neither (allowed -- host_path mode).
func TestUrlAndLocalPathConflictValidator(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name      string
		model     Model
		wantError bool
	}{
		{
			name: "both url and local_path set rejects",
			model: Model{
				URL: &URLConfig{
					URL:      types.StringValue("https://example.com/foo.iso"),
					Checksum: types.StringValue("sha256:abc"),
				},
				LocalPath: pathtype.NewPathValue("/tmp/foo.iso"),
			},
			wantError: true,
		},
		{
			name: "only url set allows",
			model: Model{
				URL: &URLConfig{
					URL:      types.StringValue("https://example.com/foo.iso"),
					Checksum: types.StringValue("sha256:abc"),
				},
				LocalPath: pathtype.NewPathNull(),
			},
			wantError: false,
		},
		{
			name: "only local_path set allows",
			model: Model{
				URL:       nil,
				LocalPath: pathtype.NewPathValue("/tmp/foo.iso"),
			},
			wantError: false,
		},
		{
			name: "neither set allows (host_path mode)",
			model: Model{
				URL:       nil,
				LocalPath: pathtype.NewPathNull(),
			},
			wantError: false,
		},
		{
			name: "unknown local_path treated as unset (deferred dependency)",
			model: Model{
				URL: &URLConfig{
					URL:      types.StringValue("https://example.com/foo.iso"),
					Checksum: types.StringValue("sha256:abc"),
				},
				LocalPath: pathtype.NewPathUnknown(),
			},
			wantError: false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := urlAndLocalPathConflictValidator{}.validate(tc.model)
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
