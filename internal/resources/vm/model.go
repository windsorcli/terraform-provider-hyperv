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

import "github.com/hashicorp/terraform-plugin-framework/types"

// Model is the tfsdk-bound struct backing the resource state. Field tags
// align with schema.go attribute names; conversion to/from the typed
// hyperv.VM DTO lives in resource.go.
//
// SecureBoot is types.Bool (not pointer-to-bool) -- the framework's null
// representation handles the gen-1 case where the host has no Secure Boot
// concept and the cmdlet returns null.
//
// CPU and Memory are value-typed nested structs (not pointer) because the
// schema marks both blocks Required: true; a missing block is a config
// error caught at plan time, not a nil dereference here.
type Model struct {
	ID         types.String `tfsdk:"id"`
	Name       types.String `tfsdk:"name"`
	Generation types.Int64  `tfsdk:"generation"`
	CPU        CPUModel     `tfsdk:"cpu"`
	Memory     MemoryModel  `tfsdk:"memory"`
	SecureBoot types.Bool   `tfsdk:"secure_boot"`
	Notes      types.String `tfsdk:"notes"`
	State      types.String `tfsdk:"state"`
	Path       types.String `tfsdk:"path"`
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
