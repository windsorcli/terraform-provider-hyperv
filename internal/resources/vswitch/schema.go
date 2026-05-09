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
// when `task generate` runs tfplugindocs (see PLAN.md S15).
func resourceSchema() schema.Schema {
	return schema.Schema{
		MarkdownDescription: "Manages a Hyper-V virtual switch (External, Internal, or Private). " +
			"Wraps the `New-VMSwitch` / `Set-VMSwitch` / `Remove-VMSwitch` cmdlets via a typed " +
			"JSON contract.\n\n" +
			"**Recovery from partial-create failure:** if `New-VMSwitch` succeeds on the host but the " +
			"provider fails to capture the result (e.g., transient stdout decode error), the switch will " +
			"exist on the host with no Terraform state. Subsequent `terraform apply` will fail with " +
			"`switch already exists`. Recover with " +
			"`terraform import hyperv_virtual_switch.<name> <switch-name>` and re-plan.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "Resource identifier. Mirrors `name` -- Hyper-V switch names are unique per host.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"name": schema.StringAttribute{
				Required:            true,
				MarkdownDescription: "Switch name. Must be unique on the host. **Forces replacement** -- Hyper-V doesn't support renaming a switch in place.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"switch_type": schema.StringAttribute{
				Required: true,
				MarkdownDescription: "Switch type. One of `External` (binds to a host NIC), `Internal` " +
					"(host-VM only), `Private` (VM-VM only), or `NAT` (Internal switch with a registered " +
					"`NetNat` instance providing outbound NAT). **Forces replacement** -- Hyper-V cannot " +
					"convert a switch from one type to another. NAT requires `nat_name` and " +
					"`nat_internal_address_prefix`; the provider enforces Microsoft's one-`NetNat`-per-host " +
					"constraint at create time.",
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
				MarkdownDescription: "Whether the host OS can use the bound NIC alongside VMs. Defaults to `true` on `External` " +
					"and `Internal` switches. **Not valid for `Private` switches** -- a config validator rejects this " +
					"combination at plan time.",
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
				MarkdownDescription: "NAT instance name. **Required** when `switch_type = \"NAT\"`; rejected " +
					"otherwise. Doubles as the resource-side identifier consumers reference. **Forces " +
					"replacement** -- `New-NetNat -Name` is immutable.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"nat_internal_address_prefix": schema.StringAttribute{
				Optional: true,
				Computed: true,
				MarkdownDescription: "Internal subnet (CIDR) the NAT instance routes for, e.g. " +
					"`192.168.100.0/24`. **Required** when `switch_type = \"NAT\"`; rejected otherwise. " +
					"Mutable in place via `Set-NetNat -InternalIPInterfaceAddressPrefix`.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"nat_host_address": schema.StringAttribute{
				Optional: true,
				Computed: true,
				MarkdownDescription: "Host-side gateway IPv4 address assigned to the host vNIC (`vEthernet " +
					"(<switch_name>)`). Must lie inside `nat_internal_address_prefix`. **Required** when " +
					"`switch_type = \"NAT\"`; rejected otherwise. **Forces replacement** -- changing the " +
					"host vNIC's IP requires tearing the NAT triple down.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"force_management_os_migration": schema.BoolAttribute{
				Optional: true,
				MarkdownDescription: "Allow destroy of an `External` switch with `allow_management_os = true` " +
					"when the SSH connection's source IP lies inside the switch's vNIC subnet. Defaults to " +
					"`false`: a pre-flight precondition refuses to destroy in that configuration because the " +
					"host can be left LAN-unreachable if the SSH session drops mid-migration. Set `true` only " +
					"when you have console / IPMI fallback or are operating from a different management path.",
			},
		},
	}
}
