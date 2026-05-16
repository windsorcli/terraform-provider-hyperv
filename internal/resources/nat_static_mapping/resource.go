package nat_static_mapping

import (
	"context"
	"errors"
	"fmt"
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

// Resource implements hyperv_nat_static_mapping.
type Resource struct {
	client *hyperv.Client
}

// New is the framework factory.
func New() resource.Resource { return &Resource{} }

// Metadata sets the resource's TF type name.
func (r *Resource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_nat_static_mapping"
}

// Schema returns the locked-in schema (see schema.go).
func (r *Resource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = resourceSchema()
}

// Configure stashes the typed Hyper-V client built by the provider's
// Configure pass. Skips when ProviderData is nil (validate-time
// invocation before the provider has resolved its config).
func (r *Resource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	if req.ProviderData == nil {
		return
	}
	client, ok := req.ProviderData.(*hyperv.Client)
	if !ok {
		resp.Diagnostics.AddError(
			"Unexpected provider data type",
			fmt.Sprintf("hyperv_nat_static_mapping expected *hyperv.Client, got %T", req.ProviderData),
		)
		return
	}
	r.client = client
}

// Create runs new.ps1 with the plan's attributes and writes the
// post-create read shape back to state.
func (r *Resource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	if r.client == nil {
		resp.Diagnostics.AddError("provider not configured",
			"hyperv_nat_static_mapping Create called before Configure stashed a client.")
		return
	}

	var plan Model
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	in, fwName, diags := buildNewInput(ctx, plan)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	tflog.Debug(ctx, "creating hyperv_nat_static_mapping", map[string]any{
		"nat_name":      in.NatName,
		"protocol":      in.Protocol,
		"external_port": in.ExternalPort,
	})
	pf, err := r.client.NewNatStaticMapping(ctx, in)
	if err != nil {
		resp.Diagnostics.AddError("Create hyperv_nat_static_mapping failed", err.Error())
		return
	}

	state, diags := modelFromNatStaticMapping(ctx, pf, fwName)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

// Read fetches the current shape via get.ps1 and reconciles state.
//
// ErrNotFound -> RemoveResource so Terraform plans recreate.
func (r *Resource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	if r.client == nil {
		resp.Diagnostics.AddError("provider not configured",
			"hyperv_nat_static_mapping Read called before Configure stashed a client.")
		return
	}

	var state Model
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	fw, fwDiags := unpackFirewallRule(ctx, state.FirewallRule)
	resp.Diagnostics.Append(fwDiags...)
	if resp.Diagnostics.HasError() {
		return
	}

	pf, err := r.client.GetNatStaticMapping(ctx, hyperv.GetNatStaticMappingInput{
		NatName:           state.NatName.ValueString(),
		Protocol:          state.Protocol.ValueString(),
		ExternalIPAddress: state.ExternalIP.ValueString(),
		ExternalPort:      int(state.ExternalPort.ValueInt64()),
		FirewallName:      fw.Name.ValueString(),
	})
	if err != nil {
		if errors.Is(err, hyperv.ErrNotFound) {
			tflog.Info(ctx, "hyperv_nat_static_mapping not found; removing from state", map[string]any{
				"id": state.ID.ValueString(),
			})
			resp.State.RemoveResource(ctx)
			return
		}
		resp.Diagnostics.AddError("Read hyperv_nat_static_mapping failed", err.Error())
		return
	}

	newState, diags := modelFromNatStaticMapping(ctx, pf, fw.Name.ValueString())
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &newState)...)
}

// Update runs set.ps1 with the plan's mutable attributes (internal_ip,
// internal_port, firewall_rule.{enabled, profile}) and writes the
// post-update read shape back. The lookup tuple (nat_name, protocol,
// external_ip, external_port, firewall_rule.name) is RequiresReplace
// at the schema layer, so any change there forces destroy+recreate
// rather than reaching Update.
func (r *Resource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	if r.client == nil {
		resp.Diagnostics.AddError("provider not configured",
			"hyperv_nat_static_mapping Update called before Configure stashed a client.")
		return
	}

	var plan, state Model
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	in, fwName, diags := buildSetInput(ctx, plan, state)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	tflog.Debug(ctx, "updating hyperv_nat_static_mapping", map[string]any{
		"id": state.ID.ValueString(),
	})
	pf, err := r.client.SetNatStaticMapping(ctx, in)
	if err != nil {
		resp.Diagnostics.AddError("Update hyperv_nat_static_mapping failed", err.Error())
		return
	}

	newState, diags := modelFromNatStaticMapping(ctx, pf, fwName)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &newState)...)
}

