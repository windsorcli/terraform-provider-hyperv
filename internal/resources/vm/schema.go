package vm

import (
	"regexp"

	"github.com/hashicorp/terraform-plugin-framework-validators/int64validator"
	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/boolplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/int64planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/listdefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/listplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringdefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	mactype "github.com/windsorcli/terraform-provider-hyperv/internal/types/mac"
	pathtype "github.com/windsorcli/terraform-provider-hyperv/internal/types/path"
)

// macAddressRegex accepts the three forms Hyper-V's
// Add/Set-VMNetworkAdapter cmdlets accept: colon-separated
// (AA:BB:CC:DD:EE:FF), hyphen-separated (AA-BB-CC-DD-EE-FF), and
// unsigned 12-hex (AABBCCDDEEFF), case-insensitive but with a uniform
// separator within one address. Set-VMNetworkAdapter rejects mixed
// forms mid-apply, so the validator rejects them at plan time
// instead; mactype.Type's StringSemanticEquals folds separator and
// case so a refresh against Hyper-V's canonical echo doesn't diff.
var macAddressRegex = regexp.MustCompile(`(?i)^[0-9a-f]{2}(:[0-9a-f]{2}){5}$|^[0-9a-f]{2}(-[0-9a-f]{2}){5}$|^[0-9a-f]{12}$`)

// hardDiskObjectAttrTypes is the framework's attr.Type representation
// of one element in the `hard_disk_drive` list, for constructing the
// schema-level Default (an empty list of this type) that keeps the
// attribute from being unknown during plan when omitted. It delegates
// to HardDiskDriveAttrTypes (model.go) so schema and model share one
// source of truth.
func hardDiskObjectAttrTypes() map[string]attr.Type {
	return HardDiskDriveAttrTypes
}

// networkAdapterObjectAttrTypes is the analog for network_adapter,
// delegating to NetworkAdapterAttrTypes (model.go).
func networkAdapterObjectAttrTypes() map[string]attr.Type {
	return NetworkAdapterAttrTypes
}

// dvdDriveObjectAttrTypes is the analog for dvd_drive, delegating to
// DvdDriveAttrTypes (model.go).
func dvdDriveObjectAttrTypes() map[string]attr.Type {
	return DvdDriveAttrTypes
}

// bootOrderObjectAttrTypes is the analog for boot_order, delegating to
// BootOrderEntryAttrTypes (model.go).
func bootOrderObjectAttrTypes() map[string]attr.Type {
	return BootOrderEntryAttrTypes
}

