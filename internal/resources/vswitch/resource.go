package vswitch

import (
	"context"
	"errors"
	"fmt"

	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-log/tflog"

	"github.com/windsorcli/terraform-provider-hyperv/internal/hyperv"
)

var (
	_ resource.Resource                = (*Resource)(nil)
	_ resource.ResourceWithConfigure   = (*Resource)(nil)
	_ resource.ResourceWithImportState = (*Resource)(nil)
)

// Resource implements hyperv_virtual_switch.
type Resource struct {
	client *hyperv.Client
}

// New is the framework factory.
func New() resource.Resource { return &Resource{} }

// Metadata sets the resource's TF type name.
func (r *Resource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_virtual_switch"
}

// Schema returns the locked-in schema (see schema.go).
func (r *Resource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = resourceSchema()
}

// Configure stashes the typed Hyper-V client built by the provider's
// Configure pass. Skips when ProviderData is nil (validate-time invocation
// before the provider has resolved its config).
func (r *Resource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	if req.ProviderData == nil {
		return
	}
	client, ok := req.ProviderData.(*hyperv.Client)
	if !ok {
		resp.Diagnostics.AddError(
			"Unexpected provider data type",
			fmt.Sprintf("hyperv_virtual_switch expected *hyperv.Client, got %T", req.ProviderData),
		)
		return
	}
	r.client = client
}

// Create runs new.ps1 with the plan's attributes and writes the post-create
// read shape back to state.
func (r *Resource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	if r.client == nil {
		resp.Diagnostics.AddError("provider not configured",
			"hyperv_virtual_switch Create called before Configure stashed a client.")
		return
	}

	var plan Model
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	in, diags := buildNewInput(ctx, plan)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	tflog.Debug(ctx, "creating hyperv_virtual_switch", map[string]any{"name": in.Name, "switch_type": in.SwitchType})
	sw, err := r.client.NewVMSwitch(ctx, in)
	if err != nil {
		resp.Diagnostics.AddError("Create hyperv_virtual_switch failed", err.Error())
		return
	}

	state := modelFromVMSwitch(ctx, sw, plan.NetAdapterNames, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

// Read fetches the current shape via get.ps1 and reconciles state.
//
// ErrNotFound -> RemoveResource so Terraform plans recreate.
// ErrUnavailable -> AddError so a transient vmms outage doesn't drop the
// resource from state. (See errors.go for the sentinel rationale.)
func (r *Resource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	if r.client == nil {
		resp.Diagnostics.AddError("provider not configured",
			"hyperv_virtual_switch Read called before Configure stashed a client.")
		return
	}

	var state Model
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	sw, err := r.client.GetVMSwitch(ctx, state.Name.ValueString())
	if err != nil {
		if errors.Is(err, hyperv.ErrNotFound) {
			tflog.Info(ctx, "hyperv_virtual_switch not found; removing from state", map[string]any{
				"name": state.Name.ValueString(),
			})
			resp.State.RemoveResource(ctx)
			return
		}
		resp.Diagnostics.AddError("Read hyperv_virtual_switch failed", err.Error())
		return
	}

	// net_adapter_names is user intent and isn't reconstructible from the
	// cmdlet's read shape (Get-VMSwitch reports NetAdapterInterfaceDescription
	// -- a friendly NIC label -- not the original adapter name list). Keep
	// the prior state's value so subsequent plans don't show phantom diffs.
	newState := modelFromVMSwitch(ctx, sw, state.NetAdapterNames, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &newState)...)
}

// Update runs set.ps1 with the plan's mutable attributes (net_adapter_names,
// allow_management_os, notes) and writes the post-update read shape back.
//
// switch_type is taken from STATE -- the schema marks it RequiresReplace, so
// any change there forces destroy+recreate rather than reaching Update --
// and forwarded so set.ps1's Private + AllowManagementOS guard fires with
// a clear error instead of the cmdlet's opaque one.
func (r *Resource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	if r.client == nil {
		resp.Diagnostics.AddError("provider not configured",
			"hyperv_virtual_switch Update called before Configure stashed a client.")
		return
	}

	var plan, state Model
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	in, diags := buildSetInput(ctx, plan, state)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	tflog.Debug(ctx, "updating hyperv_virtual_switch", map[string]any{"name": in.Name})
	sw, err := r.client.SetVMSwitch(ctx, in)
	if err != nil {
		resp.Diagnostics.AddError("Update hyperv_virtual_switch failed", err.Error())
		return
	}

	newState := modelFromVMSwitch(ctx, sw, plan.NetAdapterNames, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &newState)...)
}