// Delete runs remove.ps1. ErrNotFound is treated as success -- the
// mapping is already gone, no need to error.
func (r *Resource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	if r.client == nil {
		resp.Diagnostics.AddError("provider not configured",
			"hyperv_nat_static_mapping Delete called before Configure stashed a client.")
		return
	}

	var state Model
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	fw, fwDiags := unpackFirewallRule(ctx, state.FirewallRule)
	resp.Diagnostics.Append(fwDiags...)
	if resp.Diagnostics.HasError() {
		return
	}

	tflog.Debug(ctx, "deleting hyperv_nat_static_mapping", map[string]any{
		"id": state.ID.ValueString(),
	})
	err := r.client.RemoveNatStaticMapping(ctx, hyperv.RemoveNatStaticMappingInput{
		NatName:           state.NatName.ValueString(),
		Protocol:          state.Protocol.ValueString(),
		ExternalIPAddress: state.ExternalIP.ValueString(),
		ExternalPort:      int(state.ExternalPort.ValueInt64()),
		FirewallName:      fw.Name.ValueString(),
	})
	if err != nil && !errors.Is(err, hyperv.ErrNotFound) {
		resp.Diagnostics.AddError("Delete hyperv_nat_static_mapping failed", err.Error())
	}
}

// ImportState parses the composite identifier and seeds the lookup
// tuple in state. Two forms are accepted:
//
//	<nat_name>:<protocol>:<external_ip>:<external_port>
//	<nat_name>:<protocol>:<external_ip>:<external_port>:<firewall_rule_name>
//
// The 5-segment form lets users adopt an existing netnat-static-mapping whose
// firewall rule has a non-default DisplayName -- without it, `Read`
// can't locate the rule (it keys on the name) and `firewall_rule.name`
// in state lands as the derived default; any later config that sets a
// different name then trips `RequiresReplace` on the first plan. The
// 4-segment form falls back to `derivedFirewallRuleName(...)` so users
// who created their resource via this provider (or with the same naming
// convention) can import without knowing the rule name.
//
// The composite `id` attribute keeps its 4-segment form regardless --
// it's the resource's stable identifier, not a re-export of the
// import-only firewall name.
func (r *Resource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	parts := strings.Split(req.ID, ":")
	if len(parts) != 4 && len(parts) != 5 {
		resp.Diagnostics.AddError(
			"Invalid import ID for hyperv_nat_static_mapping",
			fmt.Sprintf("Expected `<nat_name>:<protocol>:<external_ip>:<external_port>` or "+
				"`<nat_name>:<protocol>:<external_ip>:<external_port>:<firewall_rule_name>`; got %q.", req.ID),
		)
		return
	}
	natName, proto, externalIP, externalPortStr := parts[0], parts[1], parts[2], parts[3]
	var externalPort int64
	if _, err := fmt.Sscanf(externalPortStr, "%d", &externalPort); err != nil {
		resp.Diagnostics.AddError(
			"Invalid import ID for hyperv_nat_static_mapping",
			fmt.Sprintf("external_port %q is not an integer: %s", externalPortStr, err),
		)
		return
	}
	fwName := derivedFirewallRuleName(proto, externalPort)
	if len(parts) == 5 && parts[4] != "" {
		fwName = parts[4]
	}
	compositeID := fmt.Sprintf("%s:%s:%s:%s", natName, proto, externalIP, externalPortStr)

	// Seed the firewall_rule object with the resolved name plus
	// schema defaults so the framework's "Computed value drift" guard
	// doesn't trip when Read lands its own values. The first Read
	// after import will overwrite enabled / profile from the host's
	// joined NatStaticMapping + NetFirewallRule view; the name is
	// the load-bearing import input because the firewall rule is
	// keyed by DisplayName.
	fwModel := FirewallRuleModel{
		Enabled: types.BoolValue(true),
		Name:    types.StringValue(fwName),
		Profile: types.StringValue("Any"),
	}
	fwObj, fwDiags := types.ObjectValueFrom(ctx, firewallRuleAttrTypes(), fwModel)
	resp.Diagnostics.Append(fwDiags...)
	if resp.Diagnostics.HasError() {
		return
	}

	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), compositeID)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("nat_name"), natName)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("protocol"), proto)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("external_ip"), externalIP)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("external_port"), externalPort)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("firewall_rule"), fwObj)...)
}

