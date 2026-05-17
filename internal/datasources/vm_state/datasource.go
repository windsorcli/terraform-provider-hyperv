// Package vm_state implements the hyperv_vm_state data source -- read-only
// power-state plus IP-address lookup for an existing VM by name. Useful
// for HCL conditionals and downstream resources that gate on the live
// VM state without needing to manage the VM itself.
//
// Pairs with hyperv_vm.state.{desired, current, shutdown_mode}: the
// resource manages the transition; this data source reports it.
package vm_state //nolint:revive // underscore in package matches the resource directory naming pattern.

import (
	"context"
	"errors"
	"fmt"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-log/tflog"

	"github.com/windsorcli/terraform-provider-hyperv/internal/hyperv"
	"github.com/windsorcli/terraform-provider-hyperv/internal/typeflatten"
)

var (
	_ datasource.DataSource              = (*DataSource)(nil)
	_ datasource.DataSourceWithConfigure = (*DataSource)(nil)
)

// DataSource implements data.hyperv_vm_state.
type DataSource struct {
	client *hyperv.Client
}

// New is the framework factory.
func New() datasource.DataSource { return &DataSource{} }

// Metadata sets the data source's TF type name.
func (d *DataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_vm_state"
}

// Schema declares the read shape: name is the lookup key (Required);
// everything else is read-only Computed pulled from Get-VM.
//
// The deliberately narrow surface (current + ip_addresses) reflects the
// data source's purpose: gate downstream HCL on power state. Callers
// who need the full VM read shape (memory, attachments, boot order)
// should use the hyperv_vm resource's state.
func (d *DataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "**Requirements:** Membership in the **Hyper-V Administrators** group on " +
			"the target host (or read access to `Get-VM` via a JEA endpoint).\n\n" +
			"Reads live power state and reported IP addresses for an existing Hyper-V " +
			"virtual machine by name. Useful for HCL conditionals and downstream resources that gate " +
			"on whether the VM is `Running` (e.g. provisioners that wait for the guest to come up) " +
			"without managing the VM itself.\n\n" +
			"Refreshed on every plan: an out-of-band `Start-VM` / `Stop-VM` surfaces immediately. " +
			"Pairs with `hyperv_vm.state.{desired, current, shutdown_mode}` -- the resource manages " +
			"transitions; this data source reports them.",
		Attributes: map[string]schema.Attribute{
			"name": schema.StringAttribute{
				Required:            true,
				MarkdownDescription: "VM name. The lookup key.",
			},
			"id": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "Resource identifier. Mirrors `name` -- VM names are unique per host.",
			},
			"current": schema.StringAttribute{
				Computed: true,
				MarkdownDescription: "Actual power state reported by the host. One of `Off`, `Running`, " +
					"`Saved`, `Paused`, `Starting`, `Stopping`, ... -- transient values surface during " +
					"refresh between transitions.",
			},
			"ip_addresses": schema.ListAttribute{
				Computed:    true,
				ElementType: types.StringType,
				MarkdownDescription: "Flat list of IPv4 / IPv6 addresses the guest's Hyper-V " +
					"integration services have reported across all attached NICs. Empty when the " +
					"VM is `Off`, when the guest is still booting, or when the guest doesn't ship " +
					"integration services.\n\n" +
					"**Order is host-driven and not stable across VM restarts.** Hyper-V's per-NIC, " +
					"per-IP order can shuffle on a reboot or when a NIC re-acquires a DHCP lease, " +
					"and a data source is evaluated on every plan -- so any downstream resource that " +
					"references `data.hyperv_vm_state.web.ip_addresses[0]` will see the value flip " +
					"when the host happens to surface a different IP first, planning a spurious " +
					"update. **Index into this list only when the VM is single-NIC, single-IP and " +
					"the user trusts that contract operationally.** Multi-homed VMs should pin to " +
					"a specific NIC via `hyperv_vm.network_adapter[*].ip_addresses` -- the per-NIC " +
					"view keys off the deterministic display `name` and eliminates the cross-NIC " +
					"ordering ambiguity. The List-vs-Set trade-off here is intentional: indexing " +
					"is the dominant single-IP use case, and the type may flip to `Set` in a " +
					"future major release if multi-homed users surface real pain.",
			},
		},
	}
}

// Configure stashes the typed Hyper-V client built by the provider's
// Configure pass. Skips when ProviderData is nil (validate-time invocation
// before the provider has resolved its config).
func (d *DataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	if req.ProviderData == nil {
		return
	}
	client, ok := req.ProviderData.(*hyperv.Client)
	if !ok {
		resp.Diagnostics.AddError(
			"Unexpected provider data type",
			fmt.Sprintf("data.hyperv_vm_state expected *hyperv.Client, got %T", req.ProviderData),
		)
		return
	}
	d.client = client
}

// Read fetches the VM via Get-VM and writes the typed state-only shape into
// state. ErrNotFound surfaces as an attribute-anchored diagnostic so the
// operator sees which `name` value didn't resolve. ErrUnavailable surfaces
// with transient phrasing so a vmms blip during a plan doesn't read like
// "the VM is gone".
func (d *DataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	if d.client == nil {
		resp.Diagnostics.AddError(
			"Hyper-V provider not configured",
			"data.hyperv_vm_state was invoked before the provider stashed a client. "+
				"Usually means a required provider attribute was unknown at plan time.",
		)
		return
	}

	var config Model
	resp.Diagnostics.Append(req.Config.Get(ctx, &config)...)
	if resp.Diagnostics.HasError() {
		return
	}

	tflog.Debug(ctx, "reading hyperv_vm_state", map[string]any{"name": config.Name.ValueString()})
	state, diags := readVMState(ctx, d.client, config.Name.ValueString())
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

// Model is the tfsdk-bound state struct.
type Model struct {
	Name        types.String `tfsdk:"name"`
	ID          types.String `tfsdk:"id"`
	Current     types.String `tfsdk:"current"`
	IPAddresses types.List   `tfsdk:"ip_addresses"`
}

// readVMState is the framework-detached core: easy to unit-test against a
// hyperv.Client backed by a fakeRunner. Maps client errors to anchored
// diagnostics so test cases can assert on the user-facing message without
// constructing a full ReadRequest.
func readVMState(ctx context.Context, c *hyperv.Client, name string) (Model, diag.Diagnostics) {
	var diags diag.Diagnostics

	v, err := c.GetVM(ctx, name)
	if err != nil {
		switch {
		case errors.Is(err, hyperv.ErrNotFound):
			diags.AddAttributeError(
				path.Root("name"),
				"Hyper-V virtual machine not found",
				fmt.Sprintf("No VM named %q exists on the host. Underlying error: %s", name, err),
			)
		case errors.Is(err, hyperv.ErrUnavailable):
			diags.AddError(
				"Hyper-V management service is unavailable",
				fmt.Sprintf("Get-VM could reach the host but Hyper-V is not responding. "+
					"Confirm vmms is running. Underlying error: %s", err),
			)
		default:
			diags.AddError("Read hyperv_vm_state failed", err.Error())
		}
		return Model{}, diags
	}

	return Model{
		Name:        types.StringValue(v.Name),
		ID:          types.StringValue(v.Name),
		Current:     types.StringValue(v.State),
		IPAddresses: typeflatten.IPAddresses(v.NetworkAdapters),
	}, diags
}
