// Package vm implements the hyperv_vm resource (M4 minimal first slice).
// Wraps the vm/{get,new,set,remove}.ps1 contract via the typed
// hyperv.Client.
//
// Excluded from this slice (each becomes its own follow-up PR):
//   - boot_order (gen1 BIOS / gen2 UEFI translation deserves its own design)
//   - dynamic memory (startup/min/max/buffer/priority)
//   - integration services map
//   - automatic start/stop actions
//   - checkpoint type/policy
//   - VM path overrides (defaults from Get-VMHost)
package vm

import "github.com/hashicorp/terraform-plugin-framework/types"

// Model is the tfsdk-bound struct backing the resource state. Field tags
// align with schema.go attribute names; conversion to/from the typed
// hyperv.VM DTO lives in resource.go.
//
// SecureBoot is types.Bool (not pointer-to-bool) -- the framework's null
// representation handles the gen-1 case where the host has no Secure Boot
// concept and the cmdlet returns null.
type Model struct {
	ID          types.String `tfsdk:"id"`
	Name        types.String `tfsdk:"name"`
	Generation  types.Int64  `tfsdk:"generation"`
	Vcpu        types.Int64  `tfsdk:"vcpu"`
	MemoryBytes types.Int64  `tfsdk:"memory_bytes"`
	SecureBoot  types.Bool   `tfsdk:"secure_boot"`
	Notes       types.String `tfsdk:"notes"`
	State       types.String `tfsdk:"state"`
	Path        types.String `tfsdk:"path"`
}
