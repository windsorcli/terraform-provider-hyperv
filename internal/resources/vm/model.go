// Package vm implements the hyperv_vm resource, wrapping the
// vm/{get,new,set,remove}.ps1 contract via the typed hyperv.Client.
// CPU and memory live in nested blocks per ADR-0001.
package vm

import (
	"context"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/types"

	mactype "github.com/windsorcli/terraform-provider-hyperv/internal/types/mac"
	pathtype "github.com/windsorcli/terraform-provider-hyperv/internal/types/path"
)

// Model is the tfsdk-bound struct backing the resource state. Field tags
// align with schema.go attribute names; conversion to/from the typed
// hyperv.VM DTO lives in resource.go.
type Model struct {
	ID         types.String `tfsdk:"id"`
	VMID       types.String `tfsdk:"vm_id"`
	Name       types.String `tfsdk:"name"`
	Generation types.Int64  `tfsdk:"generation"`
	// CPU and Memory are pointer-typed: ImportState briefly leaves state with only `name` set, which a value type can't represent.
	CPU    *CPUModel    `tfsdk:"cpu"`
	Memory *MemoryModel `tfsdk:"memory"`
	// HardDiskDrives is types.List, not []Struct, so an unknown plan
	// value decodes cleanly; List instead of Set works around a
	// terraform-plugin-framework v1.19 reflect-decode error on nested-set slices.
	HardDiskDrives  types.List `tfsdk:"hard_disk_drive"`
	NetworkAdapters types.List `tfsdk:"network_adapter"`
	DvdDrives       types.List `tfsdk:"dvd_drive"`
	BootOrder       types.List `tfsdk:"boot_order"`
	// SecureBoot is types.Bool, not *bool: null cleanly represents a gen-1 host with no Secure Boot concept.
	SecureBoot         types.Bool   `tfsdk:"secure_boot"`
	SecureBootTemplate types.String `tfsdk:"secure_boot_template"`
	Notes              types.String `tfsdk:"notes"`
	State              *StateModel  `tfsdk:"state"`
	IPAddresses        types.List   `tfsdk:"ip_addresses"`
	Path               types.String `tfsdk:"path"`
}

// CPUModel is the nested `cpu` block. Static count only in this slice;
// dynamic-CPU attributes (weight, reserve, limit) attach as additional
// fields here in a follow-up.
type CPUModel struct {
	Count types.Int64 `tfsdk:"count"`
}

// MemoryModel is the nested `memory` block. StartupBytes is required;
// Dynamic / MinBytes / MaxBytes opt in to Hyper-V's dynamic memory
// mode, following the same Optional+Computed+UseStateForUnknown
// pattern as state.shutdown_mode. Buffer and Priority are deferred.
type MemoryModel struct {
	StartupBytes types.Int64 `tfsdk:"startup_bytes"`
	Dynamic      types.Bool  `tfsdk:"dynamic"`
	MinBytes     types.Int64 `tfsdk:"min_bytes"`
	MaxBytes     types.Int64 `tfsdk:"max_bytes"`
}

// HardDiskDriveModel is one element of the `hard_disk_drive` nested
// set on hyperv_vm, identifying an attached VHD (Path) at a specific
// controller slot (ControllerType + ControllerNumber +
// ControllerLocation). Path uses pathtype.Path for slash/case folding
// consistent with hyperv_vhd.path and
// hyperv_image_file.destination_path, so a forward-slash path
// round-trips against the bench's canonical backslash form without
// phantom diffs.
type HardDiskDriveModel struct {
	Path               pathtype.Path `tfsdk:"path"`
	ControllerType     types.String  `tfsdk:"controller_type"`
	ControllerNumber   types.Int64   `tfsdk:"controller_number"`
	ControllerLocation types.Int64   `tfsdk:"controller_location"`
}

// HardDiskDriveAttrTypes mirrors HardDiskDriveModel's tfsdk tags.
var HardDiskDriveAttrTypes = map[string]attr.Type{
	"path":                pathtype.Type,
	"controller_type":     types.StringType,
	"controller_number":   types.Int64Type,
	"controller_location": types.Int64Type,
}

// HardDiskDriveListElementType is the Object type hard_disk_drive elements use.
var HardDiskDriveListElementType = types.ObjectType{AttrTypes: HardDiskDriveAttrTypes}

