// Package nat_static_mapping implements the hyperv_nat_static_mapping resource.
// Wraps the nat_static_mapping/{get,new,set,remove}.ps1 contract via the
// typed hyperv.Client. Composes with hyperv_virtual_switch's NAT type
// (or any pre-existing NetNat instance) to build a Windows-native
// reverse proxy in front of one or more VMs on a private network.
package nat_static_mapping

import (
	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

// Model is the tfsdk-bound struct backing the resource state. Field
// tags align with schema.go attribute names; conversion to/from the
// typed hyperv.NatStaticMapping DTO lives in resource.go.
//
// Lookup tuple (NatName, Protocol, ExternalIP, ExternalPort) is
// RequiresReplace at the schema layer -- it identifies the mapping
// uniquely and Hyper-V has no rename. internal_ip / internal_port and
// the firewall sub-attributes are in-place mutable.
type Model struct {
	ID            types.String `tfsdk:"id"`
	NatName       types.String `tfsdk:"nat_name"`
	Protocol      types.String `tfsdk:"protocol"`
	AddressFamily types.String `tfsdk:"address_family"`
	ExternalIP    types.String `tfsdk:"external_ip"`
	ExternalPort  types.Int64  `tfsdk:"external_port"`
	InternalIP    types.String `tfsdk:"internal_ip"`
	InternalPort  types.Int64  `tfsdk:"internal_port"`
	FirewallRule  types.Object `tfsdk:"firewall_rule"`
}

// FirewallRuleModel is the nested-attribute model. Carried through
// types.Object on the parent Model so the framework can serialize it.
// Defaults applied at the schema layer (enabled=true, profile=Any);
// name derived in resource.go from protocol + external_port when
// unset.
type FirewallRuleModel struct {
	Enabled types.Bool   `tfsdk:"enabled"`
	Name    types.String `tfsdk:"name"`
	Profile types.String `tfsdk:"profile"`
}

// firewallRuleAttrTypes returns the tftypes-level type map for
// FirewallRuleModel. Must match the schema's Attributes block exactly;
// drift here surfaces as "type mismatch" at plan time.
func firewallRuleAttrTypes() map[string]attr.Type {
	return map[string]attr.Type{
		"enabled": types.BoolType,
		"name":    types.StringType,
		"profile": types.StringType,
	}
}
