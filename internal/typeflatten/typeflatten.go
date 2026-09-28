// Package typeflatten holds small helpers that translate typed
// hyperv-client DTOs into terraform-plugin-framework types.List /
// types.Object values. It lives in its own package so both the vm
// resource and the vm_state data source can share one implementation
// without either depending on the other's package. Functions here
// stay free of resource-layer concerns (no plan-modifier knowledge,
// no schema awareness).
package typeflatten

import (
	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/windsorcli/terraform-provider-hyperv/internal/hyperv"
)

// IPAddresses unions the per-NIC IPAddresses arrays from
// Get-VMNetworkAdapter into a single flat types.List of strings,
// preserving cmdlet order rather than re-sorting so downstream
// consumers keying off `ip_addresses[0]` still see real drift. Returns
// a known empty list, not null, since the schema's ListAttribute
// decode requires a known value.
func IPAddresses(nics []hyperv.NetworkAdapter) types.List {
	var ips []attr.Value
	for _, n := range nics {
		for _, ip := range n.IPAddresses {
			ips = append(ips, types.StringValue(ip))
		}
	}
	return types.ListValueMust(types.StringType, ips)
}