// derivedFirewallRuleName matches the script-side derivation: when the
// user doesn't set firewall_rule.name, default to a stable identifier
// keyed on the lookup tuple's protocol + external_port. Mirrors the
// pattern azurerm_lb_nat_rule uses for its rule name default.
func derivedFirewallRuleName(protocol string, externalPort int64) string {
	return fmt.Sprintf("hyperv-pf-%s-%d", strings.ToLower(protocol), externalPort)
}

// unpackFirewallRule extracts the nested FirewallRuleModel from the
// parent Model's types.Object. Null/unknown objects produce a model
// with all-null sub-attrs; callers should fall back to defaults when
// needed.
func unpackFirewallRule(ctx context.Context, obj types.Object) (FirewallRuleModel, diag.Diagnostics) {
	var diags diag.Diagnostics
	var fw FirewallRuleModel
	if obj.IsNull() || obj.IsUnknown() {
		return fw, diags
	}
	diags.Append(obj.As(ctx, &fw, basetypes.ObjectAsOptions{})...)
	return fw, diags
}

// buildNewInput translates a Create plan into the wire-level
// NewNatStaticMappingInput. Returns the resolved firewall rule name as a
// secondary value because Create needs it both for the wire payload
// and for the post-Create state reconciliation.
func buildNewInput(ctx context.Context, plan Model) (hyperv.NewNatStaticMappingInput, string, diag.Diagnostics) {
	var diags diag.Diagnostics

	fw, fwDiags := unpackFirewallRule(ctx, plan.FirewallRule)
	diags.Append(fwDiags...)
	if diags.HasError() {
		return hyperv.NewNatStaticMappingInput{}, "", diags
	}

	// Apply runtime defaults for any sub-attribute that's still
	// null/unknown after the schema-level defaults pass. firewall.name
	// is the one the framework can't statically default (it's derived
	// from protocol + external_port); enabled and profile have static
	// defaults via the schema's Default plan modifier.
	enabled := true
	if !fw.Enabled.IsNull() && !fw.Enabled.IsUnknown() {
		enabled = fw.Enabled.ValueBool()
	}
	profile := "Any"
	if !fw.Profile.IsNull() && !fw.Profile.IsUnknown() {
		profile = fw.Profile.ValueString()
	}
	name := fw.Name.ValueString()
	if fw.Name.IsNull() || fw.Name.IsUnknown() || name == "" {
		name = derivedFirewallRuleName(plan.Protocol.ValueString(), plan.ExternalPort.ValueInt64())
	}

	in := hyperv.NewNatStaticMappingInput{
		NatName:           plan.NatName.ValueString(),
		Protocol:          plan.Protocol.ValueString(),
		ExternalIPAddress: plan.ExternalIP.ValueString(),
		ExternalPort:      int(plan.ExternalPort.ValueInt64()),
		InternalIPAddress: plan.InternalIP.ValueString(),
		InternalPort:      int(plan.InternalPort.ValueInt64()),
		Firewall: hyperv.NatStaticMappingFirewallInput{
			Enabled: enabled,
			Name:    name,
			Profile: profile,
		},
	}
	return in, name, diags
}

