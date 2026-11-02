package ics_network

import (
	"context"
	"errors"
	"fmt"
	"regexp"

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

var guidPattern = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

// Resource implements hyperv_ics_network.
type Resource struct {
	client *hyperv.Client
}

// New is the framework factory.
func New() resource.Resource { return &Resource{} }

// Metadata sets the resource's TF type name.
func (r *Resource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_ics_network"
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
			fmt.Sprintf("hyperv_ics_network expected *hyperv.Client, got %T", req.ProviderData),
		)
		return
	}
	r.client = client
}

// Create runs new.ps1 and writes the read result back to state.
func (r *Resource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	if r.client == nil {
		resp.Diagnostics.AddError("provider not configured",
			"hyperv_ics_network Create called before Configure stashed a client.")
		return
	}

	var plan Model
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	in, err := buildNewInput(plan)
	if err != nil {
		resp.Diagnostics.AddAttributeError(path.Root("address_prefix"), "Invalid address_prefix", err.Error())
		return
	}

	tflog.Debug(ctx, "creating hyperv_ics_network", map[string]any{
		"name":           in.Name,
		"address_prefix": in.AddressPrefix,
	})
	net, err := r.client.NewICSNetwork(ctx, in)
	if err != nil {
		resp.Diagnostics.AddError("Create hyperv_ics_network failed", err.Error())
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, modelFromICSNetwork(net))...)
}

// Read runs get.ps1 and removes the resource from state when the network is gone.
func (r *Resource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	if r.client == nil {
		resp.Diagnostics.AddError("provider not configured",
			"hyperv_ics_network Read called before Configure stashed a client.")
		return
	}

	var state Model
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	net, err := r.client.GetICSNetwork(ctx, state.ID.ValueString())
	if err != nil {
		if errors.Is(err, hyperv.ErrNotFound) {
			tflog.Info(ctx, "hyperv_ics_network not found; removing from state", map[string]any{
				"id": state.ID.ValueString(),
			})
			resp.State.RemoveResource(ctx)
			return
		}
		resp.Diagnostics.AddError("Read hyperv_ics_network failed", err.Error())
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, modelFromICSNetwork(net))...)
}

// Update is unreachable, since every input forces replacement; it echoes the plan to satisfy the interface.
func (r *Resource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan Model
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

// Delete runs remove.ps1; a network that is already gone is success.
func (r *Resource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	if r.client == nil {
		resp.Diagnostics.AddError("provider not configured",
			"hyperv_ics_network Delete called before Configure stashed a client.")
		return
	}

	var state Model
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	tflog.Debug(ctx, "deleting hyperv_ics_network", map[string]any{"id": state.ID.ValueString()})
	if err := r.client.RemoveICSNetwork(ctx, state.ID.ValueString()); err != nil {
		resp.Diagnostics.AddError("Delete hyperv_ics_network failed", err.Error())
	}
}

// ImportState accepts the HCN network GUID.
func (r *Resource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	if !guidPattern.MatchString(req.ID) {
		resp.Diagnostics.AddError(
			"Invalid import ID for hyperv_ics_network",
			fmt.Sprintf("Expected the HCN network GUID, like `753900d5-83fd-4c4d-83b6-7c438c6ff4fe`; got %q.", req.ID),
		)
		return
	}
	resource.ImportStatePassthroughID(ctx, path.Root("id"), req, resp)
}

// buildNewInput translates a Create plan into new.ps1's input, deriving the host address from the prefix.
func buildNewInput(plan Model) (hyperv.NewICSNetworkInput, error) {
	host, err := firstHost(plan.AddressPrefix.ValueString())
	if err != nil {
		return hyperv.NewICSNetworkInput{}, err
	}
	return hyperv.NewICSNetworkInput{
		Name:          plan.Name.ValueString(),
		AddressPrefix: plan.AddressPrefix.ValueString(),
		HostAddress:   host,
	}, nil
}

// modelFromICSNetwork hydrates a Model from the read result.
func modelFromICSNetwork(net *hyperv.ICSNetwork) *Model {
	return &Model{
		ID:            types.StringValue(net.ID),
		Name:          types.StringValue(net.Name),
		AddressPrefix: types.StringValue(net.AddressPrefix),
		SwitchName:    types.StringValue(net.SwitchName),
		IsolationID:   types.Int64Value(int64(net.IsolationID)),
		HostAddress:   types.StringValue(net.HostAddress),
	}
}
