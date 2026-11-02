package ics_network

import (
	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/int64planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
)

// resourceSchema returns the hyperv_ics_network schema. Both inputs force replacement,
// since HCN has no call that changes an existing network's name or subnet.
func resourceSchema() schema.Schema {
	return schema.Schema{
		MarkdownDescription: "**Requirements:** the Host Network Service (`hns`) must be present. On Windows " +
			"Server, enable it with `Enable-WindowsOptionalFeature -Online -FeatureName Containers-HNS -All` " +
			"and restart the host. Tested with the built-in `Administrator` account on Windows Server 2022 " +
			"(build 10.0.20348); a less privileged identity hasn't been tested.\n\n" +
			"Manages one [Host Compute Network](https://learn.microsoft.com/en-us/virtualization/api/hcn/hns_schema) " +
			"of type `ICS`. Guests on it get a DHCP lease, DNS, and outbound NAT through the host, " +
			"and the host itself takes `host_address`, the first address in `address_prefix`. " +
			"The provider calls `computenetwork.dll` through P/Invoke. The network persists across host " +
			"reboots with the same ID, prefix, and isolation ID.\n\n" +
			"Every ICS network on a host shares one Internal `VMSwitch`. The first ICS network creates it " +
			"and names it after itself, and the switch keeps that name after that network is deleted. " +
			"Deleting the last ICS network removes the switch. HNS also assigns each network a VLAN " +
			"isolation ID, and a guest NIC gets a lease only when its isolation ID matches. Connect a VM " +
			"by setting `hyperv_vm.network_adapter[*].switch_name` to `switch_name` and " +
			"`hyperv_vm.network_adapter[*].isolation_id` to `isolation_id`.\n\n" +
			"A guest can get a different lease after a host reboot. Read its address from " +
			"`hyperv_vm.<x>.network_adapter[0].ip_addresses` rather than hard-coding it.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "HCN network GUID.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"name": schema.StringAttribute{
				Required: true,
				MarkdownDescription: "Network name, unique among the host's HCN networks. It also names the " +
					"host vNIC, `vEthernet (<name>)`. Changing this forces a new resource.",
				Validators: []validator.String{
					stringvalidator.LengthAtLeast(1),
				},
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"address_prefix": schema.StringAttribute{
				Required: true,
				MarkdownDescription: "IPv4 subnet in CIDR notation, such as `172.30.97.0/24`. The address must " +
					"be the network address and the prefix `/30` or shorter. Changing this forces a new resource.",
				Validators: []validator.String{
					ipv4PrefixValidator{},
				},
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"switch_name": schema.StringAttribute{
				Computed: true,
				MarkdownDescription: "Name of the Internal `VMSwitch` every ICS network on the host shares. " +
					"Attach guest NICs to this switch.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"isolation_id": schema.Int64Attribute{
				Computed: true,
				MarkdownDescription: "VLAN isolation ID HNS assigned to this network. A guest NIC needs the same " +
					"value in `hyperv_vm.network_adapter[*].isolation_id` to get a lease.",
				PlanModifiers: []planmodifier.Int64{
					int64planmodifier.UseStateForUnknown(),
				},
			},
			"host_address": schema.StringAttribute{
				Computed: true,
				MarkdownDescription: "Host's address on the network, the first address in `address_prefix`. " +
					"Guests use it as their gateway and DNS server.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
		},
	}
}
