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
// Add/Set-VMNetworkAdapter cmdlets accept:
//
//   - colon-separated: AA:BB:CC:DD:EE:FF
//   - hyphen-separated: AA-BB-CC-DD-EE-FF
//   - unsigned 12-hex: AABBCCDDEEFF
//
// Case-insensitive. The separator must be uniform within a single
// address -- mixed forms like AA:BB-CC:DD-EE:FF parse as valid hex
// but Set-VMNetworkAdapter -StaticMacAddress rejects them mid-apply,
// so the schema validator rejects them at plan time instead. A custom
// type with StringSemanticEquals (mactype.Type) folds separator and
// case so a refresh against Hyper-V's canonical unsigned-12-hex echo
// doesn't surface a phantom diff.
var macAddressRegex = regexp.MustCompile(`(?i)^[0-9a-f]{2}(:[0-9a-f]{2}){5}$|^[0-9a-f]{2}(-[0-9a-f]{2}){5}$|^[0-9a-f]{12}$`)

// hardDiskObjectAttrTypes is the framework's attr.Type representation
// of one element in the `hard_disk_drive` list. Used to construct the
// schema-level Default (empty list of this object type), which keeps
// the attribute from being "unknown" during plan when the user omits
// it -- decoding into []HardDiskDriveModel can't represent unknown,
// and without the Default the framework's tftypes -> Go reflect path
// errors at apply time with "Suggested Type: basetypes.ListValue".
//
// Keep these tags 1:1 with HardDiskDriveModel's tfsdk tags. A drift
// silently produces "schema mismatch" diagnostics that take a long
// time to track down.
func hardDiskObjectAttrTypes() map[string]attr.Type {
	return map[string]attr.Type{
		"path":                pathtype.Type,
		"controller_type":     types.StringType,
		"controller_number":   types.Int64Type,
		"controller_location": types.Int64Type,
	}
}

// networkAdapterObjectAttrTypes is the analog for network_adapter.
// Same Default-empty-list rationale as HDDs. Keep in lockstep with
// NetworkAdapterModel and the network_adapter NestedAttributeObject
// schema below -- a drift between any of the three triggers
// "schema mismatch" diagnostics that take a while to track down.
func networkAdapterObjectAttrTypes() map[string]attr.Type {
	return map[string]attr.Type{
		"name":         types.StringType,
		"switch_name":  types.StringType,
		"ip_addresses": types.ListType{ElemType: types.StringType},
		"mac_address":  mactype.Type,
		"vlan_id":      types.Int64Type,
	}
}

// dvdDriveObjectAttrTypes is the analog for dvd_drive. Slot tuple
// matches HardDiskDrive; iso_path uses the path custom type for
// slash-style folding.
func dvdDriveObjectAttrTypes() map[string]attr.Type {
	return map[string]attr.Type{
		"iso_path":            pathtype.Type,
		"controller_type":     types.StringType,
		"controller_number":   types.Int64Type,
		"controller_location": types.Int64Type,
	}
}

// bootOrderObjectAttrTypes is the analog for boot_order. The shape is
// a discriminated union: type drives which subset of the remaining
// fields is meaningful. Required for the Default empty-list value
// since boot_order is Optional+Computed and the framework otherwise
// hands us an "unknown" ListValue that the v1.19 reflect path can't
// decode into a Go slice (same issue as hard_disk_drive et al).
func bootOrderObjectAttrTypes() map[string]attr.Type {
	return map[string]attr.Type{
		"type":                types.StringType,
		"controller_type":     types.StringType,
		"controller_number":   types.Int64Type,
		"controller_location": types.Int64Type,
		"name":                types.StringType,
	}
}

