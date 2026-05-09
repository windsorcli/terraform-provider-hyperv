// Package vswitch implements the hyperv_virtual_switch data source --
// read-only access to an existing switch by name. Mirrors the resource's
// read shape but writes no state.
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

// Schema declares the data source's read shape: name is the lookup key
// (Required); everything else is read-only Computed pulled from Get-VMSwitch.
//
// net_adapter_names is intentionally absent -- the resource preserves it
// as user intent, but Get-VMSwitch doesn't return the originally-supplied
// adapter-name list (only NetAdapterInterfaceDescription, which is the
// teamed adapter's friendly description). Exposing only what the cmdlet
// actually returns avoids a phantom field that we can't reliably populate.
func (d *DataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Reads metadata for an existing Hyper-V virtual switch by name. Useful when " +
			"the switch was created out-of-band (Hyper-V Manager, DSC, manual `New-VMSwitch`) and " +
			"a Terraform resource needs to reference it as a dependency.",
		Attributes: map[string]schema.Attribute{
			"name": schema.StringAttribute{
				Required:            true,
				MarkdownDescription: "Switch name. The lookup key.",
			},
			"id": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "Resource identifier. Mirrors `name`.",
			},
			"switch_type": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "Switch type: `External`, `Internal`, or `Private`.",
			},
			"allow_management_os": schema.BoolAttribute{
				Computed:            true,
				MarkdownDescription: "Whether the host OS shares the bound NIC. Always `false` for `Private` switches.",
			},
			"notes": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "Free-form description stored on the switch.",
			},
			"net_adapter_interface_description": schema.StringAttribute{
				Computed: true,
				MarkdownDescription: "Hyper-V-reported description of the bound NIC (External switches only). " +
					"Empty for Internal/Private. For NIC-teamed External switches this is the team adapter's description.",
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

// Read fetches the switch via Get-VMSwitch and writes the typed shape into
// state. ErrNotFound surfaces as an attribute-anchored diagnostic so the
// operator sees which `name` value didn't resolve. ErrUnavailable surfaces
// with transient phrasing so a vmms blip during a plan doesn't read like
// "the switch is gone".
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

	tflog.Debug(ctx, "reading hyperv_virtual_switch", map[string]any{"name": config.Name.ValueString()})
	state, diags := readVSwitch(ctx, d.client, config.Name.ValueString())
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

// Model is the tfsdk-bound state struct.
type Model struct {
	Name                           types.String `tfsdk:"name"`
	ID                             types.String `tfsdk:"id"`
	SwitchType                     types.String `tfsdk:"switch_type"`
	AllowManagementOS              types.Bool   `tfsdk:"allow_management_os"`
	Notes                          types.String `tfsdk:"notes"`
	NetAdapterInterfaceDescription types.String `tfsdk:"net_adapter_interface_description"`
}

// readVSwitch is the framework-detached core: easy to unit-test against a
// hyperv.Client backed by a fakeRunner. Maps client errors to anchored
// diagnostics so test cases can assert on the user-facing message without
// constructing a full ReadRequest.
func readVSwitch(ctx context.Context, c *hyperv.Client, name string) (Model, diag.Diagnostics) {
	var diags diag.Diagnostics

	// Data source has no state, so it can't carry nat_name across reads.
	// NAT-typed switches read here as their underlying type (Internal);
	// users wanting the NAT-augmented view should reference the resource
	// directly (it carries nat_name in state for the round-trip).
	sw, err := c.GetVMSwitch(ctx, name, "")
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

	return Model{
		Name:                           types.StringValue(sw.Name),
		ID:                             types.StringValue(sw.Name),
		SwitchType:                     types.StringValue(sw.SwitchType),
		AllowManagementOS:              types.BoolValue(sw.AllowManagementOS),
		Notes:                          types.StringValue(sw.Notes),
		NetAdapterInterfaceDescription: types.StringValue(sw.NetAdapterInterfaceDescription),
	}, diags
}