// buildSetInput translates an Update plan + state into a
// SetNatStaticMappingInput. The lookup tuple is sourced from state (every
// attribute in it is RequiresReplace, so plan and state should match);
// the mutable attributes come from plan.
func buildSetInput(ctx context.Context, plan, state Model) (hyperv.SetNatStaticMappingInput, string, diag.Diagnostics) {
	var diags diag.Diagnostics

	fw, fwDiags := unpackFirewallRule(ctx, plan.FirewallRule)
	diags.Append(fwDiags...)
	if diags.HasError() {
		return hyperv.SetNatStaticMappingInput{}, "", diags
	}

	// firewall.name is RequiresReplace, so it's stable across Update.
	// Source from state so a planmodifier-induced unknown in plan
	// doesn't accidentally null-out the name -- the framework's
	// UseStateForUnknown should catch this, but defending here keeps
	// the contract explicit.
	stateFw, stateFwDiags := unpackFirewallRule(ctx, state.FirewallRule)
	diags.Append(stateFwDiags...)
	if diags.HasError() {
		return hyperv.SetNatStaticMappingInput{}, "", diags
	}
	name := stateFw.Name.ValueString()
	if name == "" {
		name = derivedFirewallRuleName(state.Protocol.ValueString(), state.ExternalPort.ValueInt64())
	}

	enabled := true
	if !fw.Enabled.IsNull() && !fw.Enabled.IsUnknown() {
		enabled = fw.Enabled.ValueBool()
	}
	profile := "Any"
	if !fw.Profile.IsNull() && !fw.Profile.IsUnknown() {
		profile = fw.Profile.ValueString()
	}

	in := hyperv.SetNatStaticMappingInput{
		NatName:           state.NatName.ValueString(),
		Protocol:          state.Protocol.ValueString(),
		ExternalIPAddress: state.ExternalIP.ValueString(),
		ExternalPort:      int(state.ExternalPort.ValueInt64()),
		InternalIPAddress: plan.InternalIP.ValueString(),
		InternalPort:      int(plan.InternalPort.ValueInt64()),
		Firewall: hyperv.NatStaticMappingFirewallInput{
			Enabled: enabled,
			Name:    name,
			Profile: profile,
		},
	}
	return in, name, diags
}

// modelFromNatStaticMapping hydrates a Model from a typed NatStaticMapping DTO.
// fwName is supplied by the caller because the script's read path
// reports FirewallRuleName from the input (echoed back through the
// projection, not from the host's Get-NetFirewallRule), and the
// resource layer is the source of truth for the rule's display name.
func modelFromNatStaticMapping(ctx context.Context, pf *hyperv.NatStaticMapping, fwName string) (Model, diag.Diagnostics) {
	var diags diag.Diagnostics

	// Protocol is uppercase on the wire (Get-NetNatStaticMapping native
	// shape) but lowercase in schema (the user's `protocol` config).
	// Lowercase here so state matches plan.
	proto := strings.ToLower(pf.Protocol)

	fwModel := FirewallRuleModel{
		Enabled: types.BoolValue(pf.FirewallRulePresent),
		Name:    types.StringValue(fwName),
		Profile: types.StringValue(coalesceProfile(pf.FirewallRuleProfile)),
	}
	fwObj, objDiags := types.ObjectValueFrom(ctx, firewallRuleAttrTypes(), fwModel)
	diags.Append(objDiags...)

	return Model{
		ID:            types.StringValue(pf.ID),
		NatName:       types.StringValue(pf.NatName),
		Protocol:      types.StringValue(proto),
		AddressFamily: types.StringValue("ipv4"),
		ExternalIP:    types.StringValue(pf.ExternalIPAddress),
		ExternalPort:  types.Int64Value(int64(pf.ExternalPort)),
		InternalIP:    types.StringValue(pf.InternalIPAddress),
		InternalPort:  types.Int64Value(int64(pf.InternalPort)),
		FirewallRule:  fwObj,
	}, diags
}

// coalesceProfile maps the host's reported firewall profile back into
// the schema's accepted enum. Get-NetFirewallRule reports an empty
// string when the rule is missing; map that to "Any" so state always
// holds a valid enum value.
func coalesceProfile(hostProfile string) string {
	if hostProfile == "" {
		return "Any"
	}
	return hostProfile
}
