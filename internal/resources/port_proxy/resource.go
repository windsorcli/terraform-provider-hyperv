package port_proxy

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-framework/types/basetypes"
	"github.com/hashicorp/terraform-plugin-log/tflog"

	"github.com/windsorcli/terraform-provider-hyperv/internal/hyperv"
)

var (
	_ resource.Resource                = (*Resource)(nil)
	_ resource.ResourceWithConfigure   = (*Resource)(nil)
	_ resource.ResourceWithImportState = (*Resource)(nil)
)

// Resource implements hyperv_port_proxy.
type Resource struct {
	client *hyperv.Client
}

// New is the framework factory.
func New() resource.Resource { return &Resource{} }

// Metadata sets the resource's TF type name.
func (r *Resource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_port_proxy"
}

// Schema returns the schema from schema.go.
func (r *Resource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = resourceSchema()
}

// Configure stashes the provider's typed client, skipping validate-time calls with no provider data.
func (r *Resource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	if req.ProviderData == nil {
		return
	}
	client, ok := req.ProviderData.(*hyperv.Client)
	if !ok {
		resp.Diagnostics.AddError(
			"Unexpected provider data type",
			fmt.Sprintf("hyperv_port_proxy expected *hyperv.Client, got %T", req.ProviderData),
		)
		return
	}
	r.client = client
}

// Create runs new.ps1 and writes the read result back to state.
func (r *Resource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	if r.client == nil {
		resp.Diagnostics.AddError("provider not configured",
			"hyperv_port_proxy Create called before Configure stashed a client.")
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

	tflog.Debug(ctx, "creating hyperv_port_proxy", map[string]any{
		"listen_address": in.ListenAddress,
		"listen_port":    in.ListenPort,
	})
	pp, err := r.client.NewPortProxy(ctx, in)
	if err != nil {
		resp.Diagnostics.AddError("Create hyperv_port_proxy failed", err.Error())
		return
	}

	state, diags := modelFromPortProxy(ctx, pp)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

// Read runs get.ps1 and removes the resource from state when the rule is gone.
func (r *Resource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	if r.client == nil {
		resp.Diagnostics.AddError("provider not configured",
			"hyperv_port_proxy Read called before Configure stashed a client.")
		return
	}

	var state Model
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	// A malformed firewall_rule in state must not keep a rule deleted out of band in state.
	var fwDiags diag.Diagnostics
	pp, err := r.client.GetPortProxy(ctx, hyperv.GetPortProxyInput{
		ListenAddress: state.ListenAddress.ValueString(),
		ListenPort:    int(state.ListenPort.ValueInt64()),
		FirewallName:  stateFirewallName(ctx, state, &fwDiags),
	})
	if errors.Is(err, hyperv.ErrNotFound) {
		tflog.Info(ctx, "hyperv_port_proxy not found; removing from state", map[string]any{
			"id": state.ID.ValueString(),
		})
		resp.State.RemoveResource(ctx)
		return
	}
	resp.Diagnostics.Append(fwDiags...)
	if resp.Diagnostics.HasError() {
		return
	}
	if err != nil {
		resp.Diagnostics.AddError("Read hyperv_port_proxy failed", err.Error())
		return
	}

	newState, diags := modelFromPortProxy(ctx, pp)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &newState)...)
}

// Update runs set.ps1 to retarget the rule and reconcile its firewall rule.
func (r *Resource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	if r.client == nil {
		resp.Diagnostics.AddError("provider not configured",
			"hyperv_port_proxy Update called before Configure stashed a client.")
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

	tflog.Debug(ctx, "updating hyperv_port_proxy", map[string]any{"id": state.ID.ValueString()})
	pp, err := r.client.SetPortProxy(ctx, in)
	if err != nil {
		resp.Diagnostics.AddError("Update hyperv_port_proxy failed", err.Error())
		return
	}

	newState, diags := modelFromPortProxy(ctx, pp)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &newState)...)
}

// Delete runs remove.ps1; RemovePortProxy already treats a rule that is gone as success.
func (r *Resource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	if r.client == nil {
		resp.Diagnostics.AddError("provider not configured",
			"hyperv_port_proxy Delete called before Configure stashed a client.")
		return
	}

	var state Model
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	in := hyperv.RemovePortProxyInput{
		ListenAddress: state.ListenAddress.ValueString(),
		ListenPort:    int(state.ListenPort.ValueInt64()),
		FirewallName:  stateFirewallName(ctx, state, &resp.Diagnostics),
	}
	if resp.Diagnostics.HasError() {
		return
	}

	tflog.Debug(ctx, "deleting hyperv_port_proxy", map[string]any{"id": state.ID.ValueString()})
	if err := r.client.RemovePortProxy(ctx, in); err != nil {
		resp.Diagnostics.AddError("Delete hyperv_port_proxy failed", err.Error())
	}
}

