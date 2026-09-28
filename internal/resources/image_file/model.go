// Package image_file implements the hyperv_image_file resource. Wraps the
// image_file/{get,new,remove}.ps1 contract via the typed hyperv.Client.
package image_file //nolint:revive // underscore in package name mirrors the script directory it wraps.

import (
	"context"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-framework/types/basetypes"

	pathtype "github.com/windsorcli/terraform-provider-hyperv/internal/types/path"
)

// Model is the tfsdk-bound struct backing the resource state. Field
// tags align with schema.go; conversion to/from the typed
// hyperv.ImageFile DTO lives in resource.go. Five source modes are
// discriminated by which of URL / LocalPath / ContentBase64 /
// SourcePath is set (all four nil/null means host_path mode, verify
// only); they're mutually exclusive per the resource's ConfigValidator
// and each carries RequiresReplace, so any mode switch destroys and
// recreates. Delete keys on the same discriminators: host_path mode
// never removes the file, since the user attested it already existed.
type Model struct {
	// ID mirrors DestinationPath's value and type, so the same semantic-equality covers its Computed refresh.
	ID pathtype.Path `tfsdk:"id"`

	// DestinationPath, LocalPath, and SourcePath use pathtype.Path so
	// users can write either slash form without the framework rejecting
	// the apply when Hyper-V echoes back backslashes.
	DestinationPath pathtype.Path `tfsdk:"destination_path"`
	URL             types.Object  `tfsdk:"url"`
	LocalPath       pathtype.Path `tfsdk:"local_path"`
	ContentBase64   types.String  `tfsdk:"content_base64"`
	SourcePath      pathtype.Path `tfsdk:"source_path"`

	// ReplaceWhileMounted opts into re-streaming over a destination
	// currently mounted as a DVD on a running VM; honored only in
	// local_path, literal_bytes, and source_path modes, the ones with a
	// rewrite Update path.
	ReplaceWhileMounted types.Bool `tfsdk:"replace_while_mounted"`

	Sha256        types.String `tfsdk:"sha256"`
	SizeBytes     types.Int64  `tfsdk:"size_bytes"`
	KeepOnDestroy types.Bool   `tfsdk:"keep_on_destroy"`

	// ForceDestroy opts into detaching a destination mounted as a DVD
	// before deleting it; honored in every mode that runs the host-side
	// delete, a no-op in host_path mode since that mode never deletes.
	ForceDestroy types.Bool `tfsdk:"force_destroy"`
}

// URLConfig is the user-supplied URL-mode source configuration. url is
// required when the block is present; checksum is optional (omitted
// trusts TLS-only); compression is optional (absent fetches directly
// on the host, present routes through a runner-pipelined
// download-and-decompress). The Model carries url as types.Object,
// not *URLConfig, since a pointer-to-struct can represent null but not
// unknown, and unknown is exactly what the framework marshals when
// this comes from an unresolved parent variable (e.g. each.value.url
// before for_each materializes); the helpers below give typed access
// once it's known.
type URLConfig struct {
	URL            types.String `tfsdk:"url"`
	Checksum       types.String `tfsdk:"checksum"`
	Compression    types.String `tfsdk:"compression"`
	RunnerDownload types.Bool   `tfsdk:"runner_download"`
}

// URLAttrTypes mirrors the SingleNestedAttribute "url" fields in
// schema.go. Used by types.Object construction (ObjectValueFrom) and
// decode (Object.As).
var URLAttrTypes = map[string]attr.Type{
	"url":             types.StringType,
	"checksum":        types.StringType,
	"compression":     types.StringType,
	"runner_download": types.BoolType,
}

// URLConfig returns the decoded user-supplied URL config, or nil if
// the model's URL is null or unknown. Callers that need to distinguish
// null from unknown should inspect m.URL directly via IsNull / IsUnknown.
func (m *Model) URLConfig(ctx context.Context) (*URLConfig, diag.Diagnostics) {
	if m.URL.IsNull() || m.URL.IsUnknown() {
		return nil, nil
	}
	var u URLConfig
	diags := m.URL.As(ctx, &u, basetypes.ObjectAsOptions{})
	if diags.HasError() {
		return nil, diags
	}
	return &u, nil
}

// URLObjectFromConfig builds a types.Object from a *URLConfig. A nil
// pointer becomes a null Object (matching the "URL not set" semantics
// of the previous *URLConfig field). Used by modelFromImageFile and
// any test code that constructs a Model with a known URL block.
func URLObjectFromConfig(ctx context.Context, u *URLConfig) (types.Object, diag.Diagnostics) {
	if u == nil {
		return types.ObjectNull(URLAttrTypes), nil
	}
	return types.ObjectValueFrom(ctx, URLAttrTypes, u)
}
