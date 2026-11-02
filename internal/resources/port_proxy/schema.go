package port_proxy

import (
	"github.com/hashicorp/terraform-plugin-framework-validators/int64validator"
	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/booldefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/int64planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/objectplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringdefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
)

// resourceSchema returns the hyperv_port_proxy schema. The listen pair identifies the rule and
// forces replacement; the connect pair and firewall_rule.{enabled, profile} update in place.
func resourceSchema() schema.Schema {
	return schema.Schema{
		MarkdownDescription: "**Requirements:** tested with the built-in `Administrator` account on Windows " +
			"Server 2022 (build 10.0.20348); a less privileged identity hasn't been tested. The IP Helper " +
			"service (`iphlpsvc`) must be running, because it owns every portproxy listener.\n\n" +
			"Manages one [`netsh interface portproxy`](https://learn.microsoft.com/en-us/windows-server/networking/technologies/netsh/netsh-interface-portproxy) " +
			"`v4tov4` rule, which forwards a TCP port on the host to an IPv4 address and port, plus an " +
			"optional inbound firewall allow rule. It needs no `NetNat` instance, so it can forward " +
			"to any address the host can reach. It forwards TCP only. For a guest behind a " +
			"`NetNat`, typically on a `hyperv_virtual_switch` with `switch_type = \"NAT\"`, or for " +
			"UDP, use `hyperv_nat_static_mapping` instead.\n\n" +
			"Create checks the port against `netsh interface ipv4 show excludedportrange protocol=tcp` " +
			"and then confirms the IP Helper service is listening, because `netsh` accepts a rule for " +
			"an excluded port without reporting an error. If either check fails, Create removes the rule.\n\n" +
			"A DHCP guest can come back with a different address after a host reboot. Set " +
			"`connect_address` from the address the guest reports, for example " +
			"`hyperv_vm.<x>.network_adapter[0].ip_addresses[0]`, and the next apply retargets the rule " +
			"in place.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "Identifier `<listen_address>:<listen_port>`, the key netsh stores the rule under.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"listen_address": schema.StringAttribute{
				Optional: true,
				Computed: true,
				MarkdownDescription: "Host IPv4 address to listen on. Defaults to `0.0.0.0`, every address. " +
					"Changing this forces a new resource.",
				Default: stringdefault.StaticString("0.0.0.0"),
				Validators: []validator.String{
					ipv4Validator{},
				},
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"listen_port": schema.Int64Attribute{
				Required: true,
				MarkdownDescription: "Host TCP port to listen on, 1 to 65535. It must lie outside every Windows " +
					"TCP exclusion range. Changing this forces a new resource.",
				Validators: []validator.Int64{
					int64validator.Between(1, 65535),
				},
				PlanModifiers: []planmodifier.Int64{
					int64planmodifier.RequiresReplace(),
				},
			},
			"connect_address": schema.StringAttribute{
				Required:            true,
				MarkdownDescription: "IPv4 address to forward to, typically a guest's address. Updates in place.",
				Validators: []validator.String{
					ipv4Validator{},
				},
			},
			"connect_port": schema.Int64Attribute{
				Required:            true,
				MarkdownDescription: "TCP port to forward to, 1 to 65535. Updates in place.",
				Validators: []validator.Int64{
					int64validator.Between(1, 65535),
				},
			},
			"firewall_rule": schema.SingleNestedAttribute{
				Optional: true,
				Computed: true,
				MarkdownDescription: "Inbound firewall allow rule for `listen_port`. Defaults to " +
					"`{ enabled = true, profile = \"Any\" }`, with `name` derived as " +
					"`hyperv-pp-<listen_address>-<listen_port>`.",
				PlanModifiers: []planmodifier.Object{
					objectplanmodifier.UseStateForUnknown(),
				},
				Attributes: map[string]schema.Attribute{
					"enabled": schema.BoolAttribute{
						Optional: true,
						Computed: true,
						MarkdownDescription: "Whether to manage a `NetFirewallRule` for the listen port. Defaults " +
							"to `true`. With `false`, the port stays blocked unless another rule opens it.",
						Default: booldefault.StaticBool(true),
					},
					"name": schema.StringAttribute{
						Optional: true,
						Computed: true,
						MarkdownDescription: "Firewall rule `DisplayName`. Defaults to " +
							"`hyperv-pp-<listen_address>-<listen_port>`. Changing this forces a new resource.",
						PlanModifiers: []planmodifier.String{
							stringplanmodifier.RequiresReplace(),
							stringplanmodifier.UseStateForUnknown(),
						},
					},
					"profile": schema.StringAttribute{
						Optional: true,
						Computed: true,
						MarkdownDescription: "Firewall profile: `Any`, `Domain`, `Private`, or `Public`. " +
							"Defaults to `Any`.",
						Default: stringdefault.StaticString("Any"),
						Validators: []validator.String{
							stringvalidator.OneOf("Any", "Domain", "Private", "Public"),
						},
					},
				},
			},
		},
	}
}