// Delete runs remove.ps1. ErrNotFound is treated as success -- the switch
// is already gone, no need to error.
func (r *Resource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	if r.client == nil {
		resp.Diagnostics.AddError("provider not configured",
			"hyperv_virtual_switch Delete called before Configure stashed a client.")
		return
	}

	var state Model
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	tflog.Debug(ctx, "deleting hyperv_virtual_switch", map[string]any{"name": state.Name.ValueString()})
	err := r.client.RemoveVMSwitch(ctx, state.Name.ValueString())
	if err != nil && !errors.Is(err, hyperv.ErrNotFound) {
		resp.Diagnostics.AddError("Delete hyperv_virtual_switch failed", err.Error())
		return
	}
}

// ImportState lets `terraform import hyperv_virtual_switch.foo my-switch`
// work by treating the import ID as the switch name. Read populates the
// rest of the attributes by calling GetVMSwitch.
func (r *Resource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	resource.ImportStatePassthroughID(ctx, path.Root("name"), req, resp)
}

// buildNewInput translates a Create plan into the wire-level NewVMSwitchInput.
// Optional fields become *T pointers so omitempty drops absent attributes
// from the JSON entirely.
func buildNewInput(ctx context.Context, plan Model) (hyperv.NewVMSwitchInput, diag.Diagnostics) {
	var diags diag.Diagnostics
	in := hyperv.NewVMSwitchInput{
		Name:       plan.Name.ValueString(),
		SwitchType: plan.SwitchType.ValueString(),
	}
	if !plan.NetAdapterNames.IsNull() && !plan.NetAdapterNames.IsUnknown() {
		var names []string
		diags.Append(plan.NetAdapterNames.ElementsAs(ctx, &names, false)...)
		in.NetAdapterNames = names
	}
	if !plan.AllowManagementOS.IsNull() && !plan.AllowManagementOS.IsUnknown() {
		v := plan.AllowManagementOS.ValueBool()
		in.AllowManagementOS = &v
	}
	if !plan.Notes.IsNull() && !plan.Notes.IsUnknown() {
		v := plan.Notes.ValueString()
		in.Notes = &v
	}
	return in, diags
}

// buildSetInput translates an Update plan + state into a SetVMSwitchInput.
// SwitchType is sourced from state (immutable per RequiresReplace) so the
// script's Private + AllowManagementOS guard can fire.
func buildSetInput(ctx context.Context, plan, state Model) (hyperv.SetVMSwitchInput, diag.Diagnostics) {
	var diags diag.Diagnostics
	in := hyperv.SetVMSwitchInput{
		Name:       plan.Name.ValueString(),
		SwitchType: state.SwitchType.ValueString(),
	}
	if !plan.NetAdapterNames.IsNull() && !plan.NetAdapterNames.IsUnknown() {
		var names []string
		diags.Append(plan.NetAdapterNames.ElementsAs(ctx, &names, false)...)
		in.NetAdapterNames = names
	}
	if !plan.AllowManagementOS.IsNull() && !plan.AllowManagementOS.IsUnknown() {
		v := plan.AllowManagementOS.ValueBool()
		in.AllowManagementOS = &v
	}
	if !plan.Notes.IsNull() && !plan.Notes.IsUnknown() {
		v := plan.Notes.ValueString()
		in.Notes = &v
	}
	return in, diags
}

// modelFromVMSwitch hydrates a Model from a typed VMSwitch DTO. Caller
// supplies the net_adapter_names list separately because that attribute is
// user intent (config/plan) -- the cmdlet's read shape exposes only
// NetAdapterInterfaceDescription, which is a friendly label, not the
// adapter-name list the user originally passed.
func modelFromVMSwitch(ctx context.Context, sw *hyperv.VMSwitch, netAdapterNames types.List, diags *diag.Diagnostics) Model {
	// Preserve the user's net_adapter_names if it's known; fall back to an
	// empty list when unknown/null so state doesn't end up holding an
	// unknown value across an apply.
	adapterNames := netAdapterNames
	if adapterNames.IsNull() || adapterNames.IsUnknown() {
		empty, d := types.ListValueFrom(ctx, types.StringType, []string{})
		diags.Append(d...)
		adapterNames = empty
	}
	return Model{
		ID:                             types.StringValue(sw.Name),
		Name:                           types.StringValue(sw.Name),
		SwitchType:                     types.StringValue(sw.SwitchType),
		NetAdapterNames:                adapterNames,
		AllowManagementOS:              types.BoolValue(sw.AllowManagementOS),
		Notes:                          types.StringValue(sw.Notes),
		NetAdapterInterfaceDescription: types.StringValue(sw.NetAdapterInterfaceDescription),
	}
}