// resourceSchema returns the locked-in schema for hyperv_vm (minimal M4
// slice). MarkdownDescription on each attribute drives the Registry-
// published doc when `task generate` runs tfplugindocs (PLAN.md S15).
//
// Schema versions:
//
//	v0 (PR #20): flat vcpu / memory_bytes / state(string).
//	v1: vcpu -> cpu.count; memory_bytes -> memory.startup_bytes; state
//	    promoted to {desired, current}; inline attachment lists added.
//	v2: state.shutdown_mode added (Optional+Computed, no Default;
//	    UseStateForUnknown plan modifier preserves the prior value when
//	    the user omits the attribute, matching notes / secure_boot).
//	v3: memory.dynamic / memory.min_bytes / memory.max_bytes added
//	    (Optional+Computed, no Default; same omit-preserves shape as
//	    state.shutdown_mode). Adding fields to a SingleNestedAttribute
//	    changes the nested object's tftype, so v2 state files are
//	    bridged by a v2->v3 upgrader in upgrade.go that fills the new
//	    fields with null -- v2 users never had a chance to choose
//	    values, and the script's wire contract treats absent
//	    dynamic_memory as static (same on-host behavior as v2).
func resourceSchema() schema.Schema {
	return schema.Schema{
		Version: 5,
		MarkdownDescription: "Manages a Hyper-V virtual machine. Configures " +
			"`name`, `generation`, nested `cpu` and `memory` blocks (static or dynamic), " +
			"`secure_boot` (gen 2), `notes`, the inline `state` block for power lifecycle " +
			"(`desired`, `current`, `shutdown_mode`), and inline `network_adapter[]`, " +
			"`hard_disk_drive[]`, `dvd_drive[]`, and `boot_order` (gen 2 only) lists.\n\n" +
			"Integration services, automatic start/stop actions, and checkpoints are not " +
			"currently exposed. Generation 1 BIOS boot ordering (`Set-VMBios -StartupOrder`) " +
			"is also not currently supported -- gen 1 VMs boot from whatever Hyper-V's default is.\n\n" +
			"**Power transitions** are driven by the inline `state` block (`desired = \"Running\"`, " +
			"`\"Off\"`, `\"Saved\"`, `\"Paused\"`). Mutations to `cpu.count`, `memory.startup_bytes`, " +
			"and `secure_boot` generally require the VM to be `Off`; the script surfaces the " +
			"cmdlet's clear error rather than auto-stopping.\n\n" +
			"**`terraform destroy` performs a hard power-off** of any running VM (`Stop-VM -Force " +
			"-TurnOff`, equivalent to pulling the plug) before calling `Remove-VM -Force`. This avoids " +
			"the indefinite-hang failure mode of graceful shutdown when a guest's Hyper-V integration " +
			"services are absent or unresponsive, and matches the destroy semantics other IaC providers " +
			"(AWS, Azure, libvirt) use. **If a clean shutdown matters** -- e.g., decoupled VHDXs the " +
			"user is keeping after destroy -- drive the graceful shutdown via `state.shutdown_mode = " +
			"\"graceful\"` plus `desired = \"Off\"` (or out-of-band) before running `terraform destroy`.",
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
				MarkdownDescription: "Memory configuration. `startup_bytes` is the only required " +
					"field; `dynamic` opts in to Hyper-V's dynamic memory mode and unlocks " +
					"`min_bytes` / `max_bytes` for setting bounds. Omit `dynamic` (or set " +
					"`dynamic = false`) for the static-memory path that is always safe and matches " +
					"the v2-and-prior behavior.\n\n" +
					"`buffer` and `priority` (the advanced dynamic-memory tuning knobs for " +
					"memory pressure buffer percentage and balancer priority) are not currently " +
					"exposed; most users don't need them.",
				Attributes: map[string]schema.Attribute{
					"startup_bytes": schema.Int64Attribute{
						Required: true,
						MarkdownDescription: "Memory size in bytes the VM boots with " +
							"(e.g. `4294967296` for 4 GiB). When `dynamic = false` (or omitted), " +
							"this is also the fixed memory size. When `dynamic = true`, " +
							"`startup_bytes` must fall within `[min_bytes, max_bytes]` -- the " +
							"cmdlet errors otherwise. In-place updatable via " +
							"`Set-VMMemory -StartupBytes`; the VM generally must be `Off`.",
					},
					"dynamic": schema.BoolAttribute{
						Optional: true,
						Computed: true,
						MarkdownDescription: "Whether Hyper-V dynamic memory is enabled. Optional. " +
							"Omit (or set `false`) for the static-memory default. When `true`, " +
							"the cmdlet uses `min_bytes` / `max_bytes` if supplied, else " +
							"Hyper-V's defaults (Minimum = 512 MiB, Maximum = 1 TiB).\n\n" +
							"**Omit semantics** match `notes` / `secure_boot` / " +
							"`state.shutdown_mode`: omitting from config after a prior apply " +
							"preserves the existing value via `UseStateForUnknown`. Writing " +
							"`dynamic = null` explicitly resets state to null and the next memory " +
							"mutation reverts to the static-memory default; to switch behavior, " +
							"write `true` or `false` explicitly.",
						PlanModifiers: []planmodifier.Bool{
							boolplanmodifier.UseStateForUnknown(),
						},
					},
					"min_bytes": schema.Int64Attribute{
						Optional: true,
						Computed: true,
						MarkdownDescription: "Lower bound (in bytes) for Hyper-V's dynamic memory " +
							"mode. **Only valid when `dynamic = true`** -- a config validator " +
							"rejects `min_bytes` set with `dynamic` unset or false at plan time. " +
							"Must be <= `startup_bytes` (the cmdlet rejects the call otherwise).\n\n" +
							"Read-back surfaces null when `dynamic` is false on the host (the " +
							"host still stores Hyper-V's default of 512 MiB but it isn't in " +
							"effect). **No `UseStateForUnknown` plan modifier**: a plan that " +
							"flips `dynamic` to false must show `min_bytes` becoming null " +
							"otherwise the framework's post-apply consistency check rejects " +
							"the apply. Trade-off: plans show `min_bytes = (known after apply)` " +
							"whenever the block is in scope and the attribute is omitted, even " +
							"on no-op apply turns.",
					},
					"max_bytes": schema.Int64Attribute{
						Optional: true,
						Computed: true,
						MarkdownDescription: "Upper bound (in bytes) for Hyper-V's dynamic memory " +
							"mode. **Only valid when `dynamic = true`** -- a config validator " +
							"rejects `max_bytes` set with `dynamic` unset or false at plan time. " +
							"Must be >= `startup_bytes` (the cmdlet rejects the call otherwise).\n\n" +
							"Read-back surfaces null when `dynamic` is false on the host. **No " +
							"`UseStateForUnknown`** -- same rationale as `min_bytes`. Trade-off: " +
							"plans show `max_bytes = (known after apply)` whenever the block is " +
							"in scope and the attribute is omitted.",
					},
				},
			},
			"hard_disk_drive": schema.ListNestedAttribute{
				Optional: true,
				Computed: true,
				MarkdownDescription: "List of VHDs/VHDXs attached to the VM. Each element identifies " +
					"both the underlying file (`path`) and the controller slot the disk occupies " +
					"(`controller_type` + `controller_number` + `controller_location`). The slot " +
					"tuple is the unique key per VM -- two attachments at the same slot is an error.\n\n" +
					"**Order convention:** state stores the list canonically by slot tuple " +
					"(controller_type, then controller_number, then controller_location). Configs " +
					"that write disks in slot order match state directly; configs that don't write " +
					"in slot order will see a one-time \"reorder\" diff on the first apply that " +
					"resolves to canonical order. (List rather than Set because terraform-plugin-" +
					"framework v1.19's slice decode of nested-set attributes hits a known reflect " +
					"path that doesn't compose cleanly with the inline-block model. List + " +
					"canonical sort gives the same user-visible behavior with a simpler decode.)\n\n" +
					"**Reconciliation:** Update diffs the planned list against state by slot tuple " +
					"(NOT by index, despite being a List). Slots present in plan but not state get " +
					"`Add-VMHardDiskDrive`; slots in state but not plan get `Remove-VMHardDiskDrive`; " +
					"slots in both with a different `path` are detached then re-attached (Set-" +
					"VMHardDiskDrive's path-swap path is not used in this slice -- detach + attach " +
					"has clearer error semantics).\n\n" +
					"This resource does NOT create the VHD itself -- pair with `hyperv_vhd` or " +
					"`hyperv_image_file` for that.",
				PlanModifiers: []planmodifier.List{
					listplanmodifier.UseStateForUnknown(),
				},
				// Default empty list keeps the attribute from being
				// "unknown" during plan when the user omits it. See
				// hardDiskObjectAttrTypes above for the rationale --
				// without this, the framework's tftypes -> Go reflect
				// path errors at apply time on a no-disk VM.
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
								"is folded for comparison per Windows file-system semantics.",
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
				MarkdownDescription: "List of network adapters attached to the VM. Each NIC " +
					"is bound to a `hyperv_virtual_switch` by name and identified within " +
					"the VM by a unique display `name`. The display name is the slot key " +
					"used for diff/reconciliation -- two NICs in the same VM cannot share " +
					"a name (validator at plan time).\n\n" +
					"**Order canonicalization:** state stores the list sorted by `name`. " +
					"Configs that write NICs in name order match state directly; configs " +
					"that don't will see a one-time \"reorder\" diff on the first apply.\n\n" +
					"**Reconciliation:** Update diffs the planned list against state by " +
					"name. Names present in plan but not state get `Add-VMNetworkAdapter`; " +
					"names in state but not plan get `Remove-VMNetworkAdapter`; names in " +
					"both with a different `switch_name` get detached then re-attached " +
					"(Hyper-V doesn't expose a path-swap-only cmdlet for NIC switch " +
					"binding, so detach + attach is the natural operation).\n\n" +
					"VLAN tagging and static MAC addresses are not currently exposed.",
				PlanModifiers: []planmodifier.List{
					listplanmodifier.UseStateForUnknown(),
				},
				// Default empty list -- same rationale as hard_disk_drive
				// above. Without it, the framework's reflect path errors
				// when the user omits the attribute.
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
							MarkdownDescription: "IPv4 / IPv6 addresses Hyper-V's integration " +
								"services have reported for this specific NIC. Empty when the " +
								"VM is `Off`, when the guest is still booting, or when the guest " +
								"doesn't ship integration services.\n\n" +
								"Unlike the VM-level flat `ip_addresses` list (which mixes IPs " +
								"from every adapter and has order-unstable semantics across " +
								"reboots), the per-NIC view gives multi-homed VMs a stable " +
								"reference: index this NIC by its deterministic display `name`, " +
								"then index its `ip_addresses[0]` for the first reported IP. " +
								"Order within a single NIC remains host-driven (a DHCP renewal " +
								"can shuffle IPv4 vs IPv6 priority), but pinning the NIC " +
								"selector eliminates the cross-NIC ordering ambiguity.",
							// No UseStateForUnknown plan modifier here. Empirically
							// verified against the bench (TestAcc_VM_withNetworkAdapter
							// step 2, adding a second NIC): with the modifier in
							// place, the framework leaves the new NIC slot's
							// ip_addresses as `null` at plan time (not unknown,
							// despite the modifier's nominal "leave unknown alone"
							// docstring), and the empty list our Read populates
							// post-apply trips the framework's "Provider produced
							// inconsistent result" check (was null, now
							// cty.ListValEmpty(cty.String)).
							//
							// Without the modifier, the framework defaults
							// Computed-only attrs to unknown at plan time, which
							// accepts any post-apply value -- and on subsequent
							// plans where nothing has changed, the framework
							// preserves the state value naturally (state -> plan
							// for unchanged Computed fields is the default
							// behavior). Plan-stability is not actually lost.
						},
						"mac_address": schema.StringAttribute{
							CustomType: mactype.Type,
							Optional:   true,
							Computed:   true,
							MarkdownDescription: "Static MAC address for this NIC, in either " +
								"colon-separated (`AA:BB:CC:DD:EE:FF`), hyphen-separated " +
								"(`AA-BB-CC-DD-EE-FF`), or unsigned-12-hex (`AABBCCDDEEFF`) " +
								"form -- Hyper-V accepts all three. The stored value " +
								"preserves whatever form you wrote; semantic equality " +
								"folds separator presence and case so a refresh against " +
								"Hyper-V's canonical unsigned-12-hex echo doesn't surface " +
								"a phantom diff. Setting this disables Hyper-V's dynamic-" +
								"MAC pool for this NIC and pins the address; leave unset " +
								"to let Hyper-V auto-assign (state stores `null` in that " +
								"case so unset config matches unset state).\n\n" +
								"Changes to this field cause the NIC to be detached and " +
								"re-attached (same shape as `switch_name` updates), which " +
								"requires the VM to be `Off` for the cmdlet to apply.",
							Validators: []validator.String{
								stringvalidator.RegexMatches(macAddressRegex, "must be a valid "+
									"MAC address (e.g. `AA:BB:CC:DD:EE:FF`, `AA-BB-CC-DD-EE-FF`, "+
									"or `AABBCCDDEEFF`)"),
							},
						},
						"vlan_id": schema.Int64Attribute{
							Optional: true,
							Computed: true,
							MarkdownDescription: "Access-mode VLAN ID for this NIC. Valid " +
								"range is 1-4094. Leave unset (the default) for an untagged " +
								"NIC; state stores `null` for untagged NICs rather than the " +
								"sentinel `0` Hyper-V uses internally, so unset config matches " +
								"unset state.\n\n" +
								"Trunk and isolation VLAN modes are not currently supported " +
								"-- only Access mode. Changes to this field cause the NIC to " +
								"be detached and re-attached, requiring the VM to be `Off`.",
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
					"a controller slot identified by `controller_type` + `controller_number` + " +
					"`controller_location`; `iso_path` optionally loads an ISO into the drive " +
					"(omit for an empty drive).\n\n" +
					"**Slot tuple keys reconciliation:** Update diffs the planned list against " +
					"state by slot. Slots in plan but not state get `Add-VMDvdDrive`; slots in " +
					"state but not plan get `Remove-VMDvdDrive`; slots in both with a different " +
					"`iso_path` get detached and re-attached (the brief gap between the two " +
					"calls is acceptable since the VM is generally Off during scalar updates " +
					"anyway).\n\n" +
					"**Eject-on-destroy:** removing a DVD entry from the list detaches it " +
					"without VM replace, which is what appliance-OS install workflows need " +
					"(\"boot from ISO once, remove media on the next apply\"). Pair with a " +
					"`boot_order` change in a follow-up apply.",
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
				MarkdownDescription: "Ordered list of boot devices on a generation 2 VM (UEFI " +
					"firmware). Each entry has a `type` discriminator and the fields " +
					"appropriate for that type:\n\n" +
					"- `type = \"hard_disk_drive\"` or `\"dvd_drive\"`: identify the device " +
					"by `controller_type` + `controller_number` + `controller_location` (the same " +
					"slot tuple used in `hard_disk_drive[]` and `dvd_drive[]`).\n" +
					"- `type = \"network_adapter\"`: identify the NIC by `name`.\n\n" +
					"**Wholesale replacement.** Each plan-vs-state difference triggers " +
					"`Set-VMFirmware -BootOrder` with the entire planned list -- there's no " +
					"partial reorder; an N-element list is set as one atomic call. The VM " +
					"generally must be `Off` for the cmdlet to apply the change.\n\n" +
					"**Generation 1 (BIOS) is rejected.** Gen 1 uses a different mechanism " +
					"(category strings via `Set-VMBios -StartupOrder`) and is not currently " +
					"supported by this resource; a config validator emits a clear error if " +
					"`boot_order` is set on a gen 1 VM.\n\n" +
					"**Appliance-OS install flow:** apply once with `dvd_drive` first in " +
					"`boot_order`, install the OS, then re-apply with `hard_disk_drive` first " +
					"(and the DVD removed from `dvd_drive[]` to eject the install media).\n\n" +
					"**Drift handling:** if someone re-orders the boot list out of band on the " +
					"host, the next refresh detects the drift and the next plan corrects it.",
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
			"state": schema.SingleNestedAttribute{
				Optional: true,
				MarkdownDescription: "Power-state block. Optional: omit to leave the VM at " +
					"whatever power state Hyper-V's default applies (Off for newly created " +
					"VMs). When set, `state.desired` drives transitions and `state.current` " +
					"surfaces the host's actual state.\n\n" +
					"**Transitions:** `Off` -> `Running` calls `Start-VM`; `Running` -> `Off` " +
					"dispatches based on `state.shutdown_mode` (`turn_off` or omitted calls " +
					"`Stop-VM -TurnOff -Force` for hard power-off; `graceful` calls " +
					"`Stop-VM -Force` without `-TurnOff` to send an ACPI shutdown via Hyper-V " +
					"integration services).\n\n" +
					"**VM-must-be-Off rule:** scalar updates (`cpu.count`, `memory.startup_bytes`, " +
					"`secure_boot`) generally require the VM to be `Off`. If `state.desired = \"Running\"` " +
					"and a scalar field also changes in the same plan, the cmdlet errors at " +
					"apply time -- split the change across two applies (transition first, then " +
					"the scalar update) or set `state.desired = \"Off\"` for the duration.\n\n" +
					"**Drift detection:** `state.current` refreshes on every plan, so an " +
					"out-of-band Start-VM / Stop-VM surfaces as a diff that the next apply " +
					"corrects.",
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
						MarkdownDescription: "Actual power state reported by the host. " +
							"Includes transient values (`Starting`, `Stopping`, `Saved`, `Paused`) that " +
							"surface during refresh between transitions.\n\n" +
							"No `UseStateForUnknown` plan modifier: a plan that changes " +
							"`state.desired` would otherwise carry the prior `state.current` into " +
							"the post-apply consistency check, which the framework rejects when " +
							"the actual transition results in a different value. Trade-off: " +
							"plans show `current = (known after apply)` whenever the block is " +
							"in scope, even on no-op apply turns.",
					},
					"shutdown_mode": schema.StringAttribute{
						Optional: true,
						Computed: true,
						MarkdownDescription: "How `Running` -> `Off` transitions are performed. " +
							"Optional. Omit to use Hyper-V's hard-power-off behavior (the same " +
							"as `terraform destroy` semantics) without managing the attribute. " +
							"One of:\n\n" +
							"- `turn_off`: `Stop-VM -TurnOff -Force` -- hard power-off (equivalent " +
							"to pulling the plug). Always safe; no integration-services dependency.\n" +
							"- `graceful`: `Stop-VM -Force` (no `-TurnOff`) -- sends an ACPI shutdown " +
							"signal via Hyper-V integration services and waits for the guest to ack. " +
							"**Hangs indefinitely on guests without integration services running.** " +
							"Opt in only when the guest is known to ship and start integration services " +
							"(modern Windows, most Linux distros with hyperv-daemons).\n\n" +
							"Ignored on `Off` -> `Running` transitions: `Start-VM` has no graceful " +
							"analog, and the field is preserved in state for the next stop transition.\n\n" +
							"**Not applied during `terraform destroy`.** Destroy routes through " +
							"`remove.ps1`, which always hard-powers-off via `Stop-VM -Force -TurnOff` " +
							"before `Remove-VM` so a guest with absent integration services can't " +
							"hang the destroy. Setting `shutdown_mode = \"graceful\"` to protect " +
							"in-flight writes only protects planned `Running` -> `Off` transitions; " +
							"destroy bypasses it. Drive a graceful shutdown out-of-band before " +
							"running `terraform destroy` if a clean stop matters.\n\n" +
							"**Omit semantics:** *omitting* `shutdown_mode` from config after a " +
							"prior apply preserves the existing value via `UseStateForUnknown` (the " +
							"planned value is unknown, the modifier carries state's value into the " +
							"plan). Same shape as `notes` and `secure_boot`.\n\n" +
							"**Explicit `null` semantics differ from `notes` / `secure_boot`.** " +
							"Unlike those attributes -- which have a host-side value that survives " +
							"a null write -- `shutdown_mode` has no host backing. Writing " +
							"`shutdown_mode = null` after a prior `\"graceful\"` value resets state " +
							"to null, and the next `Running` -> `Off` transition reverts to " +
							"`turn_off` (hard power-off) because the wire payload omits the field " +
							"and the script defaults to turn_off on absent input. To preserve a " +
							"value across applies, omit the attribute (don't write null); to switch " +
							"between `\"turn_off\"` and `\"graceful\"`, write the desired value " +
							"explicitly.",
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
				MarkdownDescription: "Flat list of IPv4 / IPv6 addresses the guest's Hyper-V " +
					"integration services have reported across all attached `network_adapter[]` " +
					"entries. Empty when the VM is `Off`, when the guest is still booting, or " +
					"when the guest doesn't ship integration services (rare for modern Windows " +
					"and Linux).\n\n" +
					"**Order is host-driven and not stable across VM restarts.** Hyper-V's " +
					"per-NIC, per-IP order can shuffle on a reboot or when a NIC re-acquires a " +
					"DHCP lease, so downstream resources that reference " +
					"`hyperv_vm.web.ip_addresses[0]` may see the value flip when the host " +
					"happens to surface a different IP first, planning a spurious update. " +
					"**Index into this list only when the VM is single-NIC, single-IP and the " +
					"user trusts that contract operationally.** Multi-homed VMs should use the " +
					"per-NIC `network_adapter[*].ip_addresses` view instead -- it pins the NIC " +
					"selector by deterministic display `name`, eliminating the cross-NIC " +
					"ordering ambiguity. The List-vs-Set trade-off here is intentional: indexing " +
					"is the dominant single-IP use case, and the type may flip to `Set` in a " +
					"future major release if multi-homed users surface real pain.",
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
