// Package port_proxy implements the hyperv_port_proxy resource, one netsh portproxy v4tov4
// rule plus an optional inbound firewall rule, through the typed hyperv.Client.
package port_proxy

import (
	"github.com/hashicorp/terraform-plugin-framework/types"
)

// Model is the tfsdk-bound resource state; tags match schema.go.
type Model struct {
	ID             types.String `tfsdk:"id"`
	ListenAddress  types.String `tfsdk:"listen_address"`
	ListenPort     types.Int64  `tfsdk:"listen_port"`
	ConnectAddress types.String `tfsdk:"connect_address"`
	ConnectPort    types.Int64  `tfsdk:"connect_port"`
	FirewallRule   types.Object `tfsdk:"firewall_rule"`
	Listening      types.Bool   `tfsdk:"listening"`
}
