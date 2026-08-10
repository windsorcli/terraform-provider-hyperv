// Package vhd implements the hyperv_vhd resource. Wraps the
// vhd/{get,new,set,remove}.ps1 contract via the typed hyperv.Client.
package vhd

import (
	"github.com/hashicorp/terraform-plugin-framework/types"

	pathtype "github.com/windsorcli/terraform-provider-hyperv/internal/types/path"
)

// Model is the tfsdk-bound struct backing the resource state. Field tags
// align with schema.go attribute names; conversion to/from the typed
// hyperv.VHD DTO lives in resource.go.
//
// vhd_type is "fixed" | "dynamic" | "differencing" on the schema/wire-stdin
// side; Get-VHD's VhdType property emits PascalCase ("Fixed"/"Dynamic"/
// "Differencing") on the wire-stdout side. modelFromVHD lowercases when
// hydrating from the cmdlet read-back.
//
// Path, ParentPath, and SourcePath use the pathtype.Path custom type so
// users can write either `C:/foo` or `C:\foo` without the framework
// rejecting the apply with "Provider produced inconsistent result after
// apply" when Hyper-V returns the canonical backslash form.
//
// SourcePath selects a fourth mode alongside the three vhd_type layouts:
// rather than creating a disk, the host copies an existing one and
// optionally grows it to SizeBytes. SourceSha256 records the hash of the
// source as of the last copy, which is what makes an upstream image
// refreshed in place surface as a diff.
//
// SourceSha256 deliberately tracks the *source*, not the disk at Path.
// A copied boot disk diverges from its source the moment its VM writes to
// it, and growing it changes the bytes too -- comparing the two would
// re-copy on every apply and wipe the guest.
type Model struct {
	ID             pathtype.Path `tfsdk:"id"`
	Path           pathtype.Path `tfsdk:"path"`
	VhdType        types.String  `tfsdk:"vhd_type"`
	SizeBytes      types.Int64   `tfsdk:"size_bytes"`
	ParentPath     pathtype.Path `tfsdk:"parent_path"`
	SourcePath     pathtype.Path `tfsdk:"source_path"`
	SourceSha256   types.String  `tfsdk:"source_sha256"`
	BlockSizeBytes types.Int64   `tfsdk:"block_size_bytes"`
	FileSizeBytes  types.Int64   `tfsdk:"file_size_bytes"`
	Format         types.String  `tfsdk:"format"`
	Attached       types.Bool    `tfsdk:"attached"`
}
