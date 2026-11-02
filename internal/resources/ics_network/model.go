// Package ics_network implements the hyperv_ics_network resource, one HCN network of type ICS
// that gives guests DHCP, DNS, and outbound NAT, through the typed hyperv.Client.
package ics_network

import (
	"github.com/hashicorp/terraform-plugin-framework/types"
)

// Model is the tfsdk-bound resource state; tags match schema.go.
type Model struct {
	ID            types.String `tfsdk:"id"`
	Name          types.String `tfsdk:"name"`
	AddressPrefix types.String `tfsdk:"address_prefix"`
	SwitchName    types.String `tfsdk:"switch_name"`
	IsolationID   types.Int64  `tfsdk:"isolation_id"`
	HostAddress   types.String `tfsdk:"host_address"`
}
