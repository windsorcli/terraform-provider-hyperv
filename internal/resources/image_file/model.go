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

// Model is the tfsdk-bound struct backing the resource state. Field tags
// align with schema.go attribute names; conversion to/from the typed
// hyperv.ImageFile DTO lives in resource.go.
//
// Four source modes, discriminated by which of URL / LocalPath /
// ContentBase64 is set:
//
//   - URL non-nil                              => url mode (HttpClient fetch)
//   - URL nil, LocalPath non-null              => local_path mode (runner streams
//     bytes from a runner-side file via Connection.StreamFile)
//   - URL nil, LocalPath null, ContentBase64
//     non-null                                 => literal_bytes mode (runner
//     decodes base64, writes to a tmpfile, streams via the same wire path
//     local_path mode uses)
//   - all three nil/null                       => host_path mode (verify only)
//
// All three placement-mode discriminators (URL, LocalPath, ContentBase64)
// are mutually exclusive; the ConfigValidator on the resource rejects
// configs that set more than one. All three carry RequiresReplace at
// the schema layer, so any mode switch destroys and recreates. The
// Delete path keys on the same discriminators to gate the host-side
// remove (host_path mode never removes, since the user attested the
// file already existed).
//
// ReplaceWhileMounted is the opt-in escape hatch for re-streaming over
// a destination that's currently mounted as a DVD on a running VM. Only
// honored in local_path and literal_bytes modes -- the modes with a
// re-stream Update path; url-mode forces replacement on any change, and
// host_path-mode never writes the destination.
//
// ForceDestroy is the opt-in escape hatch for destroying a file that is
// currently mounted as a DVD on a running VM. When true, Delete asks
// remove.ps1 to detach the holding slot(s) via Set-VMDvdDrive -Path
// $null before retrying the delete. Honored in url, local_path, and
// literal_bytes modes -- those are the modes that actually run the
// host-side delete. Host_path-mode skips the delete entirely so the
// flag is a no-op there.
//
// DestinationPath uses the pathtype.Path custom type so users can
// write either `C:/foo` or `C:\foo` without the framework rejecting
// the apply with "Provider produced inconsistent result after apply"
// when Hyper-V returns the canonical backslash form. ID mirrors
// destination_path and is also Path so the same semantic-equality
// covers the Computed mirror's refresh path. LocalPath uses Path
// for the same slash-folding reason -- users on macOS / Linux runners
// typically write forward slashes for the local path even when the
// destination is a Windows-form path.
type Model struct {
	ID                  pathtype.Path `tfsdk:"id"`
	DestinationPath     pathtype.Path `tfsdk:"destination_path"`
	URL                 types.Object  `tfsdk:"url"`
	LocalPath           pathtype.Path `tfsdk:"local_path"`
	ContentBase64       types.String  `tfsdk:"content_base64"`
	ReplaceWhileMounted types.Bool    `tfsdk:"replace_while_mounted"`
	Sha256              types.String  `tfsdk:"sha256"`
	SizeBytes           types.Int64   `tfsdk:"size_bytes"`
	KeepOnDestroy       types.Bool    `tfsdk:"keep_on_destroy"`
	ForceDestroy        types.Bool    `tfsdk:"force_destroy"`
}

// URLConfig is the user-supplied URL-mode source configuration.
// `url` is required when the block is present; `checksum` is optional
// (when omitted, the download is trusted TLS-only). `compression` is
// optional -- absence means "no decompression, host fetches directly";
// presence flips the typed client to a runner-pipelined fetch (download +
// decompress on the runner, then stream decompressed bytes to the host).
//
// The Model carries `url` as types.Object rather than *URLConfig because
// the framework's pointer-to-struct shape can represent null (nil) but
// not unknown -- and unknown is exactly what the framework marshals
// when the attribute is driven from a parent variable that hasn't
// resolved yet (e.g. each.value.url before for_each materializes).
// types.Object handles all three states (known/null/unknown), and the
// helpers below give resource code typed access when the value is known.
type URLConfig struct {
	URL         types.String `tfsdk:"url"`
	Checksum    types.String `tfsdk:"checksum"`
	Compression types.String `tfsdk:"compression"`
}

// URLAttrTypes mirrors the SingleNestedAttribute "url" shape in
// schema.go. Used by types.Object construction (ObjectValueFrom) and
// decode (Object.As).
var URLAttrTypes = map[string]attr.Type{
	"url":         types.StringType,
	"checksum":    types.StringType,
	"compression": types.StringType,
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
