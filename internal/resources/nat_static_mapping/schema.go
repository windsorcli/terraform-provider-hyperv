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
// Mutability summary:
//
//	nat_name / protocol / external_ip / external_port  -> RequiresReplace
//	  (lookup tuple; NatStaticMapping has no rename)
//	internal_ip / internal_port                        -> in-place
//	  (script Set does Remove + Add; StaticMappingID re-rolls)
//	firewall_rule.{enabled, profile}                   -> in-place
//	firewall_rule.name                                 -> RequiresReplace
//	  (rename = NetFirewallRule recreate)
//
// Description handling: deferred for v1. The mapping has no native
// description field on the host -- a registry-sidecar approach would
// be needed for it to survive Read, which we haven't implemented.
func resourceSchema() schema.Schema {
	return schema.Schema{
		MarkdownDescription: "**Requirements:** **Local Administrators** on the target host. " +
			"Empirically verified on Windows Server 2022 (build 10.0.20348): both " +
			"[`Add-NetNatStaticMapping`](https://learn.microsoft.com/en-us/powershell/module/netnat/add-netnatstaticmapping) " +
			"and [`New-NetFirewallRule`](https://learn.microsoft.com/en-us/powershell/module/netsecurity/new-netfirewallrule) " +
			"return \"Access denied\" when invoked by a user in `Hyper-V Administrators` alone. " +
			"Microsoft's cmdlet reference pages do not document a privilege requirement; the floor " +
			"here is tested rather than cited.\n\n" +
			"Manages a single static NAT port forward (TCP or UDP) plus an optional " +
			"inbound firewall allow rule. Targets an existing `NetNat` instance by name -- typically " +
			"created via `hyperv_virtual_switch` with `switch_type = \"NAT\"`, but any pre-existing " +
			"NetNat (out-of-band, Hyper-V Manager, DSC) is also accepted.\n\n" +
			"Functionally equivalent to `azurerm_lb_nat_rule` and `google_compute_forwarding_rule`: " +
			"turns the Hyper-V host into a port-forwarder for VMs on a private internal network.",
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
			// static_mapping_id is intentionally NOT exposed: Hyper-V's
			// NatStaticMapping ID is opaque, re-rolls on every
			// internal_* update (Set is Remove + Add under the hood),
			// and is never used as a foreign-key target by other
			// resources. Exposing it in state forces a "known after
			// apply" hop on every plan or trips the framework's
			// inconsistent-result guard when Update changes it. The
			// script paths (get/set/remove) do their own lookup-by-
			// tuple internally and don't need state to carry the ID.
			"nat_name": schema.StringAttribute{
				Required: true,
				MarkdownDescription: "Name of the `NetNat` instance to bind this mapping to. Must already " +
					"exist on the host -- typically `hyperv_virtual_switch.<x>.nat_name` for a NAT " +
					"switch managed by this provider, but any out-of-band NetNat is fine. **Forces " +
					"replacement** -- a different NetNat is a different mapping.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"protocol": schema.StringAttribute{
				Optional: true,
				Computed: true,
				MarkdownDescription: "Transport protocol. One of `tcp` or `udp` (case-insensitive on the " +
					"wire; canonical lowercase here). ICMP and SCTP are out of scope. Defaults to `tcp`. " +
					"**Forces replacement** -- protocol is part of the mapping's identity tuple.",
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
				MarkdownDescription: "Address family. Currently only `ipv4` is supported; the attribute is " +
					"reserved for future IPv6 support. Defaults to `ipv4`. **Forces replacement**.",
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
				MarkdownDescription: "Bench-side listen IPv4 address. Defaults to `0.0.0.0` (any). Set to " +
					"a specific host IP to scope the mapping to a single NIC. **Forces replacement**.",
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
				MarkdownDescription: "Bench-side listen port (1..65535). **Forces replacement** -- the " +
					"port is part of the mapping's identity tuple.",
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
				MarkdownDescription: "Optional inbound firewall allow rule paired with the static mapping. " +
					"Defaults to `{ enabled = true, profile = \"Any\" }` with `name` derived as " +
					"`hyperv-pf-<protocol>-<external_port>`. Set `enabled = false` to skip the firewall " +
					"call entirely (the mapping still lands; the OS firewall just won't open the " +
					"listen port).",
				PlanModifiers: []planmodifier.Object{
					objectplanmodifier.UseStateForUnknown(),
				},
				Attributes: map[string]schema.Attribute{
					"enabled": schema.BoolAttribute{
						Optional: true,
						Computed: true,
						MarkdownDescription: "Whether to manage a `NetFirewallRule` alongside the static " +
							"mapping. Defaults to `true`. Setting `false` skips firewall management " +
							"entirely -- the mapping lands but the listen port stays blocked unless " +
							"another rule already opens it.",
						Default: booldefault.StaticBool(true),
					},
					"name": schema.StringAttribute{
						Optional: true,
						Computed: true,
						MarkdownDescription: "Firewall rule `DisplayName`. Defaults to " +
							"`hyperv-pf-<protocol>-<external_port>` (computed in the resource layer; not a " +
							"static schema default). **Forces replacement** -- a `NetFirewallRule` rename " +
							"is recreate.",
						PlanModifiers: []planmodifier.String{
							stringplanmodifier.RequiresReplace(),
							stringplanmodifier.UseStateForUnknown(),
						},
					},
					"profile": schema.StringAttribute{
						Optional: true,
						Computed: true,
						MarkdownDescription: "Firewall profile. One of `Any`, `Domain`, `Private`, or " +
							"`Public` (single value only in v1; comma-joined combinations are deferred). " +
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
