// Package host implements the hyperv_host data source — read-only information
// about the Hyper-V host.
package host

import (
	"context"
	"errors"
	"fmt"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-log/tflog"

	"github.com/windsorcli/terraform-provider-hyperv/internal/hyperv"
)

var (
	_ datasource.DataSource              = (*DataSource)(nil)
	_ datasource.DataSourceWithConfigure = (*DataSource)(nil)
)

// DataSource implements data.hyperv_host.
type DataSource struct {
	client *hyperv.Client
}

// New is the framework factory.
func New() datasource.DataSource { return &DataSource{} }

func (d *DataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_host"
}

func (d *DataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "**Requirements:** Membership in the **Hyper-V Administrators** group on " +
			"the target host (or read access to `Get-VMHost` via a JEA endpoint).\n\n" +
			"Information about the Hyper-V host. Useful in `for_each` patterns and for " +
			"capability detection (e.g. branching on `logical_processor_count` or `memory_capacity_bytes`).",
		Attributes: map[string]schema.Attribute{
			"computer_name": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "Hostname of the Hyper-V host (from `Get-VMHost.ComputerName`).",
			},
			"logical_processor_count": schema.Int64Attribute{
				Computed:            true,
				MarkdownDescription: "Number of logical processors visible to Hyper-V.",
			},
			"memory_capacity_bytes": schema.Int64Attribute{
				Computed:            true,
				MarkdownDescription: "Total host memory in bytes.",
			},
			"virtual_machine_path": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "Default path Hyper-V stores VM configuration files in.",
			},
			"virtual_hard_disk_path": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "Default path Hyper-V stores virtual hard disks in.",
			},
		},
	}
}

func (d *DataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	if req.ProviderData == nil {
		return
	}
	client, ok := req.ProviderData.(*hyperv.Client)
	if !ok {
		resp.Diagnostics.AddError(
			"Unexpected provider data type",
			fmt.Sprintf("data.hyperv_host expected *hyperv.Client, got %T", req.ProviderData),
		)
		return
	}
	d.client = client
}

// Model is the tfsdk-bound state struct.
type Model struct {
	ComputerName          types.String `tfsdk:"computer_name"`
	LogicalProcessorCount types.Int64  `tfsdk:"logical_processor_count"`
	MemoryCapacityBytes   types.Int64  `tfsdk:"memory_capacity_bytes"`
	VirtualMachinePath    types.String `tfsdk:"virtual_machine_path"`
	VirtualHardDiskPath   types.String `tfsdk:"virtual_hard_disk_path"`
}

func (d *DataSource) Read(ctx context.Context, _ datasource.ReadRequest, resp *datasource.ReadResponse) {
	if d.client == nil {
		resp.Diagnostics.AddError(
			"Hyper-V provider not configured",
			"Read was invoked before the provider stashed a client. Usually means a "+
				"required provider attribute was unknown at plan time and Configure returned early. "+
				"Re-run once the dependency resolves.",
		)
		return
	}
	tflog.Debug(ctx, "reading hyperv_host", nil)
	state, diags := readHost(ctx, d.client)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

// readHost is the framework-detached core: easy to unit-test against a
// hyperv.Client backed by a fakeRunner. Maps Get-VMHost errors to
// host-specific diagnostics — on this singleton ErrNotFound means the
// Hyper-V role isn't installed (cmdlet unrecognized), and ErrUnavailable
// means the role is installed but vmms is stopped or otherwise unreachable.
func readHost(ctx context.Context, c *hyperv.Client) (Model, diag.Diagnostics) {
	var diags diag.Diagnostics

	h, err := c.GetVMHost(ctx)
	if err != nil {
		if errors.Is(err, hyperv.ErrNotFound) {
			diags.AddError(
				"Hyper-V role is not installed",
				fmt.Sprintf("Get-VMHost was not recognized on this host. Install the "+
					"Hyper-V role and try again. Underlying error: %s", err),
			)
			return Model{}, diags
		}
		if errors.Is(err, hyperv.ErrUnavailable) {
			diags.AddError(
				"Hyper-V management service is unavailable",
				fmt.Sprintf("Get-VMHost reached the host but Hyper-V is not responding. "+
					"Confirm the Virtual Machine Management service (vmms) is running and "+
					"the host is not in a fenced cluster state. Underlying error: %s", err),
			)
			return Model{}, diags
		}
		diags.AddError("Hyper-V host read failed", err.Error())
		return Model{}, diags
	}
	return Model{
		ComputerName:          types.StringValue(h.ComputerName),
		LogicalProcessorCount: types.Int64Value(h.LogicalProcessorCount),
		MemoryCapacityBytes:   types.Int64Value(h.MemoryCapacity),
		VirtualMachinePath:    types.StringValue(h.VirtualMachinePath),
		VirtualHardDiskPath:   types.StringValue(h.VirtualHardDiskPath),
	}, diags
}
