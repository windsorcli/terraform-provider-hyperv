package vhd

import (
	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/boolplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/int64planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"

	pathtype "github.com/xeitu/terraform-provider-hyperv/internal/types/path"
)

// resourceSchema returns the locked-in schema for hyperv_vhd. Four
// creation modes (fixed, dynamic, differencing, copy) share the same schema,
// distinguished by `vhd_type` plus cross-attribute ConfigValidators on
// the resource (see resource.go).
func resourceSchema() schema.Schema {
	return schema.Schema{
		MarkdownDescription: "**Requirements:** Membership in the **Hyper-V Administrators** group on " +
			"the target host (or equivalent rights granted through a JEA endpoint).\n\n" +
			"Manages a VHD/VHDX file on the Hyper-V host. Four creation modes:\n" +
			"  * **`fixed`** -- pre-allocates the full `size_bytes` on disk. Slow create, no runtime expansion.\n" +
			"  * **`dynamic`** -- sparse VHDX. Initial on-disk size is minimal; the file grows as the guest writes blocks, up to `size_bytes`.\n" +
			"  * **`differencing`** -- read-only parent + writable child. `size_bytes` and `block_size_bytes` are inherited from the parent and rejected if supplied.\n" +
			"  * **`copy`** -- copies an existing host-side golden from `source_path` to `path`. The golden is never modified or deleted; optional `size_bytes` may only expand the copy.\n" +
			"Format (VHD vs VHDX) is inferred from the `path` extension. VHDX is recommended for anything modern (4 KiB sector support, larger maximum size, better corruption resistance).\n\n" +
			"**Resize is the only in-place mutation:** changing `size_bytes` runs `Resize-VHD`; copied disks may only grow. Every other creation attribute forces replacement.\n\n" +
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
				Required: true,
				MarkdownDescription: "Disk creation mode. One of `fixed`, `dynamic`, `differencing`, or `copy`. " +
					"Copy clones the host-side `source_path` without modifying the golden. Forces replacement when changed.",
				Validators: []validator.String{
					stringvalidator.OneOf("fixed", "dynamic", "differencing", "copy"),
				},
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"size_bytes": schema.Int64Attribute{
				Optional: true,
				Computed: true,
				MarkdownDescription: "Declared logical size in bytes. Required for `fixed` and `dynamic`, rejected for `differencing`, " +
					"and optional for `copy`. A copied disk is expanded when this exceeds the golden virtual size and is never shrunk.",
				PlanModifiers: []planmodifier.Int64{
					int64planmodifier.UseStateForUnknown(),
				},
			},
			"parent_path": schema.StringAttribute{
				CustomType: pathtype.Type,
				Optional:   true,
				Computed:   true,
				MarkdownDescription: "Path to the parent VHD on the host. **Required** for `differencing`; **rejected** " +
					"for `fixed` and `dynamic`. **Forces replacement** when changed -- the differencing chain is permanent. " +
					"Forward and back slashes are accepted equivalently; comparison is case-insensitive per Windows file-system semantics.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
					stringplanmodifier.RequiresReplace(),
				},
			},
			"source_path": schema.StringAttribute{
				CustomType: pathtype.Type,
				Optional:   true,
				MarkdownDescription: "Existing golden VHD/VHDX path on the Hyper-V host. Required only when `vhd_type = \"copy\"`. " +
					"The provider copies this file to `path` and never modifies or deletes the source. Forces replacement.",
				PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()},
			},
			"keep_on_destroy": schema.BoolAttribute{
				Optional:            true,
				MarkdownDescription: "Leave the managed destination VHD/VHDX on the host when the Terraform resource is destroyed. The source_path golden is never deleted.",
			},
			"block_size_bytes": schema.Int64Attribute{
				Optional: true,
				Computed: true,
				MarkdownDescription: "VHDX block size in bytes. Optional; defaults per Hyper-V (32 MiB for VHDX, 2 MiB " +
					"for VHD). For `differencing` disks this is inherited from the parent and any value supplied is " +
					"rejected by Hyper-V. **Forces replacement** when changed.",
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
