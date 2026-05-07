// Package iso_volume implements hyperv_iso_volume -- a deterministic
// ISO9660 image (volume label + files map) the provider builds on the
// runner and places on the Hyper-V host. Wraps the same image_file/
// {get,new,remove}.ps1 wire contract image_file's local_path mode uses;
// the only iso_volume-specific code is the runner-side BuildISO step.
package iso_volume //nolint:revive // underscore in package name mirrors the resource type name it backs.

import (
	"github.com/hashicorp/terraform-plugin-framework/types"

	pathtype "github.com/windsorcli/terraform-provider-hyperv/internal/types/path"
)

// Model is the tfsdk-bound struct backing the resource state. Field tags
// align with schema.go attribute names; conversion to/from the typed
// hyperv.IsoVolume DTO lives in resource.go.
//
// VolumeLabel and Files are user intent (config) and aren't reconstructible
// from the file on disk, so Read must thread the prior-state values through
// rather than try to recover them from the bytes. ID and DestinationPath
// use the pathtype.Path custom type for the same slash-folding reason as
// hyperv_image_file.
type Model struct {
	ID              pathtype.Path `tfsdk:"id"`
	DestinationPath pathtype.Path `tfsdk:"destination_path"`
	VolumeLabel     types.String  `tfsdk:"volume_label"`
	Files           types.Map     `tfsdk:"files"`
	Sha256          types.String  `tfsdk:"sha256"`
	SizeBytes       types.Int64   `tfsdk:"size_bytes"`
	KeepOnDestroy   types.Bool    `tfsdk:"keep_on_destroy"`
}
