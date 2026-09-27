package vhd

import (
	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/boolplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/int64planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"

	pathtype "github.com/windsorcli/terraform-provider-hyperv/internal/types/path"
)

// resourceSchema returns the locked-in schema for hyperv_vhd. Three
// creation modes (fixed, dynamic, differencing) share the same schema,
// distinguished by `vhd_type` plus cross-attribute ConfigValidators on
// the resource (see resource.go).
func resourceSchema() schema.Schema {
	return schema.Schema{
		MarkdownDescription: "**Requirements:** Membership in the **Hyper-V Administrators** group on " +
			"the target host (or equivalent rights granted through a JEA endpoint).\n\n" +
			"Manages a VHD/VHDX file on the Hyper-V host. Three creation modes, selected by `vhd_type`:\n\n" +
			"  * **`fixed`** -- pre-allocates the full `size_bytes` on disk. Slow create, no runtime expansion.\n" +
			"  * **`dynamic`** -- sparse VHDX. Initial on-disk size is minimal; the file grows as the guest writes blocks, up to `size_bytes`.\n" +
			"  * **`differencing`** -- read-only parent + writable child. `size_bytes` and `block_size_bytes` are inherited from the parent and rejected if supplied.\n\n" +
			"Plus a fourth mode that copies rather than creates:\n\n" +
			"  * **`source_path`-mode** -- the host copies an existing disk at `source_path` to `path` and, when `size_bytes` is set, grows the copy with `Resize-VHD`. `vhd_type`, `parent_path`, and `block_size_bytes` are inherited from the source and rejected if supplied. Use this to clone a vendor image into a per-VM boot disk: unlike `differencing`, the copy has no lasting tie to its source, so the upstream image can be replaced in place.\n\n" +
			"Format (VHD vs VHDX) is inferred from the `path` extension. VHDX is recommended for anything modern (4 KiB sector support, larger maximum size, better corruption resistance).\n\n" +
			"**In-place mutations:** changing `size_bytes` on a fixed, dynamic, or copied disk runs `Resize-VHD` (no replace), and in `source_path`-mode a source whose contents changed triggers a re-copy. `path`, `vhd_type`, `parent_path`, `source_path`, and `block_size_bytes` all force replacement when changed.\n\n" +
			"**Shrink limitations:** `Resize-VHD` only shrinks when trailing blocks are empty. Run `Optimize-VHD` first to reclaim space if a shrink errors. The provider does not run Optimize-VHD automatically -- it's a long, host-state-mutating operation that operators should trigger explicitly.\n\n" +
			"**Attached flag:** `attached` reports whether any VM currently has this disk attached. The provider does not block destroy when the disk is attached -- the underlying `Remove-Item` errors loudly with a clear message in that case.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				CustomType:          pathtype.Type,
				Computed:            true,
				MarkdownDescription: "Resource identifier. Mirrors `path` -- file paths are unique on a host.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"path": schema.StringAttribute{
				CustomType: pathtype.Type,
				Required:   true,
				MarkdownDescription: "Absolute path on the Hyper-V host where the VHD/VHDX should be created. " +
					"The format (VHD vs VHDX) is inferred from the file extension. **Forces replacement** when changed -- the provider does not move VHDs in place. " +
					"Forward and back slashes are accepted equivalently (`C:/foo/bar.vhdx` ≡ `C:\\foo\\bar.vhdx`); comparison is case-insensitive per Windows file-system semantics.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"vhd_type": schema.StringAttribute{
				Optional: true,
				Computed: true,
				MarkdownDescription: "Disk layout. One of `fixed` (pre-allocated), `dynamic` (sparse), or `differencing` " +
					"(child of a parent). **Required** unless `source_path` is set, in which case the layout is " +
					"inherited from the source disk and supplying a value is rejected. **Forces replacement** when " +
					"changed -- there is no in-place conversion path.",
				Validators: []validator.String{
					stringvalidator.OneOf("fixed", "dynamic", "differencing"),
				},
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
					stringplanmodifier.RequiresReplace(),
				},
			},
			"source_path": schema.StringAttribute{
				CustomType: pathtype.Type,
				Optional:   true,
				MarkdownDescription: "Absolute path **on the Hyper-V host** of an existing disk to copy to `path`. " +
					"When set, the resource operates in `source_path`-mode: the host copies the disk to a sibling " +
					"`.part` of `path`, verifies the copy against the SHA-256 read from the source at plan time, " +
					"atomic-renames into place, and then grows it with `Resize-VHD` if `size_bytes` is set. Both " +
					"endpoints are host-local, so the bytes never cross the runner-to-host link.\n\n" +
					"Mutually exclusive with `parent_path`, and `vhd_type` / `block_size_bytes` are rejected " +
					"alongside it (both are inherited from the source).\n\n" +
					"**Why not a `differencing` disk?** A differencing child stays bound to its parent for its " +
					"whole life, so the upstream image can never be replaced in place. A copy has no such tie: " +
					"refreshing the vendor image under a fixed name is safe, and no re-parenting is ever needed.\n\n" +
					"**Re-copy on source change.** `source_sha256` records the source's hash as of the last copy. " +
					"The provider re-hashes the source at plan time, so an image replaced in place surfaces as a " +
					"`source_sha256` diff and re-copies. **This overwrites the disk**, discarding anything the " +
					"guest wrote -- which is the intended upgrade path for immutable OS images (CoreOS, Talos) " +
					"and destructive for a disk holding state you care about.\n\n" +
					"**Forces replacement** when changed. Every plan pays a full `Get-FileHash` of the source, " +
					"which on a multi-GiB image is tens of seconds.\n\n" +
					"**Plan accuracy on a layout change.** Only the source's hash is read at plan time, not " +
					"its layout, so replacing the source with a disk of a different `vhd_type` (dynamic to " +
					"fixed, say) shows up as a `source_sha256` diff with `vhd_type` still reading its prior " +
					"value. The apply re-copies and writes the correct type, and the next plan is clean. " +
					"Reading the layout at plan time would make `vhd_type`'s `RequiresReplace` fire and turn " +
					"the in-place re-copy into a destroy-then-create, which is more disruptive for no " +
					"difference in the end state.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"source_sha256": schema.StringAttribute{
				Computed: true,
				MarkdownDescription: "SHA-256 of the file at `source_path` as of the last copy (lowercase hex). " +
					"Null outside `source_path`-mode. Re-read from the host at plan time; a change means the " +
					"upstream image was replaced and drives the re-copy.\n\n" +
					"This tracks the *source*, not the disk at `path`. A copied boot disk diverges from its " +
					"source as soon as its guest writes to it, and `size_bytes` growth changes the bytes too, " +
					"so comparing the two would re-copy on every apply.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"size_bytes": schema.Int64Attribute{
				Optional: true,
				Computed: true,
				MarkdownDescription: "Declared logical size in bytes. **Required** for `fixed` and `dynamic`; **rejected** for " +
					"`differencing` (Hyper-V inherits the size from the parent); **optional** with `source_path`, where " +
					"omitting it keeps the source's size and setting it grows the copy after the copy lands. In-place " +
					"updatable via `Resize-VHD` in every mode that accepts it; shrinks require trailing blocks to be " +
					"empty (run `Optimize-VHD` first if needed).",
				PlanModifiers: []planmodifier.Int64{
					int64planmodifier.UseStateForUnknown(),
				},
			},
			"parent_path": schema.StringAttribute{
				CustomType: pathtype.Type,
				Optional:   true,
				Computed:   true,
				MarkdownDescription: "Path to the parent VHD on the host. **Required** for `differencing`; **rejected** " +
					"for `fixed`, `dynamic`, and `source_path`-mode. **Forces replacement** when changed -- the " +
					"differencing chain is permanent. Reach for `source_path` instead when you want a standalone copy " +
					"whose upstream image can be refreshed in place. " +
					"Forward and back slashes are accepted equivalently; comparison is case-insensitive per Windows file-system semantics.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
					stringplanmodifier.RequiresReplace(),
				},
			},
			"block_size_bytes": schema.Int64Attribute{
				Optional: true,
				Computed: true,
				MarkdownDescription: "VHDX block size in bytes. Optional; defaults per Hyper-V (32 MiB for VHDX, 2 MiB " +
					"for VHD). For `differencing` disks and in `source_path`-mode this is inherited (from the parent " +
					"and the source respectively) and any value supplied is rejected. **Forces replacement** when changed.",
				PlanModifiers: []planmodifier.Int64{
					int64planmodifier.UseStateForUnknown(),
					int64planmodifier.RequiresReplace(),
				},
			},
			"file_size_bytes": schema.Int64Attribute{
				Computed: true,
				MarkdownDescription: "Actual on-disk size in bytes. For `fixed` disks this matches `size_bytes`. " +
					"For `dynamic` and `differencing` disks this starts small and grows as the guest writes blocks.",
				PlanModifiers: []planmodifier.Int64{
					int64planmodifier.UseStateForUnknown(),
				},
			},
			"format": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "Disk format reported by Hyper-V. Either `VHD` (legacy) or `VHDX`.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"attached": schema.BoolAttribute{
				Computed:            true,
				MarkdownDescription: "Whether this disk is currently attached to any VM on the host. Refreshed on every `Read`.",
				PlanModifiers: []planmodifier.Bool{
					boolplanmodifier.UseStateForUnknown(),
				},
			},
		},
	}
}
