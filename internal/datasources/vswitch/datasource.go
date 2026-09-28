// Package vswitch implements the hyperv_virtual_switch data source:
// read-only access to an existing switch by name. Mirrors the
// resource's read fields but writes no state.
package vswitch

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
)

var (
	_ datasource.DataSource              = (*DataSource)(nil)
	_ datasource.DataSourceWithConfigure = (*DataSource)(nil)
)

// DataSource implements data.hyperv_virtual_switch.
type DataSource struct {
	client *hyperv.Client
}

// New is the framework factory.
func New() datasource.DataSource { return &DataSource{} }

// Metadata sets the data source's TF type name.
func (d *DataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_virtual_switch"
}

// Schema declares name as the lookup key (Required); everything else
// is read-only Computed pulled from Get-VMSwitch. net_adapter_names is
// absent, since Get-VMSwitch doesn't return the originally-supplied
// adapter list, only the teamed adapter's description. nat_name is
// Optional: without it a NAT switch reads back as its underlying
// Internal VMSwitch with empty NAT fields; setting it joins Get-NetNat
// so switch_type reports "NAT" with nat_* populated.
func (d *DataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "**Requirements:** Membership in the **Hyper-V Administrators** group on " +
			"the target host (or read access to `Get-VMSwitch` via a JEA endpoint). Reading NAT-typed " +
			"switches additionally requires read access to `Get-NetNat`, which is in the host network " +
			"stack — local Administrators is the safest floor when `nat_name` is set.\n\n" +
			"Reads metadata for an existing Hyper-V virtual switch by name. Useful when " +
			"the switch was created out-of-band (Hyper-V Manager, DSC, manual `New-VMSwitch`) and " +
			"a Terraform resource needs to reference it as a dependency.\n\n" +
			"**NAT switches** require `nat_name` to read with `switch_type = \"NAT\"` and the joined " +
			"`nat_*` fields populated; without it, NAT switches return as their underlying `Internal` " +
			"type with empty NAT fields.",
		Attributes: map[string]schema.Attribute{
			"name": schema.StringAttribute{
				Required:            true,
				MarkdownDescription: "Switch name. The lookup key.",
			},
			"nat_name": schema.StringAttribute{
				Optional: true,
				MarkdownDescription: "NAT instance name. Set this when reading a NAT-typed switch so the " +
					"data source joins `Get-NetNat` and `Get-NetIPAddress` with the underlying `Get-VMSwitch` " +
					"and reports `switch_type = \"NAT\"`. Omit for non-NAT switches; passing a `nat_name` " +
					"that doesn't match an existing `NetNat` makes the read fail with `Hyper-V virtual " +
					"switch not found`.",
			},
			"id": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "Resource identifier. Mirrors `name`.",
			},
			"switch_type": schema.StringAttribute{
				Computed: true,
				MarkdownDescription: "Switch type. `External`, `Internal`, or `Private` for the underlying " +
					"VMSwitch types; `NAT` only when `nat_name` was supplied AND the corresponding NetNat " +
					"+ NetIPAddress are present on the host.",
			},
			"allow_management_os": schema.BoolAttribute{
				Computed:            true,
				MarkdownDescription: "Whether the host OS shares the bound NIC. Always `false` for `Private` and `NAT` switches.",
			},
			"notes": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "Free-form description stored on the switch.",
			},
			"net_adapter_interface_description": schema.StringAttribute{
				Computed: true,
				MarkdownDescription: "Hyper-V-reported description of the bound NIC (External switches only). " +
					"Empty for Internal/Private/NAT. For NIC-teamed External switches this is the team adapter's description.",
			},
			"nat_internal_address_prefix": schema.StringAttribute{
				Computed: true,
				MarkdownDescription: "Internal subnet (CIDR) the NAT instance routes for, e.g. " +
					"`192.168.100.0/24`. Populated only when `nat_name` was supplied and the switch is NAT-typed.",
			},
			"nat_host_address": schema.StringAttribute{
				Computed: true,
				MarkdownDescription: "Host-side gateway IPv4 assigned to the host vNIC. Populated only when " +
					"`nat_name` was supplied and the switch is NAT-typed.",
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
			fmt.Sprintf("data.hyperv_virtual_switch expected *hyperv.Client, got %T", req.ProviderData),
		)
		return
	}
	d.client = client
}