// ImportState accepts `<listen_address>:<listen_port>` or, for a non-default firewall rule name,
// `<listen_address>:<listen_port>:<firewall_rule_name>`. The stored id keeps the two-part form.
func (r *Resource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	parts := strings.SplitN(req.ID, ":", 3)
	if len(parts) < 2 || parts[0] == "" {
		resp.Diagnostics.AddError(
			"Invalid import ID for hyperv_port_proxy",
			fmt.Sprintf("Expected `<listen_address>:<listen_port>` or "+
				"`<listen_address>:<listen_port>:<firewall_rule_name>`; got %q.", req.ID),
		)
		return
	}
	listenAddress := parts[0]
	listenPort, err := strconv.ParseInt(parts[1], 10, 64)
	if err != nil || listenPort < 1 || listenPort > 65535 {
		resp.Diagnostics.AddError(
			"Invalid import ID for hyperv_port_proxy",
			fmt.Sprintf("listen_port %q is not an integer from 1 to 65535.", parts[1]),
		)
		return
	}
	fwName := derivedFirewallRuleName(listenAddress, listenPort)
	if len(parts) == 3 && parts[2] != "" {
		fwName = parts[2]
	}

	// Seeds enabled/profile defaults so the first Read after import starts from a complete object.
	fwObj, fwDiags := types.ObjectValueFrom(ctx, firewallRuleAttrTypes(), FirewallRuleModel{
		Enabled: types.BoolValue(true),
		Name:    types.StringValue(fwName),
		Profile: types.StringValue("Any"),
	})
	resp.Diagnostics.Append(fwDiags...)
	if resp.Diagnostics.HasError() {
		return
	}

	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), fmt.Sprintf("%s:%d", listenAddress, listenPort))...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("listen_address"), listenAddress)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("listen_port"), listenPort)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("firewall_rule"), fwObj)...)
}

// derivedFirewallRuleName is the default firewall rule name; it includes the listen address
// so two rules on the same port with different listen addresses don't share one firewall rule.
func derivedFirewallRuleName(listenAddress string, listenPort int64) string {
	return fmt.Sprintf("hyperv-pp-%s-%d", listenAddress, listenPort)
}

// unpackFirewallRule extracts the nested model; a null or unknown object yields all-null fields.
func unpackFirewallRule(ctx context.Context, obj types.Object) (FirewallRuleModel, diag.Diagnostics) {
	var fw FirewallRuleModel
	if obj.IsNull() || obj.IsUnknown() {
		return fw, nil
	}
	diags := obj.As(ctx, &fw, basetypes.ObjectAsOptions{})
	return fw, diags
}

// stateFirewallName returns the firewall rule name recorded in state, or the derived default.
func stateFirewallName(ctx context.Context, state Model, diags *diag.Diagnostics) string {
	fw, fwDiags := unpackFirewallRule(ctx, state.FirewallRule)
	diags.Append(fwDiags...)
	if name := fw.Name.ValueString(); name != "" {
		return name
	}
	return derivedFirewallRuleName(state.ListenAddress.ValueString(), state.ListenPort.ValueInt64())
}

// resolveFirewall applies the runtime defaults for a firewall_rule object; name comes from the caller.
func resolveFirewall(fw FirewallRuleModel, name string) hyperv.PortProxyFirewallInput {
	in := hyperv.PortProxyFirewallInput{Enabled: true, Name: name, Profile: "Any"}
	if !fw.Enabled.IsNull() && !fw.Enabled.IsUnknown() {
		in.Enabled = fw.Enabled.ValueBool()
	}
	if !fw.Profile.IsNull() && !fw.Profile.IsUnknown() {
		in.Profile = fw.Profile.ValueString()
	}
	return in
}

// buildNewInput translates a Create plan into new.ps1's input.
func buildNewInput(ctx context.Context, plan Model) (hyperv.NewPortProxyInput, diag.Diagnostics) {
	fw, diags := unpackFirewallRule(ctx, plan.FirewallRule)
	if diags.HasError() {
		return hyperv.NewPortProxyInput{}, diags
	}
	name := fw.Name.ValueString()
	if fw.Name.IsNull() || fw.Name.IsUnknown() || name == "" {
		name = derivedFirewallRuleName(plan.ListenAddress.ValueString(), plan.ListenPort.ValueInt64())
	}
	return hyperv.NewPortProxyInput{
		ListenAddress:  plan.ListenAddress.ValueString(),
		ListenPort:     int(plan.ListenPort.ValueInt64()),
		ConnectAddress: plan.ConnectAddress.ValueString(),
		ConnectPort:    int(plan.ConnectPort.ValueInt64()),
		Firewall:       resolveFirewall(fw, name),
	}, diags
}

// buildSetInput takes the listen pair and firewall name from state, since both force replacement,
// and the connect pair and firewall settings from plan.
func buildSetInput(ctx context.Context, plan, state Model) (hyperv.SetPortProxyInput, diag.Diagnostics) {
	fw, diags := unpackFirewallRule(ctx, plan.FirewallRule)
	name := stateFirewallName(ctx, state, &diags)
	if diags.HasError() {
		return hyperv.SetPortProxyInput{}, diags
	}
	return hyperv.SetPortProxyInput{
		ListenAddress:  state.ListenAddress.ValueString(),
		ListenPort:     int(state.ListenPort.ValueInt64()),
		ConnectAddress: plan.ConnectAddress.ValueString(),
		ConnectPort:    int(plan.ConnectPort.ValueInt64()),
		Firewall:       resolveFirewall(fw, name),
	}, diags
}

// modelFromPortProxy hydrates a Model from the read result.
func modelFromPortProxy(ctx context.Context, pp *hyperv.PortProxy) (Model, diag.Diagnostics) {
	profile := pp.FirewallRuleProfile
	if profile == "" {
		profile = "Any"
	}
	fwObj, diags := types.ObjectValueFrom(ctx, firewallRuleAttrTypes(), FirewallRuleModel{
		Enabled: types.BoolValue(pp.FirewallRulePresent),
		Name:    types.StringValue(pp.FirewallRuleName),
		Profile: types.StringValue(profile),
	})
	return Model{
		ID:             types.StringValue(pp.ID),
		ListenAddress:  types.StringValue(pp.ListenAddress),
		ListenPort:     types.Int64Value(int64(pp.ListenPort)),
		ConnectAddress: types.StringValue(pp.ConnectAddress),
		ConnectPort:    types.Int64Value(int64(pp.ConnectPort)),
		FirewallRule:   fwObj,
	}, diags
}
