package vm

import (
	"context"

	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/types"

	pathtype "github.com/windsorcli/terraform-provider-hyperv/internal/types/path"
)

// priorModelV0 is the tfsdk-bound shape of hyperv_vm state files written under
// schema version 0 (PR #20, the original "minimal first slice"). v0 was a
// flat struct: vcpu and memory_bytes as top-level Int64s, state as a
// top-level computed StringAttribute, and no inline attachment lists. v1
// (this PR) renames vcpu -> cpu.count, memory_bytes -> memory.startup_bytes,
// promotes state from a flat string to a {desired, current} nested block,
// and adds inline hard_disk_drive[]/network_adapter[]/dvd_drive[]/boot_order[]
// plus ip_addresses. The shape mismatch makes any v0 state file undecodable
// against the v1 schema; this upgrader bridges the two.
//
// Field types here mirror v0 exactly. Tags align with priorSchemaV0.
type priorModelV0 struct {
	ID          types.String `tfsdk:"id"`
	Name        types.String `tfsdk:"name"`
	Generation  types.Int64  `tfsdk:"generation"`
	VCPU        types.Int64  `tfsdk:"vcpu"`
	MemoryBytes types.Int64  `tfsdk:"memory_bytes"`
	SecureBoot  types.Bool   `tfsdk:"secure_boot"`
	Notes       types.String `tfsdk:"notes"`
	State       types.String `tfsdk:"state"`
	Path        types.String `tfsdk:"path"`
}

// priorSchemaV0 returns the schema as it shipped on main prior to this PR.
// Only attribute types/cardinality matter for state decoding -- MarkdownDescription,
// validators, and plan modifiers are intentionally omitted because the
// framework only uses this to materialize the prior state into priorModelV0.
func priorSchemaV0() schema.Schema {
	return schema.Schema{
		Attributes: map[string]schema.Attribute{
			"id":           schema.StringAttribute{Computed: true},
			"name":         schema.StringAttribute{Required: true},
			"generation":   schema.Int64Attribute{Required: true},
			"vcpu":         schema.Int64Attribute{Required: true},
			"memory_bytes": schema.Int64Attribute{Required: true},
			"secure_boot":  schema.BoolAttribute{Optional: true, Computed: true},
			"notes":        schema.StringAttribute{Optional: true, Computed: true},
			"state":        schema.StringAttribute{Computed: true},
			"path":         schema.StringAttribute{Computed: true},
		},
	}
}

// UpgradeState bridges schema versions for hyperv_vm. Each entry maps
// from a SOURCE version directly to the current (v2) shape; the
// framework dispatches based on the on-disk version, NOT a chain.
func (r *Resource) UpgradeState(_ context.Context) map[int64]resource.StateUpgrader {
	return map[int64]resource.StateUpgrader{
		0: {
			PriorSchema: ptrSchema(priorSchemaV0()),
			StateUpgrader: func(ctx context.Context, req resource.UpgradeStateRequest, resp *resource.UpgradeStateResponse) {
				var prior priorModelV0
				resp.Diagnostics.Append(req.State.Get(ctx, &prior)...)
				if resp.Diagnostics.HasError() {
					return
				}
				upgraded := upgradeV0ToV1(prior)
				resp.Diagnostics.Append(resp.State.Set(ctx, &upgraded)...)
			},
		},
		1: {
			PriorSchema: ptrSchema(priorSchemaV1()),
			StateUpgrader: func(ctx context.Context, req resource.UpgradeStateRequest, resp *resource.UpgradeStateResponse) {
				var prior priorModelV1
				resp.Diagnostics.Append(req.State.Get(ctx, &prior)...)
				if resp.Diagnostics.HasError() {
					return
				}
				upgraded := upgradeV1ToV2(prior)
				resp.Diagnostics.Append(resp.State.Set(ctx, &upgraded)...)
			},
		},
		2: {
			PriorSchema: ptrSchema(priorSchemaV2()),
			StateUpgrader: func(ctx context.Context, req resource.UpgradeStateRequest, resp *resource.UpgradeStateResponse) {
				var prior priorModelV2
				resp.Diagnostics.Append(req.State.Get(ctx, &prior)...)
				if resp.Diagnostics.HasError() {
					return
				}
				upgraded := upgradeV2ToV3(prior)
				resp.Diagnostics.Append(resp.State.Set(ctx, &upgraded)...)
			},
		},
	}
}

