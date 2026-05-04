// Package image_file implements the hyperv_image_file resource. Wraps the
// image_file/{get,new,remove}.ps1 contract via the typed hyperv.Client.
package image_file //nolint:revive // underscore in package name mirrors the script directory it wraps.

import (
	"github.com/hashicorp/terraform-plugin-framework/types"

	pathtype "github.com/windsorcli/terraform-provider-hyperv/internal/types/path"
)

// Model is the tfsdk-bound struct backing the resource state. Field tags
// align with schema.go attribute names; conversion to/from the typed
// hyperv.ImageFile DTO lives in resource.go.
//
// Three source modes, discriminated by which of URL / LocalPath is set:
//
//   - URL non-nil                     => url mode (HttpClient fetch)
//   - URL nil, LocalPath non-null     => local_path mode (runner streams
//     bytes via Connection.StreamFile)
//   - URL nil, LocalPath null         => host_path mode (verify only)
//
// URL and LocalPath are mutually exclusive; the ConfigValidator on the
// resource rejects configs that set both. Both URL and LocalPath carry
// RequiresReplace at the schema layer, so any mode switch destroys
// and recreates. The Delete path keys on the same discriminators to
// gate the host-side remove (host_path mode never removes, since the
// user attested the file already existed).
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
	ID              pathtype.Path `tfsdk:"id"`
	DestinationPath pathtype.Path `tfsdk:"destination_path"`
	URL             *URLConfig    `tfsdk:"url"`
	LocalPath       pathtype.Path `tfsdk:"local_path"`
	Sha256          types.String  `tfsdk:"sha256"`
	SizeBytes       types.Int64   `tfsdk:"size_bytes"`
	KeepOnDestroy   types.Bool    `tfsdk:"keep_on_destroy"`
}

// URLConfig is the user-supplied URL-mode source configuration. Both fields
// are required when the block is present; the schema-layer Required flag
// enforces this without a separate config validator.
type URLConfig struct {
	URL      types.String `tfsdk:"url"`
	Checksum types.String `tfsdk:"checksum"`
}
