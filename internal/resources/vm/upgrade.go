package vm

import (
	"context"

	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/types"
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

// UpgradeState bridges schema versions for hyperv_vm.
//
// Currently registers a single upgrader (v0 -> v1). Future schema bumps
// append entries to the returned map; the framework chains them
// automatically (e.g. v0 state replays v0->v1->v2 on a v2 binary).
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