// priorStateModelV1 is the v1 shape of the `state` nested block, before
// shutdown_mode was added. priorModelV1 uses this for its State field
// so the framework decodes v1 state files cleanly.
type priorStateModelV1 struct {
	Desired types.String `tfsdk:"desired"`
	Current types.String `tfsdk:"current"`
}

// priorMemoryModelV1V2 is the shape of the `memory` nested block on v1
// and v2 (before dynamic / min_bytes / max_bytes were added in v3).
// Shared between priorModelV1 and priorModelV2.
type priorMemoryModelV1V2 struct {
	StartupBytes types.Int64 `tfsdk:"startup_bytes"`
}

// priorModelV1 mirrors the v1 Model. Identical to the current Model
// except StateModel lacks shutdown_mode and MemoryModel lacks the
// dynamic-memory fields. Carrying separate types keeps the framework's
// tfsdk decoder happy when it materializes a v1 state file: the current
// Model has fields the v1 file won't have on disk.
type priorModelV1 struct {
	ID              types.String          `tfsdk:"id"`
	Name            types.String          `tfsdk:"name"`
	Generation      types.Int64           `tfsdk:"generation"`
	CPU             *CPUModel             `tfsdk:"cpu"`
	Memory          *priorMemoryModelV1V2 `tfsdk:"memory"`
	HardDiskDrives  []HardDiskDriveModel  `tfsdk:"hard_disk_drive"`
	NetworkAdapters []NetworkAdapterModel `tfsdk:"network_adapter"`
	DvdDrives       []DvdDriveModel       `tfsdk:"dvd_drive"`
	BootOrder       []BootOrderEntryModel `tfsdk:"boot_order"`
	SecureBoot      types.Bool            `tfsdk:"secure_boot"`
	Notes           types.String          `tfsdk:"notes"`
	State           *priorStateModelV1    `tfsdk:"state"`
	IPAddresses     types.List            `tfsdk:"ip_addresses"`
	Path            types.String          `tfsdk:"path"`
}

// priorModelV2 mirrors the v2 Model. Identical to the current Model
// except MemoryModel lacks the dynamic-memory fields (the only v2 ->
// v3 change). State carries shutdown_mode (added in v2) so the
// current StateModel works.
type priorModelV2 struct {
	ID              types.String          `tfsdk:"id"`
	Name            types.String          `tfsdk:"name"`
	Generation      types.Int64           `tfsdk:"generation"`
	CPU             *CPUModel             `tfsdk:"cpu"`
	Memory          *priorMemoryModelV1V2 `tfsdk:"memory"`
	HardDiskDrives  []HardDiskDriveModel  `tfsdk:"hard_disk_drive"`
	NetworkAdapters []NetworkAdapterModel `tfsdk:"network_adapter"`
	DvdDrives       []DvdDriveModel       `tfsdk:"dvd_drive"`
	BootOrder       []BootOrderEntryModel `tfsdk:"boot_order"`
	SecureBoot      types.Bool            `tfsdk:"secure_boot"`
	Notes           types.String          `tfsdk:"notes"`
	State           *StateModel           `tfsdk:"state"`
	IPAddresses     types.List            `tfsdk:"ip_addresses"`
	Path            types.String          `tfsdk:"path"`
}

