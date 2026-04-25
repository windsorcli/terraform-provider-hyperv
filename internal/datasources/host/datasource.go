// Package host implements the hyperv_host data source — read-only information
// about the Hyper-V host (computer name, logical processor count, memory,
// default VM and VHD paths). Useful in for_each patterns and for capability
// detection. See docs/PLAN.md §7 catalog.
//
// This is the first consumer of the connection abstraction (PLAN §4) and
// the simplest end-to-end demonstration of the contract:
//
//	provider.Configure → newConnection (PLAN §6) → localBackend / sshBackend / winrmBackend
//	  ↓
//	data.hyperv_host.Read → embeds preamble.ps1 → connection.RunScript → Get-VMHost → JSON → typed model
//
// In a future PR, the inline JSON unmarshal here moves into a typed
// hyperv.Client.GetVMHost(ctx) method (PLAN §3 layout).
package host

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-log/tflog"

	"github.com/windsorcli/terraform-provider-hyperv/internal/connection"
	"github.com/windsorcli/terraform-provider-hyperv/internal/scripts"
)

var (
	_ datasource.DataSource              = (*DataSource)(nil)
	_ datasource.DataSourceWithConfigure = (*DataSource)(nil)
)

// DataSource implements data.hyperv_host.
type DataSource struct {
	runner connection.Runner
}

// New is the framework factory.
func New() datasource.DataSource { return &DataSource{} }

func (d *DataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_host"
}

func (d *DataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Information about the Hyper-V host. Useful in `for_each` patterns and for " +
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
	// Validation passes call Configure with nil ProviderData; missing this
	// guard panics. (PLAN §11 anti-pattern checklist.)
	if req.ProviderData == nil {
		return
	}
	conn, ok := req.ProviderData.(connection.Connection)
	if !ok {
		resp.Diagnostics.AddError(
			"Unexpected provider data type",
			fmt.Sprintf("data.hyperv_host expected connection.Connection, got %T", req.ProviderData),
		)
		return
	}
	d.runner = conn
}

// Model is the tfsdk-bound state struct.
type Model struct {
	ComputerName          types.String `tfsdk:"computer_name"`
	LogicalProcessorCount types.Int64  `tfsdk:"logical_processor_count"`
	MemoryCapacityBytes   types.Int64  `tfsdk:"memory_capacity_bytes"`
	VirtualMachinePath    types.String `tfsdk:"virtual_machine_path"`
	VirtualHardDiskPath   types.String `tfsdk:"virtual_hard_disk_path"`
}

// vmHostJSON mirrors the subset of Get-VMHost output we read. Fields use Go
// types not pointers because Get-VMHost always returns these values for a
// healthy host (per spike #2's characterization).
type vmHostJSON struct {
	ComputerName          string `json:"ComputerName"`
	LogicalProcessorCount int64  `json:"LogicalProcessorCount"`
	MemoryCapacity        int64  `json:"MemoryCapacity"`
	VirtualMachinePath    string `json:"VirtualMachinePath"`
	VirtualHardDiskPath   string `json:"VirtualHardDiskPath"`
}

func (d *DataSource) Read(ctx context.Context, _ datasource.ReadRequest, resp *datasource.ReadResponse) {
	tflog.Debug(ctx, "reading hyperv_host", nil)
	state, diags := readHost(ctx, d.runner)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

// readHost executes Get-VMHost via the Runner and parses the result. Split
// out from Read so every branch (happy / non-zero exit / transport error /
// bad JSON) is unit-testable with a fake Runner — Read itself becomes a
// thin glue layer to the framework state.
//
// In a future PR the inline body moves into internal/scripts/host/get.ps1
// and a typed hyperv.Client.GetVMHost wraps this whole function.
func readHost(ctx context.Context, runner connection.Runner) (Model, diag.Diagnostics) {
	var diags diag.Diagnostics

	preamble, err := scripts.Preamble()
	if err != nil {
		diags.AddError("Embedded preamble read failed", err.Error())
		return Model{}, diags
	}

	body := string(preamble) + "\n" +
		`Get-VMHost | Select-Object ComputerName,LogicalProcessorCount,MemoryCapacity,VirtualMachinePath,VirtualHardDiskPath | Write-HypervResult`

	res, err := runner.RunScript(ctx, body, nil)
	if err != nil {
		diags.AddError(
			"Hyper-V host read failed",
			fmt.Sprintf("Transport error from connection layer: %s", err),
		)
		return Model{}, diags
	}
	if res.ExitCode != 0 {
		diags.AddError(
			"Hyper-V host read failed",
			fmt.Sprintf("Get-VMHost exited %d. Stderr: %s", res.ExitCode, string(res.Stderr)),
		)
		return Model{}, diags
	}
	if len(bytes.TrimSpace(res.Stdout)) == 0 {
		diags.AddError(
			"Hyper-V host read failed",
			"Get-VMHost returned exit 0 but empty stdout. This usually means the preamble's "+
				"$ProgressPreference / encoding pin didn't apply — check that `Write-HypervResult` "+
				"reached stdout.",
		)
		return Model{}, diags
	}

	var h vmHostJSON
	if err := json.Unmarshal(res.Stdout, &h); err != nil {
		diags.AddError(
			"Hyper-V host JSON parse failed",
			fmt.Sprintf("Could not unmarshal Get-VMHost output: %s\nStdout: %s", err, string(res.Stdout)),
		)
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