// HardDiskDriveModels decodes m.HardDiskDrives, or nil if null/unknown.
func (m *Model) HardDiskDriveModels(ctx context.Context) ([]HardDiskDriveModel, diag.Diagnostics) {
	if m.HardDiskDrives.IsNull() || m.HardDiskDrives.IsUnknown() {
		return nil, nil
	}
	out := make([]HardDiskDriveModel, 0, len(m.HardDiskDrives.Elements()))
	diags := m.HardDiskDrives.ElementsAs(ctx, &out, false)
	return out, diags
}

// HardDiskDriveListFromSlice builds a types.List from a slice; nil -> null, empty -> empty.
func HardDiskDriveListFromSlice(ctx context.Context, slice []HardDiskDriveModel) (types.List, diag.Diagnostics) {
	if slice == nil {
		return types.ListNull(HardDiskDriveListElementType), nil
	}
	return types.ListValueFrom(ctx, HardDiskDriveListElementType, slice)
}

// NetworkAdapterModel is one element of the `network_adapter` list on
// hyperv_vm.
type NetworkAdapterModel struct {
	// Name is the slot key for diff/reconciliation; the schema validator enforces uniqueness within a VM's list at plan time.
	Name       types.String `tfsdk:"name"`
	SwitchName types.String `tfsdk:"switch_name"`
	// IPAddresses is Computed from the host, giving a multi-homed VM a per-NIC view distinct from the VM-level ip_addresses list.
	IPAddresses types.List `tfsdk:"ip_addresses"`
	// MacAddress is Optional, not Computed: Computed would mask an intentional revert to dynamic-MAC mode by copying state forward.
	MacAddress mactype.MAC `tfsdk:"mac_address"`
	// VlanID is Optional, not Computed, for the same reason as MacAddress; unset means untagged, not "not yet read."
	VlanID types.Int64 `tfsdk:"vlan_id"`
}

// NetworkAdapterAttrTypes mirrors NetworkAdapterModel's tfsdk tags.
var NetworkAdapterAttrTypes = map[string]attr.Type{
	"name":         types.StringType,
	"switch_name":  types.StringType,
	"ip_addresses": types.ListType{ElemType: types.StringType},
	"mac_address":  mactype.Type,
	"vlan_id":      types.Int64Type,
}

// NetworkAdapterListElementType is the Object type network_adapter elements use.
var NetworkAdapterListElementType = types.ObjectType{AttrTypes: NetworkAdapterAttrTypes}

// NetworkAdapterModels decodes m.NetworkAdapters, or nil if null/unknown.
func (m *Model) NetworkAdapterModels(ctx context.Context) ([]NetworkAdapterModel, diag.Diagnostics) {
	if m.NetworkAdapters.IsNull() || m.NetworkAdapters.IsUnknown() {
		return nil, nil
	}
	out := make([]NetworkAdapterModel, 0, len(m.NetworkAdapters.Elements()))
	diags := m.NetworkAdapters.ElementsAs(ctx, &out, false)
	return out, diags
}

// NetworkAdapterListFromSlice builds a types.List from a slice; nil -> null, empty -> empty.
func NetworkAdapterListFromSlice(ctx context.Context, slice []NetworkAdapterModel) (types.List, diag.Diagnostics) {
	if slice == nil {
		return types.ListNull(NetworkAdapterListElementType), nil
	}
	return types.ListValueFrom(ctx, NetworkAdapterListElementType, slice)
}

// DvdDriveModel is one element of the `dvd_drive` list on hyperv_vm.
// Same slot tuple as HardDiskDriveModel (controller_type,
// controller_number, controller_location), but IsoPath is Optional --
// an empty DVD drive (no medium loaded) is a legitimate config.
type DvdDriveModel struct {
	IsoPath            pathtype.Path `tfsdk:"iso_path"`
	ControllerType     types.String  `tfsdk:"controller_type"`
	ControllerNumber   types.Int64   `tfsdk:"controller_number"`
	ControllerLocation types.Int64   `tfsdk:"controller_location"`
}

// BootOrderEntryModel is one element of the `boot_order` list on a
// gen 2 hyperv_vm. Type discriminates between hard_disk_drive /
// dvd_drive entries, which carry the slot tuple, and network_adapter
// entries, which carry Name; unused fields for a given Type are null.
// Gen 1 BIOS startup order is a separate, deferred slice, and the
// schema validator rejects boot_order on gen 1 at plan time.
type BootOrderEntryModel struct {
	Type               types.String `tfsdk:"type"`
	ControllerType     types.String `tfsdk:"controller_type"`
	ControllerNumber   types.Int64  `tfsdk:"controller_number"`
	ControllerLocation types.Int64  `tfsdk:"controller_location"`
	Name               types.String `tfsdk:"name"`
}

