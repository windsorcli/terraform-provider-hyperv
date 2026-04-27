// Package image_file implements the hyperv_image_file resource. Wraps the
// image_file/{get,new,remove}.ps1 contract via the typed hyperv.Client.
package image_file //nolint:revive // underscore in package name mirrors the script directory it wraps.

import "github.com/hashicorp/terraform-plugin-framework/types"

// Model is the tfsdk-bound struct backing the resource state. Field tags
// align with schema.go attribute names; conversion to/from the typed
// hyperv.ImageFile DTO lives in resource.go.
//
// URL is a pointer so its presence/absence acts as the source-mode
// discriminator: non-nil => url-mode (HttpClient fetch), nil =>
// host_path-mode (verify-only). Mode switches between configs trigger
// RequiresReplace at the schema layer; Delete keys on this same nil
// check to gate the host-side remove.
type Model struct {
	ID              types.String `tfsdk:"id"`
	DestinationPath types.String `tfsdk:"destination_path"`
	URL             *URLConfig   `tfsdk:"url"`
	Sha256          types.String `tfsdk:"sha256"`
	SizeBytes       types.Int64  `tfsdk:"size_bytes"`
}

// URLConfig is the user-supplied URL-mode source configuration. Both fields
// are required when the block is present; the schema-layer Required flag
// enforces this without a separate config validator.
type URLConfig struct {
	URL      types.String `tfsdk:"url"`
	Checksum types.String `tfsdk:"checksum"`
}