// priorSchemaV1 mirrors the v1 schema's structural shape -- attribute
// names and types only. Defaults / validators / plan modifiers /
// MarkdownDescription are intentionally omitted because the framework
// only needs structural information to decode a stored state file.
//
// Keep in sync with resourceSchema() ATTRIBUTE NAMES AND TYPES, MINUS
// the v2-only state.shutdown_mode addition. If a future v2 -> v3
// migration adds another attribute, snapshot the v2 shape here as a
// new priorSchemaV2.
func priorSchemaV1() schema.Schema {
	return schema.Schema{
		Attributes: map[string]schema.Attribute{
			"id":         schema.StringAttribute{Computed: true},
			"name":       schema.StringAttribute{Required: true},
			"generation": schema.Int64Attribute{Required: true},
			"cpu": schema.SingleNestedAttribute{
				Required: true,
				Attributes: map[string]schema.Attribute{
					"count": schema.Int64Attribute{Required: true},
				},
			},
			"memory": schema.SingleNestedAttribute{
				Required: true,
				Attributes: map[string]schema.Attribute{
					"startup_bytes": schema.Int64Attribute{Required: true},
				},
			},
			"hard_disk_drive": schema.ListNestedAttribute{
				Optional: true,
				Computed: true,
				NestedObject: schema.NestedAttributeObject{
					Attributes: map[string]schema.Attribute{
						"path":                schema.StringAttribute{CustomType: pathtype.Type, Required: true},
						"controller_type":     schema.StringAttribute{Optional: true, Computed: true},
						"controller_number":   schema.Int64Attribute{Required: true},
						"controller_location": schema.Int64Attribute{Required: true},
					},
				},
			},
			"network_adapter": schema.ListNestedAttribute{
				Optional: true,
				Computed: true,
				NestedObject: schema.NestedAttributeObject{
					Attributes: map[string]schema.Attribute{
						"name":        schema.StringAttribute{Required: true},
						"switch_name": schema.StringAttribute{Required: true},
					},
				},
			},
			"dvd_drive": schema.ListNestedAttribute{
				Optional: true,
				Computed: true,
				NestedObject: schema.NestedAttributeObject{
					Attributes: map[string]schema.Attribute{
						"iso_path":            schema.StringAttribute{CustomType: pathtype.Type, Optional: true},
						"controller_type":     schema.StringAttribute{Optional: true, Computed: true},
						"controller_number":   schema.Int64Attribute{Required: true},
						"controller_location": schema.Int64Attribute{Required: true},
					},
				},
			},
			"boot_order": schema.ListNestedAttribute{
				Optional: true,
				Computed: true,
				NestedObject: schema.NestedAttributeObject{
					Attributes: map[string]schema.Attribute{
						"type":                schema.StringAttribute{Required: true},
						"controller_type":     schema.StringAttribute{Optional: true, Computed: true},
						"controller_number":   schema.Int64Attribute{Optional: true, Computed: true},
						"controller_location": schema.Int64Attribute{Optional: true, Computed: true},
						"name":                schema.StringAttribute{Optional: true, Computed: true},
					},
				},
			},
			"secure_boot": schema.BoolAttribute{Optional: true, Computed: true},
			"notes":       schema.StringAttribute{Optional: true, Computed: true},
			"state": schema.SingleNestedAttribute{
				Optional: true,
				Attributes: map[string]schema.Attribute{
					"desired": schema.StringAttribute{Optional: true},
					"current": schema.StringAttribute{Computed: true},
				},
			},
			"ip_addresses": schema.ListAttribute{Computed: true, ElementType: types.StringType},
			"path":         schema.StringAttribute{Computed: true},
		},
	}
}

