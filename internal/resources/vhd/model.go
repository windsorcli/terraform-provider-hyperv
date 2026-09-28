// Package vhd implements the hyperv_vhd resource. Wraps the
// vhd/{get,new,set,remove}.ps1 contract via the typed hyperv.Client.
package vhd

import (
	"github.com/hashicorp/terraform-plugin-framework/types"

	pathtype "github.com/windsorcli/terraform-provider-hyperv/internal/types/path"
)

// Model is the tfsdk-bound struct backing the resource state. Field
// tags align with schema.go; conversion to/from the typed hyperv.VHD
// DTO lives in resource.go. vhd_type is lowercase on the wire-stdin
// side but Get-VHD's VhdType emits PascalCase on stdout; modelFromVHD
// lowercases on hydration. Path, ParentPath, and SourcePath use
// pathtype.Path so users can write either slash form without the
// framework rejecting the apply when Hyper-V echoes back backslashes.
type Model struct {
	ID         pathtype.Path `tfsdk:"id"`
	Path       pathtype.Path `tfsdk:"path"`
	VhdType    types.String  `tfsdk:"vhd_type"`
	SizeBytes  types.Int64   `tfsdk:"size_bytes"`
	ParentPath pathtype.Path `tfsdk:"parent_path"`

	// SourcePath selects a fourth mode: the host copies an existing disk
	// instead of creating one, optionally growing it to SizeBytes.
	SourcePath pathtype.Path `tfsdk:"source_path"`

	// SourceSha256 tracks the source's hash, not the disk at Path: a
	// copied disk diverges from its source once a VM writes to it or
	// it's grown, so comparing the two would re-copy on every apply.
	SourceSha256 types.String `tfsdk:"source_sha256"`

	BlockSizeBytes types.Int64  `tfsdk:"block_size_bytes"`
	FileSizeBytes  types.Int64  `tfsdk:"file_size_bytes"`
	Format         types.String `tfsdk:"format"`
	Attached       types.Bool   `tfsdk:"attached"`
}
