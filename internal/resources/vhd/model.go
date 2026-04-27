// Package vhd implements the hyperv_vhd resource. Wraps the
// vhd/{get,new,set,remove}.ps1 contract via the typed hyperv.Client.
package vhd

import "github.com/hashicorp/terraform-plugin-framework/types"

// Model is the tfsdk-bound struct backing the resource state. Field tags
// align with schema.go attribute names; conversion to/from the typed
// hyperv.VHD DTO lives in resource.go.
//
// vhd_type is "fixed" | "dynamic" | "differencing" on the schema/wire-stdin
// side; Get-VHD's VhdType property emits PascalCase ("Fixed"/"Dynamic"/
// "Differencing") on the wire-stdout side. modelFromVHD lowercases when
// hydrating from the cmdlet read-back.
type Model struct {
	ID             types.String `tfsdk:"id"`
	Path           types.String `tfsdk:"path"`
	VhdType        types.String `tfsdk:"vhd_type"`
	SizeBytes      types.Int64  `tfsdk:"size_bytes"`
	ParentPath     types.String `tfsdk:"parent_path"`
	BlockSizeBytes types.Int64  `tfsdk:"block_size_bytes"`
	FileSizeBytes  types.Int64  `tfsdk:"file_size_bytes"`
	Format         types.String `tfsdk:"format"`
	Attached       types.Bool   `tfsdk:"attached"`
}
