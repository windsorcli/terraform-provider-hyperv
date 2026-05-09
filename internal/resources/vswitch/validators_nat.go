package vswitch

import (
	"context"
	"fmt"
	"net"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
)

// natRequiresNatAttrsValidator: when switch_type = "NAT", the three NAT
// inputs (nat_name, nat_internal_address_prefix, nat_host_address) must
// be set. Rejecting at plan time gives a clear attribute-anchored
// diagnostic instead of the script's "missing parameter" surface.
type natRequiresNatAttrsValidator struct{}

func (v natRequiresNatAttrsValidator) Description(_ context.Context) string {
	return "switch_type = \"NAT\" requires nat_name, nat_internal_address_prefix, and nat_host_address"
}

func (v natRequiresNatAttrsValidator) MarkdownDescription(ctx context.Context) string {
	return v.Description(ctx)
}

func (v natRequiresNatAttrsValidator) ValidateResource(ctx context.Context, req resource.ValidateConfigRequest, resp *resource.ValidateConfigResponse) {
	var data Model
	resp.Diagnostics.Append(req.Config.Get(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if data.SwitchType.IsUnknown() {
		return
	}
	if data.SwitchType.ValueString() != "NAT" {
		return
	}
	if data.NatName.IsNull() {
		resp.Diagnostics.AddAttributeError(
			path.Root("nat_name"),
			"nat_name required for NAT switch",
			"NAT switches must declare a nat_name. Set nat_name to a non-empty string "+
				"or change switch_type.",
		)
	}
	if data.NatInternalAddressPrefix.IsNull() {
		resp.Diagnostics.AddAttributeError(
			path.Root("nat_internal_address_prefix"),
			"nat_internal_address_prefix required for NAT switch",
			"NAT switches must declare an internal address prefix in CIDR notation "+
				"(for example, \"192.168.100.0/24\").",
		)
	}
	if data.NatHostAddress.IsNull() {
		resp.Diagnostics.AddAttributeError(
			path.Root("nat_host_address"),
			"nat_host_address required for NAT switch",
			"NAT switches must declare nat_host_address (the host vNIC IPv4 inside "+
				"the prefix). The provider does not yet auto-derive cidrhost(prefix, 1).",
		)
	}
}

// natRejectsNonNatAttrsValidator: NAT switch_type rejects net_adapter_names
// and allow_management_os. Same defense-in-depth as the Private case: the
// script-layer guard still throws, but plan-time rejection is the better
// UX.
type natRejectsNonNatAttrsValidator struct{}

func (v natRejectsNonNatAttrsValidator) Description(_ context.Context) string {
	return "switch_type = \"NAT\" rejects net_adapter_names and allow_management_os"
}

func (v natRejectsNonNatAttrsValidator) MarkdownDescription(ctx context.Context) string {
	return v.Description(ctx)
}

func (v natRejectsNonNatAttrsValidator) ValidateResource(ctx context.Context, req resource.ValidateConfigRequest, resp *resource.ValidateConfigResponse) {
	var data Model
	resp.Diagnostics.Append(req.Config.Get(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if data.SwitchType.IsUnknown() {
		return
	}
	if data.SwitchType.ValueString() != "NAT" {
		return
	}
	if !data.NetAdapterNames.IsNull() && !data.NetAdapterNames.IsUnknown() {
		resp.Diagnostics.AddAttributeError(
			path.Root("net_adapter_names"),
			"net_adapter_names not valid for NAT switch",
			"NAT switches don't bind to a host NIC -- they expose VMs through Internal "+
				"+ NetNat. Remove net_adapter_names or change switch_type.",
		)
	}
	if !data.AllowManagementOS.IsNull() && !data.AllowManagementOS.IsUnknown() {
		resp.Diagnostics.AddAttributeError(
			path.Root("allow_management_os"),
			"allow_management_os not valid for NAT switch",
			"NAT switches always have a host vNIC (that's what NetNat plumbs through); "+
				"there's nothing to toggle. Remove the attribute or change switch_type.",
		)
	}
}

// natAttrsRejectedOnNonNatValidator: NAT-only inputs (nat_name,
// nat_internal_address_prefix, nat_host_address) must not be set when
// switch_type is something other than NAT. Catches misconfiguration
// where users sprinkle NAT attrs onto an Internal switch hoping for
// auto-promotion -- the resource doesn't do that promotion.
type natAttrsRejectedOnNonNatValidator struct{}

func (v natAttrsRejectedOnNonNatValidator) Description(_ context.Context) string {
	return "nat_name / nat_internal_address_prefix / nat_host_address require switch_type = \"NAT\""
}

func (v natAttrsRejectedOnNonNatValidator) MarkdownDescription(ctx context.Context) string {
	return v.Description(ctx)
}

func (v natAttrsRejectedOnNonNatValidator) ValidateResource(ctx context.Context, req resource.ValidateConfigRequest, resp *resource.ValidateConfigResponse) {
	var data Model
	resp.Diagnostics.Append(req.Config.Get(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if data.SwitchType.IsUnknown() {
		return
	}
	if data.SwitchType.ValueString() == "NAT" {
		return
	}
	rejectAttr := func(field path.Path, label string, set bool) {
		if !set {
			return
		}
		resp.Diagnostics.AddAttributeError(
			field,
			fmt.Sprintf("%s requires switch_type = \"NAT\"", label),
			fmt.Sprintf("%s is only meaningful for NAT switches. Remove the attribute or "+
				"change switch_type to \"NAT\".", label),
		)
	}
	rejectAttr(path.Root("nat_name"), "nat_name",
		!data.NatName.IsNull() && !data.NatName.IsUnknown())
	rejectAttr(path.Root("nat_internal_address_prefix"), "nat_internal_address_prefix",
		!data.NatInternalAddressPrefix.IsNull() && !data.NatInternalAddressPrefix.IsUnknown())
	rejectAttr(path.Root("nat_host_address"), "nat_host_address",
		!data.NatHostAddress.IsNull() && !data.NatHostAddress.IsUnknown())
}

// natPrefixIssue identifies which CIDR-shape rule a candidate
// nat_internal_address_prefix failed. natPrefixOK means the prefix is
// usable as-is.
type natPrefixIssue int

const (
	natPrefixOK natPrefixIssue = iota
	natPrefixIssueParse
	natPrefixIssueNotIPv4
	natPrefixIssueBadLength
	natPrefixIssueHostBits
)

// natPrefixCheckResult carries the contextual bits the validator needs
// to format per-issue diagnostics. Fields are populated only for the
// issue they describe; zero values otherwise.
type natPrefixCheckResult struct {
	Issue     natPrefixIssue
	ParseErr  error  // populated when Issue == natPrefixIssueParse
	PrefixLen int    // populated when Issue == natPrefixIssueBadLength
	Canonical string // populated when Issue == natPrefixIssueHostBits (the ipnet.String() form)
}

// checkNATPrefix runs the CIDR-shape rules for nat_internal_address_prefix
// in order and returns the first one that fails (or natPrefixOK).
//
// Rules:
//  1. Parseable as CIDR.
//  2. IPv4 family (NAT switches currently support IPv4 only).
//  3. Prefix length 1..30 (shorter leaves no usable hosts; /31 and /32
//     are degenerate for a NAT subnet).
//  4. Canonical network-address form: net.ParseCIDR accepts host-bit
//     forms like "192.168.100.1/24"; Windows New-NetNat rejects them.
//
// Pure function so unit tests pin each rule without constructing a
// tfsdk.Config to drive the validator end-to-end.
func checkNATPrefix(prefix string) natPrefixCheckResult {
	ip, ipnet, err := net.ParseCIDR(prefix)
	if err != nil {
		return natPrefixCheckResult{Issue: natPrefixIssueParse, ParseErr: err}
	}
	if ip.To4() == nil {
		return natPrefixCheckResult{Issue: natPrefixIssueNotIPv4}
	}
	ones, _ := ipnet.Mask.Size()
	if ones < 1 || ones > 30 {
		return natPrefixCheckResult{Issue: natPrefixIssueBadLength, PrefixLen: ones}
	}
	if !ip.Equal(ipnet.IP) {
		return natPrefixCheckResult{Issue: natPrefixIssueHostBits, Canonical: ipnet.String()}
	}
	return natPrefixCheckResult{Issue: natPrefixOK}
}

// natPrefixCIDRValidator: nat_internal_address_prefix must be a valid
// IPv4 CIDR with prefix length <= 30 (smaller prefixes leave no usable
// host addresses) AND must be in canonical network-address form (host
// bits zeroed). Windows New-NetNat -InternalIPInterfaceAddressPrefix
// rejects host-bit forms like "192.168.100.1/24" with an opaque cmdlet
// error; rejecting here gives the operator a plan-time diagnostic that
// suggests the canonical equivalent.
//
// The actual rule logic lives in checkNATPrefix; this validator is a
// thin wrapper that maps each issue to a path-anchored diagnostic.
type natPrefixCIDRValidator struct{}

func (v natPrefixCIDRValidator) Description(_ context.Context) string {
	return "nat_internal_address_prefix must be a canonical IPv4 CIDR (prefix length 1..30, host bits zeroed)"
}

func (v natPrefixCIDRValidator) MarkdownDescription(ctx context.Context) string {
	return v.Description(ctx)
}

func (v natPrefixCIDRValidator) ValidateResource(ctx context.Context, req resource.ValidateConfigRequest, resp *resource.ValidateConfigResponse) {
	var data Model
	resp.Diagnostics.Append(req.Config.Get(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if data.NatInternalAddressPrefix.IsNull() || data.NatInternalAddressPrefix.IsUnknown() {
		return
	}
	prefix := data.NatInternalAddressPrefix.ValueString()
	result := checkNATPrefix(prefix)
	attr := path.Root("nat_internal_address_prefix")
	switch result.Issue {
	case natPrefixOK:
		return
	case natPrefixIssueParse:
		resp.Diagnostics.AddAttributeError(attr,
			"nat_internal_address_prefix is not a valid CIDR",
			fmt.Sprintf("Could not parse %q as a CIDR: %s. Use a form like \"192.168.100.0/24\".",
				prefix, result.ParseErr),
		)
	case natPrefixIssueNotIPv4:
		resp.Diagnostics.AddAttributeError(attr,
			"nat_internal_address_prefix must be IPv4",
			fmt.Sprintf("%q parses as a non-IPv4 address. NAT switches currently support IPv4 only.", prefix),
		)
	case natPrefixIssueBadLength:
		resp.Diagnostics.AddAttributeError(attr,
			"nat_internal_address_prefix has an unusable prefix length",
			fmt.Sprintf("Prefix length /%d leaves too few host addresses for a NAT subnet. "+
				"Use /30 or longer (e.g. /24).", result.PrefixLen),
		)
	case natPrefixIssueHostBits:
		resp.Diagnostics.AddAttributeError(attr,
			"nat_internal_address_prefix must be in canonical network-address form",
			fmt.Sprintf("%q has non-zero host bits. Windows NetNat rejects this form. "+
				"Use the masked network address: %q.", prefix, result.Canonical),
		)
	}
}

// natHostAddressInPrefixValidator: nat_host_address must be a valid IPv4
// address that falls inside nat_internal_address_prefix. Plan-time
// rejection beats Hyper-V's opaque "address not in subnet" cmdlet error.
type natHostAddressInPrefixValidator struct{}

func (v natHostAddressInPrefixValidator) Description(_ context.Context) string {
	return "nat_host_address must be IPv4 and lie inside nat_internal_address_prefix"
}

func (v natHostAddressInPrefixValidator) MarkdownDescription(ctx context.Context) string {
	return v.Description(ctx)
}

func (v natHostAddressInPrefixValidator) ValidateResource(ctx context.Context, req resource.ValidateConfigRequest, resp *resource.ValidateConfigResponse) {
	var data Model
	resp.Diagnostics.Append(req.Config.Get(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if data.NatHostAddress.IsNull() || data.NatHostAddress.IsUnknown() {
		return
	}
	hostAddr := data.NatHostAddress.ValueString()
	ip := net.ParseIP(hostAddr)
	if ip == nil || ip.To4() == nil {
		resp.Diagnostics.AddAttributeError(
			path.Root("nat_host_address"),
			"nat_host_address is not a valid IPv4 address",
			fmt.Sprintf("Could not parse %q as IPv4. Use dotted-quad form like \"192.168.100.1\".", hostAddr),
		)
		return
	}
	// Skip the in-prefix check if the prefix is missing/unknown -- the
	// other validator already surfaces a diagnostic in that case.
	if data.NatInternalAddressPrefix.IsNull() || data.NatInternalAddressPrefix.IsUnknown() {
		return
	}
	prefix := strings.TrimSpace(data.NatInternalAddressPrefix.ValueString())
	_, ipnet, err := net.ParseCIDR(prefix)
	if err != nil {
		// natPrefixCIDRValidator surfaces the prefix-shape diagnostic.
		return
	}
	if !ipnet.Contains(ip) {
		resp.Diagnostics.AddAttributeError(
			path.Root("nat_host_address"),
			"nat_host_address must lie inside nat_internal_address_prefix",
			fmt.Sprintf("%q is not inside %q. Choose a host address within the prefix "+
				"(typically the first usable address, e.g. cidrhost(prefix, 1)).", hostAddr, prefix),
		)
	}
}
