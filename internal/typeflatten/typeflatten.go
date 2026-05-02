// Package typeflatten holds small helpers that translate the typed
// hyperv-client DTO shape into terraform-plugin-framework types.List /
// types.Object values. Lives in its own package so both the vm resource
// (internal/resources/vm) and the vm_state data source
// (internal/datasources/vm_state) can share a single implementation
// without one taking a dependency on the other's package.
//
// Keep functions in here narrow and free of resource-layer concerns
// (no plan-modifier knowledge, no schema awareness). They take typed
// hyperv DTOs and return framework types -- nothing else.
package typeflatten

import (
	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/windsorcli/terraform-provider-hyperv/internal/hyperv"
)

// IPAddresses unions the per-NIC IPAddresses arrays from
// Get-VMNetworkAdapter into a single flat types.List of strings.
// Order is preserved: NICs in cmdlet order, IPs in cmdlet order
// within each NIC. Hyper-V reports a stable per-boot order; we
// don't re-sort because that would mask drift for downstream
// consumers that key off `ip_addresses[0]`.
//
// Returns a known empty list (not null) when no IPs are present.
// The schema layer's ListAttribute decode requires a known value,
// and an empty Off-VM is the steady state for most acc-test fixtures.
func IPAddresses(nics []hyperv.NetworkAdapter) types.List {
	var ips []attr.Value
	for _, n := range nics {
		for _, ip := range n.IPAddresses {
			ips = append(ips, types.StringValue(ip))
		}
	}
	return types.ListValueMust(types.StringType, ips)
}
