// Package vhd implements the hyperv_vhd resource. Wraps the
// vhd/{get,new,set,remove}.ps1 contract via the typed hyperv.Client.
package vhd

import (
	"github.com/hashicorp/terraform-plugin-framework/types"

	pathtype "github.com/xeitu/terraform-provider-hyperv/internal/types/path"
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
// Path and ParentPath use the pathtype.Path custom type so users can
// write either `C:/foo` or `C:\foo` without the framework rejecting
// the apply with "Provider produced inconsistent result after apply"
// when Hyper-V returns the canonical backslash form.
type Model struct {
	ID             pathtype.Path `tfsdk:"id"`
	Path           pathtype.Path `tfsdk:"path"`
	VhdType        types.String  `tfsdk:"vhd_type"`
	SizeBytes      types.Int64   `tfsdk:"size_bytes"`
	ParentPath     pathtype.Path `tfsdk:"parent_path"`
	SourcePath     pathtype.Path `tfsdk:"source_path"`
	KeepOnDestroy  types.Bool    `tfsdk:"keep_on_destroy"`
	BlockSizeBytes types.Int64   `tfsdk:"block_size_bytes"`
	FileSizeBytes  types.Int64   `tfsdk:"file_size_bytes"`
	Format         types.String  `tfsdk:"format"`
	Attached       types.Bool    `tfsdk:"attached"`
}