// Read fetches the switch via Get-VMSwitch and writes the typed
// result into state. ErrNotFound surfaces as an attribute-anchored
// diagnostic so the operator sees which `name` didn't resolve;
// ErrUnavailable surfaces with transient phrasing so a vmms blip
// doesn't read like "the switch is gone".
func (d *DataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	if d.client == nil {
		resp.Diagnostics.AddError(
			"Hyper-V provider not configured",
			"data.hyperv_virtual_switch was invoked before the provider stashed a client. "+
				"Usually means a required provider attribute was unknown at plan time.",
		)
		return
	}

	var config Model
	resp.Diagnostics.Append(req.Config.Get(ctx, &config)...)
	if resp.Diagnostics.HasError() {
		return
	}

	natNameInput := config.NatName.ValueString()
	tflog.Debug(ctx, "reading hyperv_virtual_switch", map[string]any{
		"name":     config.Name.ValueString(),
		"nat_name": natNameInput,
	})
	state, diags := readVSwitch(ctx, d.client, config.Name.ValueString(), natNameInput)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}
	// Echo nat_name from config back so the Optional attribute round-trips cleanly.
	state.NatName = config.NatName
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

// Model is the tfsdk-bound state struct.
type Model struct {
	Name                           types.String `tfsdk:"name"`
	NatName                        types.String `tfsdk:"nat_name"`
	ID                             types.String `tfsdk:"id"`
	SwitchType                     types.String `tfsdk:"switch_type"`
	AllowManagementOS              types.Bool   `tfsdk:"allow_management_os"`
	Notes                          types.String `tfsdk:"notes"`
	NetAdapterInterfaceDescription types.String `tfsdk:"net_adapter_interface_description"`
	NatInternalAddressPrefix       types.String `tfsdk:"nat_internal_address_prefix"`
	NatHostAddress                 types.String `tfsdk:"nat_host_address"`
}

// readVSwitch is the framework-detached core: easy to unit-test against a
// hyperv.Client backed by a fakeRunner. Maps client errors to anchored
// diagnostics so test cases can assert on the user-facing message without
// constructing a full ReadRequest.
//
// natName is the optional NAT-augmentation knob: non-empty joins
// Get-NetNat + Get-NetIPAddress with the VMSwitch read; empty returns
// the bare read, so a NAT-typed switch surfaces as "Internal" with
// empty nat_* fields.
func readVSwitch(ctx context.Context, c *hyperv.Client, name, natName string) (Model, diag.Diagnostics) {
	var diags diag.Diagnostics

	sw, err := c.GetVMSwitch(ctx, name, natName)
	if err != nil {
		switch {
		case errors.Is(err, hyperv.ErrNotFound):
			diags.AddAttributeError(
				path.Root("name"),
				"Hyper-V virtual switch not found",
				fmt.Sprintf("No switch named %q exists on the host. Underlying error: %s", name, err),
			)
		case errors.Is(err, hyperv.ErrUnavailable):
			diags.AddError(
				"Hyper-V management service is unavailable",
				fmt.Sprintf("Get-VMSwitch could reach the host but Hyper-V is not responding. "+
					"Confirm vmms is running. Underlying error: %s", err),
			)
		default:
			diags.AddError("Read hyperv_virtual_switch failed", err.Error())
		}
		return Model{}, diags
	}

	natPrefix := types.StringNull()
	if sw.NatInternalAddressPrefix != "" {
		natPrefix = types.StringValue(sw.NatInternalAddressPrefix)
	}
	natHost := types.StringNull()
	if sw.NatHostAddress != "" {
		natHost = types.StringValue(sw.NatHostAddress)
	}

	// NatName is left at its zero value; Read echoes config.NatName back into state.
	return Model{
		Name:                           types.StringValue(sw.Name),
		ID:                             types.StringValue(sw.Name),
		SwitchType:                     types.StringValue(sw.SwitchType),
		AllowManagementOS:              types.BoolValue(sw.AllowManagementOS),
		Notes:                          types.StringValue(sw.Notes),
		NetAdapterInterfaceDescription: types.StringValue(sw.NetAdapterInterfaceDescription),
		NatInternalAddressPrefix:       natPrefix,
		NatHostAddress:                 natHost,
	}, diags
}