// priorSchemaV2 mirrors the v2 schema's structural shape: the v1 shape
// plus the v2-only state.shutdown_mode field. Memory still has only
// startup_bytes -- dynamic / min_bytes / max_bytes are the v3 addition.
//
// Keep in sync with resourceSchema() ATTRIBUTE NAMES AND TYPES, MINUS
// the v3-only memory.{dynamic, min_bytes, max_bytes} additions. Same
// "structural-only" rule as priorSchemaV1.
func priorSchemaV2() schema.Schema {
	return schema.Schema{
		Attributes: map[string]schema.Attribute{
			"id":         schema.StringAttribute{Computed: true},
			"name":       schema.StringAttribute{Required: true},
			"generation": schema.Int64Attribute{Required: true},
			"cpu": schema.SingleNestedAttribute{
				Required: true,
				Attributes: map[string]schema.Attribute{
					"count": schema.Int64Attribute{Required: true},
				},
			},
			"memory": schema.SingleNestedAttribute{
				Required: true,
				Attributes: map[string]schema.Attribute{
					"startup_bytes": schema.Int64Attribute{Required: true},
				},
			},
			"hard_disk_drive": schema.ListNestedAttribute{
				Optional: true,
				Computed: true,
				NestedObject: schema.NestedAttributeObject{
					Attributes: map[string]schema.Attribute{
						"path":                schema.StringAttribute{CustomType: pathtype.Type, Required: true},
						"controller_type":     schema.StringAttribute{Optional: true, Computed: true},
						"controller_number":   schema.Int64Attribute{Required: true},
						"controller_location": schema.Int64Attribute{Required: true},
					},
				},
			},
			"network_adapter": schema.ListNestedAttribute{
				Optional: true,
				Computed: true,
				NestedObject: schema.NestedAttributeObject{
					Attributes: map[string]schema.Attribute{
						"name":        schema.StringAttribute{Required: true},
						"switch_name": schema.StringAttribute{Required: true},
					},
				},
			},
			"dvd_drive": schema.ListNestedAttribute{
				Optional: true,
				Computed: true,
				NestedObject: schema.NestedAttributeObject{
					Attributes: map[string]schema.Attribute{
						"iso_path":            schema.StringAttribute{CustomType: pathtype.Type, Optional: true},
						"controller_type":     schema.StringAttribute{Optional: true, Computed: true},
						"controller_number":   schema.Int64Attribute{Required: true},
						"controller_location": schema.Int64Attribute{Required: true},
					},
				},
			},
			"boot_order": schema.ListNestedAttribute{
				Optional: true,
				Computed: true,
				NestedObject: schema.NestedAttributeObject{
					Attributes: map[string]schema.Attribute{
						"type":                schema.StringAttribute{Required: true},
						"controller_type":     schema.StringAttribute{Optional: true, Computed: true},
						"controller_number":   schema.Int64Attribute{Optional: true, Computed: true},
						"controller_location": schema.Int64Attribute{Optional: true, Computed: true},
						"name":                schema.StringAttribute{Optional: true, Computed: true},
					},
				},
			},
			"secure_boot": schema.BoolAttribute{Optional: true, Computed: true},
			"notes":       schema.StringAttribute{Optional: true, Computed: true},
			"state": schema.SingleNestedAttribute{
				Optional: true,
				Attributes: map[string]schema.Attribute{
					"desired":       schema.StringAttribute{Optional: true},
					"current":       schema.StringAttribute{Computed: true},
					"shutdown_mode": schema.StringAttribute{Optional: true, Computed: true},
				},
			},
			"ip_addresses": schema.ListAttribute{Computed: true, ElementType: types.StringType},
			"path":         schema.StringAttribute{Computed: true},
		},
	}
}

// upgradeV1ToV2 maps a v1 state struct into the v2 Model. The only
// shape change is state.shutdown_mode being added; v1 state values
// migrate with ShutdownMode left null because v1 users never had
// the option to manage it. The script's wire contract treats absent
// shutdown_mode as the turn_off behavior (same as v1's implicit
// behavior), so existing state files come up running the same path
// without storing a phantom value the user never chose. The user
// opts into "graceful" by editing the config. Pure function for
// direct unit testing.
func upgradeV1ToV2(prior priorModelV1) Model {
	var state *StateModel
	if prior.State != nil {
		state = &StateModel{
			Desired:      prior.State.Desired,
			Current:      prior.State.Current,
			ShutdownMode: types.StringNull(),
		}
	}
	return Model{
		ID:              prior.ID,
		Name:            prior.Name,
		Generation:      prior.Generation,
		CPU:             prior.CPU,
		Memory:          expandPriorMemoryV1V2(prior.Memory),
		HardDiskDrives:  prior.HardDiskDrives,
		NetworkAdapters: prior.NetworkAdapters,
		DvdDrives:       prior.DvdDrives,
		BootOrder:       prior.BootOrder,
		SecureBoot:      prior.SecureBoot,
		Notes:           prior.Notes,
		State:           state,
		IPAddresses:     prior.IPAddresses,
		Path:            prior.Path,
	}
}