// resourceSchema returns the locked-in schema for hyperv_vm.
// MarkdownDescription on each attribute drives the Registry-published
// doc when `task generate` runs tfplugindocs.
//
// Schema versions:
//
//	v0: flat vcpu / memory_bytes / state(string).
//	v1: vcpu -> cpu.count; memory_bytes -> memory.startup_bytes; state
//	    promoted to {desired, current}; inline attachment lists added.
//	v2: state.shutdown_mode added (Optional+Computed, no Default;
//	    UseStateForUnknown preserves the prior value when the user
//	    omits the attribute, matching notes / secure_boot).
//	v3: memory.dynamic / memory.min_bytes / memory.max_bytes added the
//	    same way. Adding fields to a SingleNestedAttribute changes the
//	    nested object's tftype, so a v2->v3 upgrader in upgrade.go
//	    bridges old state by filling the new fields with null.
//
// lint:allow-long-comment
func resourceSchema() schema.Schema {
	return schema.Schema{
		Version: 5,
		MarkdownDescription: "**Requirements:** Membership in the **Hyper-V Administrators** " +
			"group on the target host, or equivalent rights granted through a JEA endpoint.\n\n" +
			"Manages a Hyper-V virtual machine: `name`, `generation`, the nested `cpu` and " +
			"`memory` blocks, `secure_boot` (generation 2), `notes`, the inline `state` block for " +
			"power lifecycle, and inline `network_adapter`, `hard_disk_drive`, `dvd_drive`, and " +
			"`boot_order` (generation 2 only) lists.\n\n" +
			"Integration services, automatic start/stop actions, and checkpoints are not supported. " +
			"Generation 1 VMs boot from Hyper-V's default BIOS order; `Set-VMBios -StartupOrder` is " +
			"not exposed.\n\n" +
			"~> **Note:** `terraform destroy` always hard powers off a running VM " +
			"(`Stop-VM -Force -TurnOff`) before removing it, the same destroy semantics other " +
			"virtualization providers use. To shut down cleanly first, set " +
			"`state.shutdown_mode = \"graceful\"` and `state.desired = \"Off\"` (or shut down " +
			"out-of-band) before destroying.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "Resource identifier, matching `name` since VM names are unique per host.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"name": schema.StringAttribute{
				Required: true,
				MarkdownDescription: "Name of the VM. Must be unique on the host. Changing this " +
					"forces a new resource, since Hyper-V does not support renaming a VM in place.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"generation": schema.Int64Attribute{
				Required: true,
				MarkdownDescription: "Hyper-V generation for the VM. Valid values are `1` (BIOS, " +
					"IDE/VHD) and `2` (UEFI, Secure Boot capable, SCSI/VHDX). Changing this forces " +
					"a new resource, since Hyper-V cannot convert a VM between generations.",
				Validators: []validator.Int64{
					int64validator.OneOf(1, 2),
				},
				PlanModifiers: []planmodifier.Int64{
					int64planmodifier.RequiresReplace(),
				},
			},
			"cpu": schema.SingleNestedAttribute{
				Required: true,
				MarkdownDescription: "Virtual processor configuration. Only a static processor " +
					"count is supported; dynamic CPU weight, reserve, and limit are not exposed.",
				Attributes: map[string]schema.Attribute{
					"count": schema.Int64Attribute{
						Required: true,
						MarkdownDescription: "Number of virtual processors. Updatable in place via " +
							"`Set-VMProcessor -Count`; the VM must generally be `Off` for the change " +
							"to apply.",
					},
				},
			},
			"memory": schema.SingleNestedAttribute{
				Required: true,
				MarkdownDescription: "Memory configuration for the VM. `startup_bytes` is required. " +
					"Set `dynamic = true` to enable Hyper-V's dynamic memory mode and configure " +
					"`min_bytes` and `max_bytes`; omit it, or set it to `false`, for static memory.\n\n" +
					"The dynamic memory buffer percentage and balancer priority are not exposed.",
				Attributes: map[string]schema.Attribute{
					"startup_bytes": schema.Int64Attribute{
						Required: true,
						MarkdownDescription: "Memory size in bytes the VM starts with, for example " +
							"`4294967296` for 4 GiB. This is the fixed memory size unless " +
							"`dynamic = true`, in which case it must fall within " +
							"`[min_bytes, max_bytes]`. Updatable in place via " +
							"`Set-VMMemory -StartupBytes`; the VM must generally be `Off`.",
					},
					"dynamic": schema.BoolAttribute{
						Optional: true,
						Computed: true,
						MarkdownDescription: "Enables Hyper-V dynamic memory. Defaults to `false` " +
							"(static memory). When `true`, Hyper-V uses `min_bytes` / `max_bytes` " +
							"if set, otherwise its own defaults (512 MiB minimum, 1 TiB maximum).\n\n" +
							"~> **Note:** Omitting this attribute after a prior apply preserves its " +
							"existing value. Writing `dynamic = null` explicitly resets it to the " +
							"static-memory default.",
						PlanModifiers: []planmodifier.Bool{
							boolplanmodifier.UseStateForUnknown(),
						},
					},
					"min_bytes": schema.Int64Attribute{
						Optional: true,
						Computed: true,
						MarkdownDescription: "Lower bound, in bytes, for Hyper-V's dynamic memory " +
							"mode. Valid only when `dynamic = true`; must be less than or equal to " +
							"`startup_bytes`. Reads back as `null` when `dynamic` is `false` on " +
							"the host.\n\n" +
							"~> **Note:** This attribute shows as `(known after apply)` whenever " +
							"the `memory` block is in scope and the value is omitted, even when " +
							"nothing else changes.",
					},
					"max_bytes": schema.Int64Attribute{
						Optional: true,
						Computed: true,
						MarkdownDescription: "Upper bound, in bytes, for Hyper-V's dynamic memory " +
							"mode. Valid only when `dynamic = true`; must be greater than or equal " +
							"to `startup_bytes`. Reads back as `null` when `dynamic` is `false` on " +
							"the host, and shows as `(known after apply)` under the same " +
							"conditions as `min_bytes`.",
					},
				},
			},
			"hard_disk_drive": schema.ListNestedAttribute{
				Optional: true,
				Computed: true,
				MarkdownDescription: "List of VHDs and VHDXs attached to the VM. Each entry " +
					"identifies the file (`path`) and the controller slot it occupies " +
					"(`controller_type`, `controller_number`, `controller_location`); the slot is " +
					"the unique key per VM, and two entries at the same slot is an error.\n\n" +
					"State stores entries sorted by slot. A config that lists disks out of slot " +
					"order sees a one-time reorder diff on the first apply.\n\n" +
					"Updates diff the planned list against state by slot, not by list index: a " +
					"slot present only in the plan is attached, a slot present only in state is " +
					"detached, and a slot present in both with a different `path` is detached and " +
					"re-attached.\n\n" +
					"This resource does not create the VHD itself; pair it with `hyperv_vhd` or " +
					"`hyperv_image_file`.",
				PlanModifiers: []planmodifier.List{
					listplanmodifier.UseStateForUnknown(),
				},
				// Empty-list Default keeps the attribute from being unknown during plan when omitted (see hardDiskObjectAttrTypes).
				Default: listdefault.StaticValue(
					types.ListValueMust(
						types.ObjectType{AttrTypes: hardDiskObjectAttrTypes()},
						[]attr.Value{},
					),
				),
				NestedObject: schema.NestedAttributeObject{
					Attributes: map[string]schema.Attribute{
						"path": schema.StringAttribute{
							CustomType: pathtype.Type,
							Required:   true,
							MarkdownDescription: "Absolute path on the host of the VHD/VHDX to " +
								"attach. Forward and back slashes are accepted equivalently; case " +
								"is folded for comparison per Windows file-system semantics. While " +
								"the VM has a checkpoint, this still reads back as the configured " +
								"base disk rather than the checkpoint's differencing disk.",
						},
						"controller_type": schema.StringAttribute{
							Optional: true,
							Computed: true,
							Default:  stringdefault.StaticString("SCSI"),
							MarkdownDescription: "Controller bus. `SCSI` is the default and the " +
								"only valid choice for gen 2 VMs; `IDE` is gen-1-only. The script " +
								"layer surfaces Hyper-V's clear \"cannot attach IDE devices to a " +
								"generation 2 virtual machine\" error if the wrong type is paired " +
								"with the wrong generation.",
							Validators: []validator.String{
								stringvalidator.OneOf("SCSI", "IDE"),
							},
							PlanModifiers: []planmodifier.String{
								stringplanmodifier.UseStateForUnknown(),
							},
						},
						"controller_number": schema.Int64Attribute{
							Required: true,
							MarkdownDescription: "Controller index within the bus (0-based). " +
								"Required: the slot tuple identifies the attachment, and " +
								"auto-assignment isn't supported in this slice.",
						},
						"controller_location": schema.Int64Attribute{
							Required: true,
							MarkdownDescription: "Slot position within the controller (0-based). " +
								"Required for the same reason as `controller_number`.",
						},
					},
				},
			},
			"network_adapter": schema.ListNestedAttribute{
				Optional: true,
				Computed: true,
				MarkdownDescription: "List of network adapters attached to the VM. Each NIC binds " +
					"to a `hyperv_virtual_switch` by name and is identified within the VM by a " +
					"unique display `name`, which is also the key used for reconciliation; two " +
					"NICs on the same VM cannot share a name.\n\n" +
					"State stores entries sorted by `name`. A config that lists NICs out of " +
					"order sees a one-time reorder diff on the first apply.\n\n" +
					"Updates diff the planned list against state by name: a name present only " +
					"in the plan is attached, a name present only in state is detached, and a " +
					"name present in both with a different `switch_name` is detached and " +
					"re-attached.\n\n" +
					"Only access-mode VLAN tagging (`vlan_id`) is supported; trunk and isolation " +
					"VLAN modes are not.",
				PlanModifiers: []planmodifier.List{
					listplanmodifier.UseStateForUnknown(),
				},
				// Default empty list, same rationale as hard_disk_drive above.
				Default: listdefault.StaticValue(
					types.ListValueMust(
						types.ObjectType{AttrTypes: networkAdapterObjectAttrTypes()},
						[]attr.Value{},
					),
				),
				NestedObject: schema.NestedAttributeObject{
					Attributes: map[string]schema.Attribute{
						"name": schema.StringAttribute{
							Required: true,
							MarkdownDescription: "Display name of the NIC. Used as the slot key " +
								"for reconciliation and shown in Hyper-V Manager's NIC list. " +
								"Must be unique within this VM's `network_adapter` list.",
						},
						"switch_name": schema.StringAttribute{
							Required: true,
							MarkdownDescription: "Name of the `hyperv_virtual_switch` to bind " +
								"this NIC to. Hyper-V validates the switch exists at apply " +
								"time and surfaces its own clear error if it doesn't.",
						},
						"ip_addresses": schema.ListAttribute{
							Computed:    true,
							ElementType: types.StringType,
							MarkdownDescription: "IPv4 and IPv6 addresses Hyper-V's integration " +
								"services have reported for this specific NIC. Empty when the " +
								"VM is `Off`, when the guest is still booting, or when the guest " +
								"doesn't ship integration services.\n\n" +
								"Unlike the VM-level flat `ip_addresses`, which mixes addresses " +
								"from every adapter, this gives a multi-homed VM a stable " +
								"reference: index the NIC by its display `name`, then index " +
								"`ip_addresses[0]` for its first reported address. Order within " +
								"a single NIC is still host-driven, since a DHCP renewal can " +
								"shuffle IPv4/IPv6 priority, but pinning the NIC eliminates " +
								"cross-NIC ordering ambiguity.",
							// No UseStateForUnknown: on a new NIC slot it leaves ip_addresses null, tripping the post-apply consistency check.
						},
						"mac_address": schema.StringAttribute{
							CustomType: mactype.Type,
							Optional:   true,
							MarkdownDescription: "Static MAC address for this NIC. Accepts " +
								"colon-separated (`AA:BB:CC:DD:EE:FF`), hyphen-separated " +
								"(`AA-BB-CC-DD-EE-FF`), or unsigned 12-hex (`AABBCCDDEEFF`) form; " +
								"Hyper-V accepts all three, and a refresh against Hyper-V's " +
								"canonical hex form does not produce a diff. Setting this disables " +
								"Hyper-V's dynamic MAC pool for this NIC and pins the address; leave " +
								"it unset to let Hyper-V auto-assign.\n\n" +
								"Changes to this field detach and re-attach the NIC, the same as " +
								"`switch_name` changes, and require the VM to be `Off`. To revert to " +
								"a dynamic MAC, remove the attribute from config or set it to `null`.",
							Validators: []validator.String{
								stringvalidator.RegexMatches(macAddressRegex, "must be a valid "+
									"MAC address (e.g. `AA:BB:CC:DD:EE:FF`, `AA-BB-CC-DD-EE-FF`, "+
									"or `AABBCCDDEEFF`)"),
							},
						},
						"vlan_id": schema.Int64Attribute{
							Optional: true,
							MarkdownDescription: "Access-mode VLAN ID for this NIC. Valid range is " +
								"1-4094. Leave it unset for an untagged NIC; only access mode is " +
								"supported, not trunk or isolation. Changes to this field detach " +
								"and re-attach the NIC and require the VM to be `Off`. To revert to " +
								"untagged, remove the attribute from config or set it to `null`.",
							Validators: []validator.Int64{
								int64validator.Between(1, 4094),
							},
						},
					},
				},
			},
			"dvd_drive": schema.ListNestedAttribute{
				Optional: true,
				Computed: true,
				MarkdownDescription: "List of DVD drives attached to the VM. Each drive occupies " +
					"a controller slot identified by `controller_type`, `controller_number`, and " +
					"`controller_location`; `iso_path` optionally loads an ISO into the drive, " +
					"or omit it for an empty drive.\n\n" +
					"Updates diff the planned list against state by slot: a slot present only " +
					"in the plan is attached, a slot present only in state is detached, and a " +
					"slot present in both with a different `iso_path` is detached and " +
					"re-attached.\n\n" +
					"Removing a DVD entry from the list ejects it without replacing the VM, " +
					"useful for an install workflow that boots from ISO once and removes the " +
					"media on the next apply.",
				PlanModifiers: []planmodifier.List{
					listplanmodifier.UseStateForUnknown(),
				},
				Default: listdefault.StaticValue(
					types.ListValueMust(
						types.ObjectType{AttrTypes: dvdDriveObjectAttrTypes()},
						[]attr.Value{},
					),
				),
				NestedObject: schema.NestedAttributeObject{
					Attributes: map[string]schema.Attribute{
						"iso_path": schema.StringAttribute{
							CustomType: pathtype.Type,
							Optional:   true,
							MarkdownDescription: "Absolute path on the host of the ISO to load " +
								"into this DVD drive. Omit for an empty drive (medium tray exists, " +
								"nothing inserted). Forward and back slashes are accepted " +
								"equivalently.",
						},
						"controller_type": schema.StringAttribute{
							Optional: true,
							Computed: true,
							Default:  stringdefault.StaticString("SCSI"),
							MarkdownDescription: "Controller bus. `SCSI` is the default and the " +
								"only valid choice for gen 2 VMs; `IDE` is gen-1-only. The " +
								"script layer surfaces Hyper-V's clear cross-gen error if " +
								"mismatched.",
							Validators: []validator.String{
								stringvalidator.OneOf("SCSI", "IDE"),
							},
							PlanModifiers: []planmodifier.String{
								stringplanmodifier.UseStateForUnknown(),
							},
						},
						"controller_number": schema.Int64Attribute{
							Required: true,
							MarkdownDescription: "Controller index within the bus (0-based). " +
								"Required for slot identification.",
						},
						"controller_location": schema.Int64Attribute{
							Required: true,
							MarkdownDescription: "Slot position within the controller (0-based). " +
								"Required for slot identification.",
						},
					},
				},
			},
			"boot_order": schema.ListNestedAttribute{
				Optional: true,
				Computed: true,
				MarkdownDescription: "Ordered list of boot devices on a generation 2 (UEFI) VM. " +
					"Each entry has a `type` discriminator and the fields for that type:\n\n" +
					"- `type = \"hard_disk_drive\"` or `\"dvd_drive\"`: identify the device by " +
					"`controller_type`, `controller_number`, and `controller_location`, the same " +
					"slot used in `hard_disk_drive` and `dvd_drive`.\n" +
					"- `type = \"network_adapter\"`: identify the NIC by `name`.\n\n" +
					"Any difference between plan and state sets the entire list in one " +
					"`Set-VMFirmware -BootOrder` call; there is no partial reorder. The VM must " +
					"generally be `Off` for the change to apply.\n\n" +
					"Any File- or Unknown-type UEFI boot entry Hyper-V already has, a boot path " +
					"this schema doesn't model, is preserved in that call rather than dropped, so " +
					"a VM with such entries keeps them across every `boot_order` update.\n\n" +
					"Not supported on generation 1 VMs, which use `Set-VMBios -StartupOrder` " +
					"instead; a config validator rejects `boot_order` on a generation 1 VM.\n\n" +
					"For an OS install from ISO, apply once with `dvd_drive` first in " +
					"`boot_order`, install the OS, then re-apply with `hard_disk_drive` first " +
					"and the DVD removed from `dvd_drive`.",
				PlanModifiers: []planmodifier.List{
					listplanmodifier.UseStateForUnknown(),
				},
				Default: listdefault.StaticValue(
					types.ListValueMust(
						types.ObjectType{AttrTypes: bootOrderObjectAttrTypes()},
						[]attr.Value{},
					),
				),
				NestedObject: schema.NestedAttributeObject{
					Attributes: map[string]schema.Attribute{
						"type": schema.StringAttribute{
							Required: true,
							MarkdownDescription: "Discriminator: `hard_disk_drive`, `dvd_drive`, or " +
								"`network_adapter`. Drives which subset of the other fields applies.",
							Validators: []validator.String{
								stringvalidator.OneOf("hard_disk_drive", "dvd_drive", "network_adapter"),
							},
						},
						"controller_type": schema.StringAttribute{
							Optional: true,
							Computed: true,
							MarkdownDescription: "For `hard_disk_drive` / `dvd_drive` entries: the " +
								"slot's bus (`SCSI` or `IDE`). Defaults to `SCSI`. Ignored for " +
								"`network_adapter` entries.",
							Validators: []validator.String{
								stringvalidator.OneOf("SCSI", "IDE", ""),
							},
						},
						"controller_number": schema.Int64Attribute{
							Optional: true,
							Computed: true,
							MarkdownDescription: "For `hard_disk_drive` / `dvd_drive` entries: the " +
								"controller index within the bus (0-based). Ignored for " +
								"`network_adapter` entries.",
						},
						"controller_location": schema.Int64Attribute{
							Optional: true,
							Computed: true,
							MarkdownDescription: "For `hard_disk_drive` / `dvd_drive` entries: the " +
								"slot position within the controller (0-based). Ignored for " +
								"`network_adapter` entries.",
						},
						"name": schema.StringAttribute{
							Optional: true,
							Computed: true,
							MarkdownDescription: "For `network_adapter` entries: the NIC display " +
								"name (must match a `network_adapter[].name` already declared on " +
								"this VM). Ignored for `hard_disk_drive` / `dvd_drive` entries.",
						},
					},
				},
			},
			"secure_boot": schema.BoolAttribute{
				Optional: true,
				Computed: true,
				MarkdownDescription: "Whether UEFI Secure Boot is enabled. Valid only when " +
					"`generation = 2`. Defaults to Hyper-V's own default, typically `true` for new " +
					"generation 2 VMs. Updatable in place via `Set-VMFirmware`.\n\n" +
					"~> **Note:** Once set, `secure_boot` cannot be cleared back to the host " +
					"default in place. Writing `secure_boot = null`, or removing the attribute, " +
					"leaves the host value unchanged and every subsequent plan shows the same " +
					"diff. To change it, set an explicit `true` or `false`; to clear it, destroy " +
					"and recreate the VM.",
				PlanModifiers: []planmodifier.Bool{
					boolplanmodifier.UseStateForUnknown(),
				},
			},
			"secure_boot_template": schema.StringAttribute{
				Optional: true,
				Computed: true,
				MarkdownDescription: "UEFI Secure Boot template controlling which signing CAs the VM " +
					"firmware trusts. Valid only when `generation = 2`. Common values are " +
					"`MicrosoftWindows` (default for new generation 2 VMs), " +
					"`MicrosoftUEFICertificateAuthority` (the broader Microsoft UEFI CA, required " +
					"for current Server 2022 install media after Microsoft's CVE-2023-24932 cert " +
					"rotation), and `OpenSourceShieldedVM`. Hyper-V rejects unknown templates.\n\n" +
					"Changing this forces a new resource; the template is set at create time via " +
					"`Set-VMFirmware`.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
					stringplanmodifier.RequiresReplace(),
				},
			},
			"notes": schema.StringAttribute{
				Optional: true,
				Computed: true,
				MarkdownDescription: "Free-form description stored on the VM by Hyper-V.\n\n" +
					"~> **Note:** `notes` cannot be cleared in place once set. Omitting it from " +
					"config after a prior apply preserves the existing value, since omit means " +
					"\"don't care,\" not \"clear.\" Writing `notes = null` or `notes = \"\"` does not " +
					"clear the host value either; every subsequent plan shows the same diff. To " +
					"change `notes`, write a different non-empty value; to remove it, destroy and " +
					"recreate the VM.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"state": schema.SingleNestedAttribute{
				Optional: true,
				MarkdownDescription: "Power-state block. Omit it to leave the VM at whatever power " +
					"state Hyper-V's default applies, `Off` for newly created VMs. When set, " +
					"`state.desired` drives transitions and `state.current` surfaces the host's " +
					"actual state.\n\n" +
					"`Off` to `Running` calls `Start-VM`. `Running` to `Off` dispatches on " +
					"`state.shutdown_mode`: `turn_off`, or omitted, calls `Stop-VM -TurnOff " +
					"-Force` for a hard power-off; `graceful` calls `Stop-VM -Force` without " +
					"`-TurnOff` to send an ACPI shutdown through Hyper-V integration services.\n\n" +
					"~> **Note:** Scalar updates (`cpu.count`, `memory.startup_bytes`, " +
					"`secure_boot`) generally require the VM to be `Off`. Changing " +
					"`state.desired` to `\"Running\"` in the same plan as a scalar update fails " +
					"at apply time; split the change across two applies, or set " +
					"`state.desired = \"Off\"` for the duration.",
				Attributes: map[string]schema.Attribute{
					"desired": schema.StringAttribute{
						Optional: true,
						MarkdownDescription: "Desired power state. `Off` or `Running`. Omit to " +
							"surface only the current state without managing transitions.",
						Validators: []validator.String{
							stringvalidator.OneOf("Off", "Running"),
						},
					},
					"current": schema.StringAttribute{
						Computed: true,
						MarkdownDescription: "Actual power state reported by the host. Includes " +
							"transient values (`Starting`, `Stopping`, `Saved`, `Paused`) that " +
							"surface during refresh between transitions.\n\n" +
							"~> **Note:** This attribute always shows as `(known after apply)` " +
							"whenever the `state` block is in scope, even on a no-op apply.",
					},
					"shutdown_mode": schema.StringAttribute{
						Optional: true,
						Computed: true,
						MarkdownDescription: "How `Running` to `Off` transitions are performed. Omit " +
							"it to use Hyper-V's hard power-off behavior without managing the " +
							"attribute. One of:\n\n" +
							"- `turn_off`: `Stop-VM -TurnOff -Force`, a hard power-off equivalent " +
							"to pulling the plug. Always safe; has no integration-services " +
							"dependency.\n" +
							"- `graceful`: `Stop-VM -Force` (no `-TurnOff`), which sends an ACPI " +
							"shutdown signal through Hyper-V integration services and waits for " +
							"the guest to acknowledge it.\n\n" +
							"~> **Note:** `graceful` hangs indefinitely on a guest that is not " +
							"running integration services. Use it only when the guest is known to " +
							"ship and start them, as modern Windows and most Linux distributions " +
							"with `hyperv-daemons` do.\n\n" +
							"Ignored on `Off` to `Running` transitions, since `Start-VM` has no " +
							"graceful equivalent; the value is preserved in state for the next stop.\n\n" +
							"~> **Note:** `shutdown_mode` is not applied during `terraform destroy`, " +
							"which always hard powers off the VM first so a guest without " +
							"integration services can't hang the destroy. Shut down gracefully " +
							"out-of-band beforehand if a clean stop matters.\n\n" +
							"Omitting this attribute after a prior apply preserves its existing " +
							"value, the same as `notes` and `secure_boot`. Unlike those attributes, " +
							"though, `shutdown_mode` has no host-side value to fall back to: " +
							"writing `shutdown_mode = null` after a prior `\"graceful\"` value resets " +
							"state to null, and the next `Running` to `Off` transition reverts to " +
							"`turn_off`. To preserve a value across applies, omit the attribute " +
							"rather than writing `null`.",
						Validators: []validator.String{
							stringvalidator.OneOf("turn_off", "graceful"),
						},
						PlanModifiers: []planmodifier.String{
							stringplanmodifier.UseStateForUnknown(),
						},
					},
				},
			},
			"ip_addresses": schema.ListAttribute{
				Computed:    true,
				ElementType: types.StringType,
				MarkdownDescription: "Flat list of IPv4 and IPv6 addresses the guest's Hyper-V " +
					"integration services have reported across all attached `network_adapter` " +
					"entries. Empty when the VM is `Off`, when the guest is still booting, or " +
					"when the guest doesn't ship integration services.\n\n" +
					"~> **Note:** Order is host-driven and not stable across VM restarts; a " +
					"reboot or DHCP lease renewal can change which IP appears first, so " +
					"indexing `ip_addresses[0]` can plan a spurious update. Index into this " +
					"list only for a single-NIC, single-IP VM. A multi-homed VM should use the " +
					"per-NIC `network_adapter[*].ip_addresses` instead, which pins the selector " +
					"to a NIC's deterministic display `name`.",
				PlanModifiers: []planmodifier.List{
					listplanmodifier.UseStateForUnknown(),
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
