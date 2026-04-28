// Package vm implements the hyperv_vm resource (M4 minimal first slice).
// Wraps the vm/{get,new,set,remove}.ps1 contract via the typed
// hyperv.Client.
//
// Excluded from this slice (each becomes its own follow-up PR):
//   - boot_order (gen1 BIOS / gen2 UEFI translation deserves its own design)
//   - dynamic memory (min_bytes / max_bytes / buffer / priority on the
//     existing memory block)
//   - integration services map
//   - automatic start/stop actions
//   - checkpoint type/policy
//   - VM path overrides (defaults from Get-VMHost)
//
// CPU and memory live in nested blocks per ADR-0001: `cpu = { count = N }`
// and `memory = { startup_bytes = N }`. The blocks exist as nested
// attributes (rather than flat top-level fields) so dynamic-CPU and
// dynamic-memory follow-ups attach to the same block without flattening
// more attribute names into the top namespace -- e.g. cpu.weight,
// cpu.reserve, memory.min_bytes, memory.max_bytes.
package vm

import (
	"github.com/hashicorp/terraform-plugin-framework/types"

	pathtype "github.com/windsorcli/terraform-provider-hyperv/internal/types/path"
)

// Model is the tfsdk-bound struct backing the resource state. Field tags
// align with schema.go attribute names; conversion to/from the typed
// hyperv.VM DTO lives in resource.go.
//
// SecureBoot is types.Bool (not pointer-to-bool) -- the framework's null
// representation handles the gen-1 case where the host has no Secure Boot
// concept and the cmdlet returns null.
//
// CPU and Memory are pointer-typed (not value) because at ImportState
// time the state passes through with just `name` set; the framework
// then calls Read to populate everything else, but in the brief
// window before Read runs, cpu/memory are null in state. A value
// type can't represent null and the framework errors with "Received
// null value, however the target type cannot handle null values."
// Pointers null cleanly; modelFromVM allocates them.
//
// HardDiskDrives is a list of attachments stored canonically by slot
// tuple (controller_type, controller_number, controller_location).
// Schema-side ListNestedAttribute (rather than SetNestedAttribute)
// because terraform-plugin-framework v1.19's slice decode of
// nested-set attributes hits a reflect path that produces a
// "Target Type: []vm.HardDiskDriveModel, Suggested Type:
// basetypes.SetValue" error during req.Plan.Get. List + a canonical
// sort in modelFromVM gives the same user-visible behavior (HCL
// ordering matches canonical state on subsequent applies) with a
// simpler decode.
type Model struct {
	ID              types.String          `tfsdk:"id"`
	Name            types.String          `tfsdk:"name"`
	Generation      types.Int64           `tfsdk:"generation"`
	CPU             *CPUModel             `tfsdk:"cpu"`
	Memory          *MemoryModel          `tfsdk:"memory"`
	HardDiskDrives  []HardDiskDriveModel  `tfsdk:"hard_disk_drive"`
	NetworkAdapters []NetworkAdapterModel `tfsdk:"network_adapter"`
	DvdDrives       []DvdDriveModel       `tfsdk:"dvd_drive"`
	SecureBoot      types.Bool            `tfsdk:"secure_boot"`
	Notes           types.String          `tfsdk:"notes"`
	State           types.String          `tfsdk:"state"`
	Path            types.String          `tfsdk:"path"`
}

// CPUModel is the nested `cpu` block. Static count only in this slice;
// dynamic-CPU attributes (weight, reserve, limit) attach as additional
// fields here in a follow-up.
type CPUModel struct {
	Count types.Int64 `tfsdk:"count"`
}

// MemoryModel is the nested `memory` block. Static memory only in this
// slice (DynamicMemoryEnabled=$false on the wire); dynamic memory adds
// MinBytes / MaxBytes alongside StartupBytes in a follow-up.
type MemoryModel struct {
	StartupBytes types.Int64 `tfsdk:"startup_bytes"`
}

// HardDiskDriveModel is one element of the `hard_disk_drive` nested set
// on hyperv_vm. Identifies an attached VHD (Path) at a specific
// controller slot (ControllerType + ControllerNumber + ControllerLocation).
//
// Path uses pathtype.Path for slash/case folding consistent with
// hyperv_vhd.path and hyperv_image_file.destination_path -- a VHD path
// the user wrote with forward slashes round-trips through the bench's
// canonical backslash form without phantom diffs.
type HardDiskDriveModel struct {
	Path               pathtype.Path `tfsdk:"path"`
	ControllerType     types.String  `tfsdk:"controller_type"`
	ControllerNumber   types.Int64   `tfsdk:"controller_number"`
	ControllerLocation types.Int64   `tfsdk:"controller_location"`
}

// NetworkAdapterModel is one element of the `network_adapter` list on
// hyperv_vm. Display Name is the slot key for diff/reconciliation
// (Hyper-V allows duplicate-named NICs at the cmdlet level, but the
// resource-layer schema validator enforces uniqueness within a VM's
// list at plan time, so the slot key is well-defined).
//
// SwitchName binds the NIC to a hyperv_virtual_switch by name.
// Required in this slice; future commits may add Optional defaulting
// to "unbound" if a real use case surfaces.
type NetworkAdapterModel struct {
	Name       types.String `tfsdk:"name"`
	SwitchName types.String `tfsdk:"switch_name"`
}

// DvdDriveModel is one element of the `dvd_drive` list on hyperv_vm.
// Same slot-tuple shape as HardDiskDriveModel (controller_type,
// controller_number, controller_location), but IsoPath is Optional --
// an empty DVD drive (no medium loaded) is a legitimate config.
//
// IsoPath uses pathtype.Path for slash-style folding consistent with
// hyperv_image_file.destination_path and hyperv_vhd.path.
type DvdDriveModel struct {
	IsoPath            pathtype.Path `tfsdk:"iso_path"`
	ControllerType     types.String  `tfsdk:"controller_type"`
	ControllerNumber   types.Int64   `tfsdk:"controller_number"`
	ControllerLocation types.Int64   `tfsdk:"controller_location"`
}
