package vswitch

import (
	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/boolplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/listplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

// resourceSchema returns the locked-in schema for hyperv_virtual_switch.
// MarkdownDescription on each attribute drives the Registry-published doc
// when `task generate` runs tfplugindocs.
func resourceSchema() schema.Schema {
	return schema.Schema{
		MarkdownDescription: "**Requirements:** Depend on `switch_type`, empirically verified on " +
			"Windows Server 2022 (build 10.0.20348):\n\n" +
			"- `Private`, `Internal`: **Hyper-V Administrators** is sufficient.\n" +
			"- `NAT`: **local Administrators** is required; the underlying `New-NetNat` returns " +
			"\"Access denied\" for Hyper-V Administrators alone.\n" +
			"- `External`: local Administrators is the recommended floor; binding a physical NIC " +
			"under a lower-privilege identity risks disrupting the management plane and was not " +
			"directly tested.\n\n" +
			"Over WinRM, the connecting identity also needs `Administrators` or " +
			"`Remote Management Users` membership for endpoint access; `Administrators` implies " +
			"this, but a delegated Hyper-V-Administrators-only identity does not.\n\n" +
			"Manages a Hyper-V virtual switch: `External`, `Internal`, `Private`, or `NAT`.\n\n" +
			"~> **Note:** If `New-VMSwitch` succeeds on the host but the provider fails to record " +
			"the result, for example on a transient stdout decode error, the switch exists on the " +
			"host with no Terraform state, and the next `terraform apply` fails with " +
			"\"switch already exists.\" Recover with " +
			"`terraform import hyperv_virtual_switch.<name> <switch-name>` and re-plan.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "Resource identifier, matching `name` since switch names are unique per host.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"name": schema.StringAttribute{
				Required: true,
				MarkdownDescription: "Name of the switch. Must be unique on the host. Changing this " +
					"forces a new resource, since Hyper-V does not support renaming a switch in place.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"switch_type": schema.StringAttribute{
				Required: true,
				MarkdownDescription: "Switch type: `External` (binds to a host NIC), `Internal` " +
					"(host-VM only), `Private` (VM-VM only), or `NAT` (an Internal switch with a " +
					"registered `NetNat` instance providing outbound NAT). `NAT` requires " +
					"`nat_name` and `nat_internal_address_prefix`; an existing NetNat with the same " +
					"`nat_name` is adopted, but one with a different prefix fails the create. " +
					"Changing this forces a new resource; Hyper-V cannot convert a switch from one " +
					"type to another.",
				Validators: []validator.String{
					stringvalidator.OneOf("External", "Internal", "Private", "NAT"),
				},
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"net_adapter_names": schema.ListAttribute{
				ElementType:         types.StringType,
				Optional:            true,
				Computed:            true,
				MarkdownDescription: "List of host NIC names to bind the switch to. Required when `switch_type = \"External\"`; ignored otherwise. Multiple names form a NIC team.",
				PlanModifiers: []planmodifier.List{
					listplanmodifier.UseStateForUnknown(),
				},
			},
			"allow_management_os": schema.BoolAttribute{
				Optional: true,
				Computed: true,
				MarkdownDescription: "Whether the host OS can use the bound NIC alongside VMs. " +
					"Defaults to `true` on `External` and `Internal` switches. Not valid for " +
					"`Private` switches.",
				PlanModifiers: []planmodifier.Bool{
					boolplanmodifier.UseStateForUnknown(),
				},
			},
			"notes": schema.StringAttribute{
				Optional:            true,
				Computed:            true,
				MarkdownDescription: "Free-form description stored on the switch by Hyper-V. Setting to an empty string clears it.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"net_adapter_interface_description": schema.StringAttribute{
				Computed: true,
				MarkdownDescription: "Read-only: the Hyper-V-reported description of the bound NIC (External switches only). " +
					"Empty for Internal/Private/NAT. For NIC-teamed External switches this is the team adapter's description, " +
					"not any individual member NIC's.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"nat_name": schema.StringAttribute{
				Optional: true,
				Computed: true,
				MarkdownDescription: "NAT instance name. Required when `switch_type = \"NAT\"`; " +
					"rejected otherwise. Must start with a letter or digit and otherwise contain " +
					"only letters, digits, underscores, dots, and hyphens; wildcard metacharacters " +
					"(`*`, `?`, `[`) are rejected, since `Get-NetNat -Name` would interpret them as " +
					"a pattern. Changing this forces a new resource; `New-NetNat -Name` is immutable.",
				Validators: []validator.String{
					stringvalidator.RegexMatches(
						natNameRegex,
						"must start with a letter or digit and otherwise contain only letters, digits, underscores, dots, and hyphens (no spaces or wildcard metacharacters)",
					),
				},
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"nat_internal_address_prefix": schema.StringAttribute{
				Optional: true,
				Computed: true,
				MarkdownDescription: "Internal subnet, in CIDR form, the NAT instance routes for, " +
					"for example `192.168.100.0/24`. Required when `switch_type = \"NAT\"`; " +
					"rejected otherwise. Changing this forces a new resource, since `Set-NetNat` " +
					"does not accept an updated prefix.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"nat_host_address": schema.StringAttribute{
				Optional: true,
				Computed: true,
				MarkdownDescription: "Host-side gateway IPv4 address assigned to the host vNIC " +
					"(`vEthernet (<switch_name>)`). Must lie inside `nat_internal_address_prefix`. " +
					"Required when `switch_type = \"NAT\"`; rejected otherwise. Changing this " +
					"forces a new resource.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"force_management_os_migration": schema.BoolAttribute{
				Optional: true,
				MarkdownDescription: "Acknowledges the destroy hazard for an `External` switch with " +
					"`allow_management_os = true`. Removing such a switch triggers an asynchronous " +
					"host-IP migration back to the physical NIC; if the connection to the host " +
					"traverses the switch's vNIC and drops mid-migration, the host can be left " +
					"LAN-unreachable, recoverable only through console or IPMI.\n\n" +
					"~> **Note:** The provider can't tell how Terraform is connecting to the host, " +
					"so this gate fires on every `External` destroy with `allow_management_os = " +
					"true`, regardless of connection path. Set this to `true` only once you've " +
					"confirmed console or IPMI fallback, or that the connection doesn't traverse " +
					"this switch's vNIC. Defaults to `false`. Valid only when " +
					"`switch_type = \"External\"`.",
			},
		},
	}
}
