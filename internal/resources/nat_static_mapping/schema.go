package nat_static_mapping

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

// resourceSchema returns the locked-in schema for hyperv_nat_static_mapping.
// MarkdownDescription on each attribute drives the Registry-published
// doc when `task generate` runs tfplugindocs.
//
// Mutability:
//
//	nat_name / protocol / external_ip / external_port  -> RequiresReplace
//	  (lookup tuple; NatStaticMapping has no rename)
//	internal_ip / internal_port                        -> in-place
//	  (script Set does Remove + Add; StaticMappingID re-rolls)
//	firewall_rule.{enabled, profile}                   -> in-place
//	firewall_rule.name                                 -> RequiresReplace
//	  (rename = NetFirewallRule recreate)
//
// No description attribute: the mapping has no native description
// field on the host to survive Read.
//
// lint:allow-long-comment
func resourceSchema() schema.Schema {
	return schema.Schema{
		MarkdownDescription: "**Requirements:** **Local Administrators** on the target host. Both " +
			"[`Add-NetNatStaticMapping`](https://learn.microsoft.com/en-us/powershell/module/netnat/add-netnatstaticmapping) " +
			"and [`New-NetFirewallRule`](https://learn.microsoft.com/en-us/powershell/module/netsecurity/new-netfirewallrule) " +
			"return \"Access denied\" for a user in `Hyper-V Administrators` alone, as verified on " +
			"Windows Server 2022 (build 10.0.20348); Microsoft's cmdlet reference pages don't " +
			"document a privilege requirement, so this floor is tested rather than cited.\n\n" +
			"Manages a single static NAT port forward, TCP or UDP, plus an " +
			"optional inbound firewall allow rule; functionally equivalent to `azurerm_lb_nat_rule` " +
			"or `google_compute_forwarding_rule`. Targets an existing `NetNat` instance by name, " +
			"typically created through `hyperv_virtual_switch` with `switch_type = \"NAT\"`, but " +
			"any pre-existing NetNat, created out-of-band, through Hyper-V Manager, or through " +
			"DSC, is also accepted.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Computed: true,
				MarkdownDescription: "Composite identifier: " +
					"`<nat_name>:<protocol>:<external_ip>:<external_port>`. Stable across rebinds; " +
					"importable via `terraform import`.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			// static_mapping_id isn't exposed: it re-rolls on every internal_* update and nothing needs it as a foreign key.
			"nat_name": schema.StringAttribute{
				Required: true,
				MarkdownDescription: "Name of the `NetNat` instance to bind this mapping to. Must already " +
					"exist on the host, typically `hyperv_virtual_switch.<x>.nat_name` for a NAT " +
					"switch managed by this provider, though any out-of-band NetNat works too. " +
					"Changing this forces a new resource; a different NetNat is a different mapping.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"protocol": schema.StringAttribute{
				Optional: true,
				Computed: true,
				MarkdownDescription: "Transport protocol: `tcp` or `udp`. ICMP and SCTP are not " +
					"supported. Defaults to `tcp`. Changing this forces a new resource, since " +
					"protocol is part of the mapping's identity.",
				Default: stringdefault.StaticString("tcp"),
				Validators: []validator.String{
					stringvalidator.OneOf("tcp", "udp"),
				},
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"address_family": schema.StringAttribute{
				Optional: true,
				Computed: true,
				MarkdownDescription: "Address family. Only `ipv4` is currently supported. Defaults to " +
					"`ipv4`. Changing this forces a new resource.",
				Default: stringdefault.StaticString("ipv4"),
				Validators: []validator.String{
					stringvalidator.OneOf("ipv4"),
				},
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"external_ip": schema.StringAttribute{
				Optional: true,
				Computed: true,
				MarkdownDescription: "Host-side listen IPv4 address. Defaults to `0.0.0.0` for any " +
					"address; set a specific host IP to scope the mapping to a single NIC. " +
					"Changing this forces a new resource.",
				Default: stringdefault.StaticString("0.0.0.0"),
				Validators: []validator.String{
					ipv4Validator{},
				},
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"external_port": schema.Int64Attribute{
				Required: true,
				MarkdownDescription: "Host-side listen port, 1 to 65535. Changing this forces a new " +
					"resource, since the port is part of the mapping's identity.",
				Validators: []validator.Int64{
					int64validator.Between(1, 65535),
				},
				PlanModifiers: []planmodifier.Int64{
					int64planmodifier.RequiresReplace(),
				},
			},
			"internal_ip": schema.StringAttribute{
				Required: true,
				MarkdownDescription: "Internal IPv4 address of the VM serving the forwarded port. Must be " +
					"inside the parent NetNat's `internal_address_prefix`. Mutable in place: changing " +
					"this re-rolls the static mapping (Remove + Add) but the resource ID stays stable.",
				Validators: []validator.String{
					ipv4Validator{},
				},
			},
			"internal_port": schema.Int64Attribute{
				Required: true,
				MarkdownDescription: "Internal port on the VM serving the forwarded traffic (1..65535). " +
					"Mutable in place via the same Remove + Add path as `internal_ip`.",
				Validators: []validator.Int64{
					int64validator.Between(1, 65535),
				},
			},
			"firewall_rule": schema.SingleNestedAttribute{
				Optional: true,
				Computed: true,
				MarkdownDescription: "Optional inbound firewall allow rule paired with the static " +
					"mapping. Defaults to `{ enabled = true, profile = \"Any\" }` with `name` " +
					"derived as `hyperv-pf-<protocol>-<external_port>`. Set `enabled = false` to " +
					"skip the firewall call entirely; the mapping still lands, but the OS " +
					"firewall won't open the listen port.",
				PlanModifiers: []planmodifier.Object{
					objectplanmodifier.UseStateForUnknown(),
				},
				Attributes: map[string]schema.Attribute{
					"enabled": schema.BoolAttribute{
						Optional: true,
						Computed: true,
						MarkdownDescription: "Whether to manage a `NetFirewallRule` alongside the static " +
							"mapping. Defaults to `true`. Setting `false` skips firewall management " +
							"entirely; the mapping lands, but the listen port stays blocked unless " +
							"another rule already opens it.",
						Default: booldefault.StaticBool(true),
					},
					"name": schema.StringAttribute{
						Optional: true,
						Computed: true,
						MarkdownDescription: "Firewall rule `DisplayName`. Defaults to " +
							"`hyperv-pf-<protocol>-<external_port>`. Changing this forces a new " +
							"resource; renaming a `NetFirewallRule` means recreating it.",
						PlanModifiers: []planmodifier.String{
							stringplanmodifier.RequiresReplace(),
							stringplanmodifier.UseStateForUnknown(),
						},
					},
					"profile": schema.StringAttribute{
						Optional: true,
						Computed: true,
						MarkdownDescription: "Firewall profile: `Any`, `Domain`, `Private`, or `Public`. " +
							"Only a single value is supported. Defaults to `Any`.",
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
