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
	_ resource.Resource                     = (*Resource)(nil)
	_ resource.ResourceWithConfigure        = (*Resource)(nil)
	_ resource.ResourceWithConfigValidators = (*Resource)(nil)
	_ resource.ResourceWithImportState      = (*Resource)(nil)
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

// ConfigValidators surfaces cross-attribute checks at plan time. The script
// layer still enforces the same invariants as defense-in-depth (direct script
// invocation in tests bypasses the framework), but plan-time rejection is
// the better UX -- `terraform validate` and `plan` catch the bad config
// before any cmdlet runs.
func (r *Resource) ConfigValidators(_ context.Context) []resource.ConfigValidator {
	return []resource.ConfigValidator{
		privateAllowMgmtOSValidator{},
		externalRequiresAdapterNamesValidator{},
	}
}

// privateAllowMgmtOSValidator rejects allow_management_os when switch_type
// is Private. New-VMSwitch errors with "parameter is not applicable" for
// that combination; rejecting at plan time gives a clearer diagnostic
// anchored to the offending attribute.
type privateAllowMgmtOSValidator struct{}

// Description is a one-line summary the framework surfaces in `terraform
// validate -json` output and on schema-introspection paths.
func (v privateAllowMgmtOSValidator) Description(_ context.Context) string {
	return "allow_management_os is not valid for switch_type 'Private'"
}

// MarkdownDescription mirrors Description -- the rule has no markdown-only
// formatting beyond the plain string.
func (v privateAllowMgmtOSValidator) MarkdownDescription(ctx context.Context) string {
	return v.Description(ctx)
}

// ValidateResource fires the diagnostic when both attributes are known and
// the combination is invalid. Unknown values mean a deferred dependency
// hasn't resolved yet; the next plan pass with concrete values gets the
// chance to validate.
func (v privateAllowMgmtOSValidator) ValidateResource(ctx context.Context, req resource.ValidateConfigRequest, resp *resource.ValidateConfigResponse) {
	var data Model
	resp.Diagnostics.Append(req.Config.Get(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}
	// Skip if any input is unknown -- a deferred dep hasn't resolved yet;
	// the next plan pass will validate with concrete values.
	if data.SwitchType.IsUnknown() || data.AllowManagementOS.IsUnknown() {
		return
	}
	if data.SwitchType.ValueString() == "Private" && !data.AllowManagementOS.IsNull() {
		resp.Diagnostics.AddAttributeError(
			path.Root("allow_management_os"),
			"allow_management_os not valid for Private switch",
			"Private switches don't bind to a host NIC, so allow_management_os has no effect. "+
				"Remove the attribute or change switch_type to External or Internal.",
		)
	}
}

// externalRequiresAdapterNamesValidator rejects External switches that don't
// supply at least one host NIC name. New-VMSwitch errors at apply time with
// "Cannot bind argument to parameter 'NetAdapterName'" otherwise; rejecting
// at plan time gives a clearer diagnostic anchored to the offending attribute.
type externalRequiresAdapterNamesValidator struct{}

func (v externalRequiresAdapterNamesValidator) Description(_ context.Context) string {
	return "net_adapter_names is required when switch_type = 'External'"
}

func (v externalRequiresAdapterNamesValidator) MarkdownDescription(ctx context.Context) string {
	return v.Description(ctx)
}

func (v externalRequiresAdapterNamesValidator) ValidateResource(ctx context.Context, req resource.ValidateConfigRequest, resp *resource.ValidateConfigResponse) {
	var data Model
	resp.Diagnostics.Append(req.Config.Get(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if data.SwitchType.IsUnknown() || data.NetAdapterNames.IsUnknown() {
		return
	}
	if data.SwitchType.ValueString() != "External" {
		return
	}
	if data.NetAdapterNames.IsNull() {
		resp.Diagnostics.AddAttributeError(
			path.Root("net_adapter_names"),
			"net_adapter_names required for External switch",
			"External switches must bind to one or more host NICs. Set net_adapter_names "+
				"to a non-empty list, or change switch_type to Internal or Private.",
		)
		return
	}
	var names []string
	resp.Diagnostics.Append(data.NetAdapterNames.ElementsAs(ctx, &names, false)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if len(names) == 0 {
		resp.Diagnostics.AddAttributeError(
			path.Root("net_adapter_names"),
			"net_adapter_names must be non-empty for External switch",
			"External switches must bind to at least one host NIC. Either supply one or "+
				"more adapter names, or change switch_type to Internal or Private.",
		)
	}
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
	// Don't forward allow_management_os for Private switches. The attribute
	// is Optional+Computed, so plan carries the prior-state value (false on
	// Private since there's no host NIC) even when the user never set it --
	// forwarding it would trip set.ps1's "not valid for Private" guard on
	// every Update of a Private switch.
	if state.SwitchType.ValueString() != "Private" &&
		!plan.AllowManagementOS.IsNull() && !plan.AllowManagementOS.IsUnknown() {
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
