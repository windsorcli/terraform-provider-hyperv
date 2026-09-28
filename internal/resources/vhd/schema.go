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
		MarkdownDescription: "**Requirements:** Membership in the **Hyper-V Administrators** " +
			"group on the target host, or equivalent rights granted through a JEA endpoint.\n\n" +
			"Manages a VHD or VHDX file on the Hyper-V host. Three creation modes, selected by " +
			"`vhd_type`:\n\n" +
			"- `fixed`: pre-allocates the full `size_bytes` on disk. Slower to create; does not " +
			"grow at runtime.\n" +
			"- `dynamic`: sparse VHDX. Starts small on disk and grows as the guest writes blocks, " +
			"up to `size_bytes`.\n" +
			"- `differencing`: a read-only parent plus a writable child. `size_bytes` and " +
			"`block_size_bytes` are inherited from the parent and rejected if supplied.\n\n" +
			"A fourth mode copies rather than creates: setting `source_path` copies an existing " +
			"disk to `path` and, if `size_bytes` is set, grows the copy with `Resize-VHD`. " +
			"`vhd_type`, `parent_path`, and `block_size_bytes` are inherited from the source and " +
			"rejected if supplied. Unlike `differencing`, a copy has no lasting tie to its source, " +
			"so it suits cloning a vendor image into a per-VM boot disk whose upstream image can " +
			"later be replaced.\n\n" +
			"Format (VHD or VHDX) is inferred from the `path` extension; VHDX is recommended for " +
			"anything modern, with 4 KiB sector support, a larger maximum size, and better " +
			"corruption resistance.\n\n" +
			"Changing `size_bytes` on a fixed, dynamic, or copied disk runs `Resize-VHD` in " +
			"place; in source_path mode, a changed source triggers a re-copy. `path`, " +
			"`vhd_type`, `parent_path`, `source_path`, and `block_size_bytes` all force a new " +
			"resource when changed.\n\n" +
			"~> **Note:** `Resize-VHD` only shrinks a disk when its trailing blocks are empty; " +
			"run `Optimize-VHD` first if a shrink fails. The provider does not run `Optimize-VHD` " +
			"automatically, since it is a long, host-state-mutating operation.\n\n" +
			"~> **Note:** `attached` reports whether any VM has this disk attached, but the " +
			"provider does not block destroy on it; `Remove-Item` fails with a clear error if the " +
			"disk is still attached.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				CustomType:          pathtype.Type,
				Computed:            true,
				MarkdownDescription: "Resource identifier, matching `path` since file paths are unique on a host.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"path": schema.StringAttribute{
				CustomType: pathtype.Type,
				Required:   true,
				MarkdownDescription: "Absolute path on the Hyper-V host where the VHD or VHDX is created. " +
					"The format is inferred from the file extension. Forward and back slashes are " +
					"equivalent (`C:/foo/bar.vhdx` is the same as `C:\\foo\\bar.vhdx`), and comparison " +
					"is case-insensitive, matching Windows file-system semantics. Changing this forces " +
					"a new resource; the provider does not move VHDs in place.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"vhd_type": schema.StringAttribute{
				Optional: true,
				Computed: true,
				MarkdownDescription: "Disk layout: `fixed` (pre-allocated), `dynamic` (sparse), or " +
					"`differencing` (child of a parent). Required unless `source_path` is set, in which " +
					"case the layout is inherited from the source disk and a value here is rejected. " +
					"Changing this forces a new resource; there is no in-place conversion between layouts.",
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
				MarkdownDescription: "Absolute path on the Hyper-V host of an existing disk to copy " +
					"to `path`. Setting this puts the resource in source_path mode: the host " +
					"copies the disk to a sibling `.part` file, verifies it against the source's " +
					"SHA-256, renames it into place atomically, and grows it with `Resize-VHD` if " +
					"`size_bytes` is set. Both paths are host-local, so the bytes never cross the " +
					"runner-to-host link.\n\n" +
					"Mutually exclusive with `parent_path`; `vhd_type` and `block_size_bytes` are " +
					"also rejected alongside it, since both are inherited from the source. Unlike " +
					"a `differencing` disk, a copy has no lasting tie to its source, so the " +
					"upstream image can be replaced in place without re-parenting.\n\n" +
					"~> **Note:** `source_sha256` records the source's hash as of the last copy, " +
					"and the provider re-hashes the source on every plan. Replacing the source " +
					"image in place re-copies the disk and overwrites anything the guest wrote " +
					"-- the intended upgrade path for immutable OS images such as CoreOS or " +
					"Talos, but destructive for a disk holding state worth keeping.\n\n" +
					"Changing this forces a new resource. Every plan pays a full `Get-FileHash` " +
					"of the source, which takes tens of seconds on a multi-GiB image.\n\n" +
					"Only the source's hash is read at plan time, not its layout, so replacing " +
					"the source with a disk of a different `vhd_type` shows as a " +
					"`source_sha256` diff while `vhd_type` still reads its prior value; the " +
					"apply re-copies with the correct type and the next plan is clean.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"source_sha256": schema.StringAttribute{
				Computed: true,
				MarkdownDescription: "SHA-256 of the file at `source_path` as of the last copy, " +
					"lowercase hex. Null outside source_path mode. Re-read from the host on every " +
					"plan; a change means the upstream image was replaced, which drives a re-copy.\n\n" +
					"This tracks the source, not the disk at `path`: a copied boot disk diverges " +
					"from its source as soon as the guest writes to it, and `size_bytes` growth " +
					"changes the bytes too, so comparing the two would re-copy on every apply.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"size_bytes": schema.Int64Attribute{
				Optional: true,
				Computed: true,
				MarkdownDescription: "Declared logical size in bytes. Required for `fixed` and " +
					"`dynamic`; rejected for `differencing`, since Hyper-V inherits the size from " +
					"the parent; optional with `source_path`, where omitting it keeps the source's " +
					"size and setting it grows the copy after it lands. Updatable in place via " +
					"`Resize-VHD` wherever it's accepted; shrinking requires the trailing blocks to " +
					"be empty, so run `Optimize-VHD` first if a shrink fails.",
				PlanModifiers: []planmodifier.Int64{
					int64planmodifier.UseStateForUnknown(),
				},
			},
			"parent_path": schema.StringAttribute{
				CustomType: pathtype.Type,
				Optional:   true,
				Computed:   true,
				MarkdownDescription: "Path to the parent VHD on the host. Required for " +
					"`differencing`; rejected for `fixed`, `dynamic`, and source_path mode. Forward " +
					"and back slashes are equivalent, and comparison is case-insensitive, matching " +
					"Windows file-system semantics. Changing this forces a new resource, since the " +
					"differencing chain is permanent; use `source_path` instead for a standalone " +
					"copy whose upstream image can be refreshed in place.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
					stringplanmodifier.RequiresReplace(),
				},
			},
			"block_size_bytes": schema.Int64Attribute{
				Optional: true,
				Computed: true,
				MarkdownDescription: "VHDX block size in bytes. Defaults to Hyper-V's own default: " +
					"32 MiB for VHDX, 2 MiB for VHD. Inherited from the parent for `differencing` " +
					"disks and from the source in source_path mode; a value supplied in either " +
					"case is rejected. Changing this forces a new resource.",
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
