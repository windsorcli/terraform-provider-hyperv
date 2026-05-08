// Package iso_volume implements the hyperv_iso_volume resource. It
// synthesizes a deterministic ISO9660 volume on the runner via
// internal/iso, then streams the bytes to the host through the same
// wire path that hyperv_image_file's local_path mode uses (typed
// client's StreamFile primitive + new.ps1 source_mode=local_path
// for verify-and-rename). The host script is reused unchanged --
// from new.ps1's perspective the runner-synthesized ISO and a
// runner-supplied vhdx are indistinguishable: both arrive as bytes
// staged at a sibling .part path with an expected SHA-256.
package iso_volume //nolint:revive // underscore in package name mirrors the resource directory.

import (
	"github.com/hashicorp/terraform-plugin-framework/types"

	pathtype "github.com/windsorcli/terraform-provider-hyperv/internal/types/path"
)

// Model is the tfsdk-bound struct backing the resource state.
//
// Field tags align with schema.go attribute names. The user supplies
// `volume_label`, `files`, and `destination_path`; the provider
// computes and stores `id`, `sha256`, and `size_bytes`. `keep_on_destroy`
// mirrors hyperv_image_file's escape hatch -- destroy removes from
// state but leaves the bytes on disk. Useful when the seed-ISO outlives
// a single VM (rare for cidata seeds, but common for autounattend
// images shared across multiple VMs).
//
// `files` is a Map<string, string> -- filename -> UTF-8 content. v1
// only supports root-level files (no subdirectories) and UTF-8 text
// content. Binary or hierarchical content can land later as a
// schema-additive change without breaking existing configs.
//
// DestinationPath uses pathtype.Path so users can write either
// `C:/foo` or `C:\foo` without triggering an "inconsistent result
// after apply" diagnostic when Hyper-V returns the canonical
// backslash form. ID mirrors destination_path and is also Path so
// the same semantic-equality covers the Computed mirror's refresh.
type Model struct {
	ID              pathtype.Path `tfsdk:"id"`
	DestinationPath pathtype.Path `tfsdk:"destination_path"`
	VolumeLabel     types.String  `tfsdk:"volume_label"`
	Files           types.Map     `tfsdk:"files"`
	Sha256          types.String  `tfsdk:"sha256"`
	SizeBytes       types.Int64   `tfsdk:"size_bytes"`
	KeepOnDestroy   types.Bool    `tfsdk:"keep_on_destroy"`
}
