// Package provider implements the Hyper-V Terraform provider.
//
// At this point only the Provider type itself is defined. Schema attributes,
// the Configure body, resources, and data sources land in subsequent commits
// per docs/PLAN.md §12 M1.
package provider

import (
	"context"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/provider"
	"github.com/hashicorp/terraform-plugin-framework/provider/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource"
)

var _ provider.Provider = (*HypervProvider)(nil)

// HypervProvider is the root provider type. Each terraform plan/apply gets
// its own instance via the closure returned by New.
type HypervProvider struct {
	version string
}

// New returns a provider factory suitable for providerserver.Serve.
func New(version string) func() provider.Provider {
	return func() provider.Provider {
		return &HypervProvider{version: version}
	}
}

func (p *HypervProvider) Metadata(_ context.Context, _ provider.MetadataRequest, resp *provider.MetadataResponse) {
	resp.TypeName = "hyperv"
	resp.Version = p.version
}

func (p *HypervProvider) Schema(_ context.Context, _ provider.SchemaRequest, resp *provider.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "The Hyper-V provider manages the lifecycle of Hyper-V virtual machines, " +
			"virtual switches, virtual disks, and related resources. It supports three execution backends " +
			"(`local`, `ssh`, `winrm`) so the provider binary itself runs on Linux/macOS/Windows even " +
			"though it manages Windows hosts.",
		Attributes: map[string]schema.Attribute{},
	}
}

func (p *HypervProvider) Configure(_ context.Context, _ provider.ConfigureRequest, _ *provider.ConfigureResponse) {
}

func (p *HypervProvider) Resources(_ context.Context) []func() resource.Resource {
	return nil
}

func (p *HypervProvider) DataSources(_ context.Context) []func() datasource.DataSource {
	return nil
}