// DvdDriveAttrTypes mirrors DvdDriveModel's tfsdk-tagged fields.
// Used by the Object element type that backs the dvd_drive list, by
// the helpers below for ListValueFrom / ElementsAs round-trips, and by
// schema.go's Default empty-list value.
var DvdDriveAttrTypes = map[string]attr.Type{
	"iso_path":            pathtype.Type,
	"controller_type":     types.StringType,
	"controller_number":   types.Int64Type,
	"controller_location": types.Int64Type,
}

// DvdDriveListElementType is the Object element type the dvd_drive
// list holds. Computed once so all helpers share the same instance.
var DvdDriveListElementType = types.ObjectType{AttrTypes: DvdDriveAttrTypes}

// BootOrderEntryAttrTypes mirrors BootOrderEntryModel's tfsdk-tagged
// fields. Same role as DvdDriveAttrTypes for the boot_order list.
var BootOrderEntryAttrTypes = map[string]attr.Type{
	"type":                types.StringType,
	"controller_type":     types.StringType,
	"controller_number":   types.Int64Type,
	"controller_location": types.Int64Type,
	"name":                types.StringType,
}

// BootOrderEntryListElementType is the Object element type the
// boot_order list holds.
var BootOrderEntryListElementType = types.ObjectType{AttrTypes: BootOrderEntryAttrTypes}

// DvdDriveModels returns the typed slice underlying m.DvdDrives, or
// nil if the list is null or unknown. Resource code that needs to
// distinguish "user explicitly set []" from "user did not set the
// attribute" should inspect m.DvdDrives directly via IsNull /
// IsUnknown -- a known-empty list returns an empty (but non-nil)
// slice here.
func (m *Model) DvdDriveModels(ctx context.Context) ([]DvdDriveModel, diag.Diagnostics) {
	if m.DvdDrives.IsNull() || m.DvdDrives.IsUnknown() {
		return nil, nil
	}
	out := make([]DvdDriveModel, 0, len(m.DvdDrives.Elements()))
	diags := m.DvdDrives.ElementsAs(ctx, &out, false)
	return out, diags
}

// DvdDriveListFromSlice builds a types.List from a typed slice. A nil
// slice becomes a null list (matching the "DvdDrives not managed"
// semantics on a fresh VM); an empty slice becomes an empty list
// (the schema's Default value when the user omits the attribute).
func DvdDriveListFromSlice(ctx context.Context, slice []DvdDriveModel) (types.List, diag.Diagnostics) {
	if slice == nil {
		return types.ListNull(DvdDriveListElementType), nil
	}
	return types.ListValueFrom(ctx, DvdDriveListElementType, slice)
}

// BootOrderEntries returns the typed slice underlying m.BootOrder, or
// nil if null/unknown. Same null-vs-empty rationale as DvdDriveModels.
func (m *Model) BootOrderEntries(ctx context.Context) ([]BootOrderEntryModel, diag.Diagnostics) {
	if m.BootOrder.IsNull() || m.BootOrder.IsUnknown() {
		return nil, nil
	}
	out := make([]BootOrderEntryModel, 0, len(m.BootOrder.Elements()))
	diags := m.BootOrder.ElementsAs(ctx, &out, false)
	return out, diags
}

// BootOrderListFromSlice builds a types.List from a typed slice.
// Nil slice -> null list; empty slice -> empty list. Same rationale
// as DvdDriveListFromSlice.
func BootOrderListFromSlice(ctx context.Context, slice []BootOrderEntryModel) (types.List, diag.Diagnostics) {
	if slice == nil {
		return types.ListNull(BootOrderEntryListElementType), nil
	}
	return types.ListValueFrom(ctx, BootOrderEntryListElementType, slice)
}

// StateModel is the nested `state` block on hyperv_vm. Pointer-typed
// for the same reason as CPU and Memory. Desired is the user-facing
// power-state input ("Off" | "Running"), a transition fires only when
// it differs from the host's actual state, and Current is the
// Computed readback from Hyper-V.
type StateModel struct {
	Desired      types.String `tfsdk:"desired"`
	Current      types.String `tfsdk:"current"`
	ShutdownMode types.String `tfsdk:"shutdown_mode"`
}
