package vm

import (
	"github.com/hashicorp/terraform-plugin-framework-validators/int64validator"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/boolplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/int64planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
)

// resourceSchema returns the locked-in schema for hyperv_vm (minimal M4
// slice). MarkdownDescription on each attribute drives the Registry-
// published doc when `task generate` runs tfplugindocs (PLAN.md S15).
func resourceSchema() schema.Schema {
	return schema.Schema{
		MarkdownDescription: "Manages a Hyper-V virtual machine. Ships with " +
			"`name`, `generation`, nested `cpu` and `memory` blocks, `secure_boot` (gen 2), " +
			"and `notes`. Dynamic CPU/memory, integration services, automatic start/stop actions, " +
			"checkpoints, `boot_order`, attached storage, attached NICs, and power state land " +
			"in follow-up PRs of the M4 milestone (per ADR-0001 those will arrive as inline " +
			"`network_adapter[]`, `hard_disk_drive[]`, `dvd_drive[]`, and `state` blocks on this " +
			"resource).\n\n" +
			"**Boot order** is intentionally absent from this slice -- the gen 1 (BIOS) vs gen 2 (UEFI) " +
			"translation deserves its own design pass. New VMs boot from whatever Hyper-V's default is " +
			"until that lands.\n\n" +
			"**Power transitions:** the operational lifecycle (start/stop/save/pause) ships in a " +
			"follow-up as the inline `state` block on this resource. Mutations to `cpu.count`, " +
			"`memory.startup_bytes`, and `secure_boot` generally require the VM to be `Off`; the " +
			"script surfaces the cmdlet's clear error rather than auto-stopping.\n\n" +
			"**`terraform destroy` performs a hard power-off** of any running VM (`Stop-VM -Force " +
			"-TurnOff`, equivalent to pulling the plug) before calling `Remove-VM -Force`. This avoids " +
			"the indefinite-hang failure mode of graceful shutdown when a guest's Hyper-V integration " +
			"services are absent or unresponsive, and matches the destroy semantics other IaC providers " +
			"(AWS, Azure, libvirt) use. **If a clean shutdown matters** -- e.g., decoupled VHDXs the " +
			"user is keeping after destroy -- drive the graceful shutdown via `hyperv_vm_state` (when " +
			"available) or out-of-band before running `terraform destroy`.\n\n" +
			"**Static memory only.** This slice configures memory via `Set-VMMemory -DynamicMemoryEnabled $false`. " +
			"Dynamic memory ships in a follow-up.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "Resource identifier. Mirrors `name` -- VM names are unique per host.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"name": schema.StringAttribute{
				Required: true,
				MarkdownDescription: "VM name. Must be unique on the host. **Forces replacement** -- " +
					"Hyper-V doesn't support renaming a VM in place.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"generation": schema.Int64Attribute{
				Required: true,
				MarkdownDescription: "VM generation. `1` (BIOS, legacy boot, IDE/VHD) or `2` (UEFI, " +
					"Secure Boot capable, SCSI/VHDX). **Forces replacement** -- Hyper-V cannot convert " +
					"a VM from one generation to another.",
				Validators: []validator.Int64{
					int64validator.OneOf(1, 2),
				},
				PlanModifiers: []planmodifier.Int64{
					int64planmodifier.RequiresReplace(),
				},
			},
			"cpu": schema.SingleNestedAttribute{
				Required: true,
				MarkdownDescription: "Virtual processor configuration. Static count only in this slice; " +
					"dynamic-CPU attributes (`weight`, `reserve`, `limit`) attach to this same block " +
					"in a follow-up.",
				Attributes: map[string]schema.Attribute{
					"count": schema.Int64Attribute{
						Required: true,
						MarkdownDescription: "Number of virtual processors. In-place updatable via " +
							"`Set-VMProcessor -Count`; the VM generally must be `Off` for the change " +
							"to apply (cmdlet errors otherwise).",
					},
				},
			},
			"memory": schema.SingleNestedAttribute{
				Required: true,
				MarkdownDescription: "Memory configuration. **Static memory only** in this slice " +
					"(`Set-VMMemory -DynamicMemoryEnabled $false`); dynamic memory (`min_bytes`, " +
					"`max_bytes`, optional `buffer` and `priority`) attaches to this same block in " +
					"a follow-up.",
				Attributes: map[string]schema.Attribute{
					"startup_bytes": schema.Int64Attribute{
						Required: true,
						MarkdownDescription: "Static memory size in bytes (e.g. `4294967296` for " +
							"4 GiB). In-place updatable via `Set-VMMemory -StartupBytes` with " +
							"`DynamicMemoryEnabled=$false`; the VM generally must be `Off`.",
					},
				},
			},
			"secure_boot": schema.BoolAttribute{
				Optional: true,
				Computed: true,
				MarkdownDescription: "Whether UEFI Secure Boot is enabled. **Valid only when `generation = 2`** -- " +
					"a config validator rejects this on gen 1 at plan time. Defaults to Hyper-V's default " +
					"(typically `true` for new gen 2 VMs). In-place updatable via `Set-VMFirmware`.\n\n" +
					"**Cannot be cleared in-place.** Once `secure_boot` has been set in config and applied, " +
					"writing `secure_boot = null` (or removing the attribute and re-adding it later) will " +
					"NOT revert to the host default -- the change isn't forwarded by the partial-update " +
					"path, the host keeps the previous value, and every subsequent plan shows the same " +
					"diff. To revert, either explicitly set the desired bool (e.g. `secure_boot = true`) " +
					"or destroy and recreate the VM.",
				PlanModifiers: []planmodifier.Bool{
					boolplanmodifier.UseStateForUnknown(),
				},
			},
			"notes": schema.StringAttribute{
				Optional: true,
				Computed: true,
				MarkdownDescription: "Free-form description stored on the VM by Hyper-V.\n\n" +
					"**Cannot be cleared in-place once set.** Three failure modes to be aware of:\n\n" +
					"  * Omitting `notes` from config after a prior apply preserves the existing value via " +
					"`UseStateForUnknown` (omit means \"don't care,\" not \"clear\").\n" +
					"  * Writing `notes = null` explicitly does NOT clear the host's notes -- the change " +
					"isn't forwarded by the partial-update path, and every subsequent plan shows the same " +
					"`null -> \"<existing>\"` diff. **Destroy-and-recreate is the only escape.**\n" +
					"  * Writing `notes = \"\"` explicitly also loops: the host stores empty, but the " +
					"provider collapses that back to null in state to keep the omit-attribute case stable.\n\n" +
					"To change `notes`, write a different non-empty value. To remove notes from a VM, " +
					"destroy and recreate.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"state": schema.StringAttribute{
				Computed: true,
				MarkdownDescription: "Current power state reported by the host. One of `Off`, `Running`, " +
					"`Saved`, `Paused`, `Starting`, `Stopping`, ... Visibility-only on this resource; " +
					"power transitions belong to the separate `hyperv_vm_state` resource.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"path": schema.StringAttribute{
				Computed: true,
				MarkdownDescription: "Filesystem path on the host where the VM's configuration files live. " +
					"Useful for backup tooling that targets the underlying directory.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
		},
	}
}
