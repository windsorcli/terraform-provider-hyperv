package vswitch_test

// Acceptance test for the hyperv_virtual_switch data source. Creates a
// NAT switch via the resource and reads it back through the data source
// twice -- once with nat_name set (joined read; switch_type=NAT) and
// once without (bare read; switch_type=Internal, nat_* null). Pins the
// contract that surfaced from the PR review: without nat_name, the
// data source silently reports a NAT-typed switch as Internal, which
// would mis-route any downstream HCL branching on switch_type.
//
// Topology-independent: NAT switches don't bind a host NIC, so this
// test runs against any HYPERV_BACKEND target without bench-specific
// configuration.

import (
	"context"
	"fmt"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/knownvalue"
	"github.com/hashicorp/terraform-plugin-testing/statecheck"
	"github.com/hashicorp/terraform-plugin-testing/tfjsonpath"

	"github.com/xeitu/terraform-provider-hyperv/internal/acctest"
	"github.com/xeitu/terraform-provider-hyperv/internal/hyperv"
)

// TestAcc_DataVirtualSwitch_NATAugmentedRead pairs a NAT switch resource
// with a data source query that joins NetNat + NetIPAddress. Step 1
// asserts the joined read populates switch_type=NAT plus nat_*; step 2
// asserts the bare read (no nat_name input) returns Internal with the
// nat_* fields null -- the silent mis-reporting the reviewer flagged.
func TestAcc_DataVirtualSwitch_NATAugmentedRead(t *testing.T) {
	name := acctest.RandomName("vswitch-data-nat")
	natName := acctest.RandomName("nat-data")
	client := acctest.NewClient(t)

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { acctest.PreCheck(t) },
		ProtoV6ProviderFactories: acctest.ProtoV6ProviderFactories,
		CheckDestroy: acctest.CheckResourceGone("hyperv_virtual_switch",
			func(ctx context.Context, switchName string) (*hyperv.VMSwitch, error) {
				return client.GetVMSwitch(ctx, switchName, natName)
			}),
		Steps: []resource.TestStep{
			{
				// nat_name supplied: the data source takes the joined
				// path and reports the NAT-augmented view.
				Config: vswitchDataNATConfig(name, natName, true),
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue(
						"data.hyperv_virtual_switch.read",
						tfjsonpath.New("switch_type"),
						knownvalue.StringExact("NAT"),
					),
					statecheck.ExpectKnownValue(
						"data.hyperv_virtual_switch.read",
						tfjsonpath.New("nat_name"),
						knownvalue.StringExact(natName),
					),
					statecheck.ExpectKnownValue(
						"data.hyperv_virtual_switch.read",
						tfjsonpath.New("nat_internal_address_prefix"),
						knownvalue.StringExact("192.168.222.0/24"),
					),
					statecheck.ExpectKnownValue(
						"data.hyperv_virtual_switch.read",
						tfjsonpath.New("nat_host_address"),
						knownvalue.StringExact("192.168.222.1"),
					),
				},
			},
			{
				// nat_name omitted: the data source falls back to the
				// bare VMSwitch read. NAT switches surface as their
				// underlying Internal type with nat_* fields null --
				// callers branching on switch_type silently miss them.
				Config: vswitchDataNATConfig(name, natName, false),
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue(
						"data.hyperv_virtual_switch.read",
						tfjsonpath.New("switch_type"),
						knownvalue.StringExact("Internal"),
					),
					statecheck.ExpectKnownValue(
						"data.hyperv_virtual_switch.read",
						tfjsonpath.New("nat_internal_address_prefix"),
						knownvalue.Null(),
					),
					statecheck.ExpectKnownValue(
						"data.hyperv_virtual_switch.read",
						tfjsonpath.New("nat_host_address"),
						knownvalue.Null(),
					),
				},
			},
		},
	})
}

// vswitchDataNATConfig renders a NAT-switch resource paired with a
// data source query. When passNatName is true, the data source's
// nat_name input is wired up; otherwise it's omitted, exercising the
// "bare read" fallback path.
//
// The internal prefix is .222.0/24 (not /100/24 used in the resource
// acc test) so a stale orphan from an earlier run can't accidentally
// shadow this one's NetNat at the singleton check.
func vswitchDataNATConfig(name, natName string, passNatName bool) string {
	natNameAttr := ""
	if passNatName {
		natNameAttr = fmt.Sprintf("  nat_name = %q\n", natName)
	}
	return fmt.Sprintf(`
resource "hyperv_virtual_switch" "test" {
  name                        = %q
  switch_type                 = "NAT"
  nat_name                    = %q
  nat_internal_address_prefix = "192.168.222.0/24"
  nat_host_address            = "192.168.222.1"
}

data "hyperv_virtual_switch" "read" {
  name = hyperv_virtual_switch.test.name
%s}
`, name, natName, natNameAttr)
}