// expandPriorMemoryV1V2 maps the v1/v2 memory shape into the v3 model.
// v1/v2 had only StartupBytes; v3 adds dynamic / min_bytes / max_bytes.
// The new fields land null because pre-v3 users never had a chance to
// choose values, and the script's wire contract treats absent
// dynamic_memory as static (matches v1/v2 on-host behavior).
func expandPriorMemoryV1V2(prior *priorMemoryModelV1V2) *MemoryModel {
	if prior == nil {
		return nil
	}
	return &MemoryModel{
		StartupBytes: prior.StartupBytes,
		Dynamic:      types.BoolNull(),
		MinBytes:     types.Int64Null(),
		MaxBytes:     types.Int64Null(),
	}
}

// upgradeV2ToV3 maps a v2 state struct into the v3 Model. The only
// shape change is memory.{dynamic, min_bytes, max_bytes} being added;
// v2 state values migrate with the new fields null because v2 users
// never had a chance to choose values, and absent dynamic_memory on
// the wire is the same on-host behavior as v2.
func upgradeV2ToV3(prior priorModelV2) Model {
	return Model{
		ID:              prior.ID,
		Name:            prior.Name,
		Generation:      prior.Generation,
		CPU:             prior.CPU,
		Memory:          expandPriorMemoryV1V2(prior.Memory),
		HardDiskDrives:  prior.HardDiskDrives,
		NetworkAdapters: prior.NetworkAdapters,
		DvdDrives:       prior.DvdDrives,
		BootOrder:       prior.BootOrder,
		SecureBoot:      prior.SecureBoot,
		Notes:           prior.Notes,
		State:           prior.State,
		IPAddresses:     prior.IPAddresses,
		Path:            prior.Path,
	}
}

// upgradeV0ToV1 maps a v0 state struct into the v1 Model. Extracted as a
// pure function so the conversion logic is unit-testable without
// constructing tfsdk.State and tftypes raw values just to exercise the
// rename mappings.
func upgradeV0ToV1(prior priorModelV0) Model {
	return Model{
		ID:         prior.ID,
		Name:       prior.Name,
		Generation: prior.Generation,
		CPU:        &CPUModel{Count: prior.VCPU},
		Memory:     &MemoryModel{StartupBytes: prior.MemoryBytes},
		SecureBoot: prior.SecureBoot,
		Notes:      prior.Notes,
		Path:       prior.Path,

		// New inline list attributes did not exist at v0. The next
		// refresh fills them from the host; until then, empty (known)
		// lists keep the post-upgrade state shape valid against the
		// v1 schema.
		HardDiskDrives:  []HardDiskDriveModel{},
		NetworkAdapters: []NetworkAdapterModel{},
		DvdDrives:       []DvdDriveModel{},
		BootOrder:       []BootOrderEntryModel{},
		IPAddresses:     types.ListNull(types.StringType),

		// v0 state was a flat Computed StringAttribute -- users had
		// no way to manage power state on this resource, so the v1
		// state block is left null (Optional, "not managed"). The
		// next refresh repopulates state.current; state.desired stays
		// null until the user opts in.
		State: nil,
	}
}

// ptrSchema returns a *schema.Schema pointing at s. resource.StateUpgrader's
// PriorSchema field expects a pointer; this saves callers from declaring
// a temporary just to take its address.
func ptrSchema(s schema.Schema) *schema.Schema { return &s }

// Compile-time guard: the Resource implements ResourceWithUpgradeState.
// Listed alongside the other resource.* interface assertions in resource.go.
var _ resource.ResourceWithUpgradeState = (*Resource)(nil)
