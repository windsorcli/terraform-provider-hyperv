package port_forward_test

// Acceptance test for hyperv_port_forward. Pairs a NAT switch
// (hyperv_virtual_switch with switch_type=NAT) with a port_forward
// resource that consumes its nat_name -- exercises the composition
// PR M6 was designed for. Topology-independent: NAT switches don't
// bind a host NIC, so the test runs against any HYPERV_BACKEND
// target without bench-specific configuration.
//
// CheckDestroy verifies the static mapping is gone via the typed
// client; an orphan would surface here rather than silently surviving
// a green test run.

import (
	"context"
	"fmt"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/knownvalue"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
	"github.com/hashicorp/terraform-plugin-testing/statecheck"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
	"github.com/hashicorp/terraform-plugin-testing/tfjsonpath"

	"github.com/windsorcli/terraform-provider-hyperv/internal/acctest"
	"github.com/windsorcli/terraform-provider-hyperv/internal/hyperv"
)

// TestAcc_PortForward_basic exercises create + in-place update of
// internal_port + destroy. Step 1 creates the mapping with the
// nested firewall_rule defaults; step 2 mutates internal_port and
// expects an Update plan (not Replace) -- pinning the in-place
// mutability of internal_*.
func TestAcc_PortForward_basic(t *testing.T) {
	switchName := acctest.RandomName("vswitch-pf")
	natName := acctest.RandomName("nat-pf")
	client := acctest.NewClient(t)

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { acctest.PreCheck(t) },
		ProtoV6ProviderFactories: acctest.ProtoV6ProviderFactories,
		// Two CheckDestroys run in sequence: the port_forward must be
		// gone first (its destroy depends on the NAT switch still
		// being there for the lookup tuple to resolve), then the
		// NAT switch.
		CheckDestroy: resource.ComposeAggregateTestCheckFunc(
			acctest.CheckResourceGone("hyperv_port_forward",
				func(ctx context.Context, _ string) (*hyperv.PortForward, error) {
					return client.GetPortForward(ctx, hyperv.GetPortForwardInput{
						NatName:           natName,
						Protocol:          "tcp",
						ExternalIPAddress: "0.0.0.0",
						ExternalPort:      8080,
						FirewallName:      "hyperv-pf-tcp-8080",
					})
				}),
			acctest.CheckResourceGone("hyperv_virtual_switch",
				func(ctx context.Context, name string) (*hyperv.VMSwitch, error) {
					return client.GetVMSwitch(ctx, name, natName)
				}),
		),
		Steps: []resource.TestStep{
			{
				Config: portForwardConfig(switchName, natName, 30080),
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue("hyperv_port_forward.test",
						tfjsonpath.New("nat_name"),
						knownvalue.StringExact(natName)),
					statecheck.ExpectKnownValue("hyperv_port_forward.test",
						tfjsonpath.New("protocol"),
						knownvalue.StringExact("tcp")),
					statecheck.ExpectKnownValue("hyperv_port_forward.test",
						tfjsonpath.New("external_port"),
						knownvalue.Int64Exact(8080)),
					statecheck.ExpectKnownValue("hyperv_port_forward.test",
						tfjsonpath.New("internal_port"),
						knownvalue.Int64Exact(30080)),
					statecheck.ExpectKnownValue("hyperv_port_forward.test",
						tfjsonpath.New("id"),
						knownvalue.StringExact(fmt.Sprintf("%s:tcp:0.0.0.0:8080", natName))),
				},
			},
			{
				// In-place update: internal_port mutates via Remove + Add.
				// StaticMappingID changes (the cmdlet re-numbers the mapping
				// on Add), but the resource ID stays stable.
				Config: portForwardConfig(switchName, natName, 30090),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{
						plancheck.ExpectResourceAction(
							"hyperv_port_forward.test",
							plancheck.ResourceActionUpdate,
						),
					},
				},
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue("hyperv_port_forward.test",
						tfjsonpath.New("internal_port"),
						knownvalue.Int64Exact(30090)),
					statecheck.ExpectKnownValue("hyperv_port_forward.test",
						tfjsonpath.New("id"),
						knownvalue.StringExact(fmt.Sprintf("%s:tcp:0.0.0.0:8080", natName))),
				},
			},
			{
				// Import round-trip with the 5-segment form (explicit
				// firewall rule name). ImportStateVerify asserts the
				// imported state EQUALS the post-Apply state, which
				// forces ImportState's seed of firewall_rule.name to
				// match what Read returned -- the previous bug (4-seg
				// only, no firewall_rule seed) would surface here as
				// a missing-attribute mismatch.
				ResourceName:      "hyperv_port_forward.test",
				ImportState:       true,
				ImportStateVerify: true,
				ImportStateIdFunc: func(s *terraform.State) (string, error) {
					rs, ok := s.RootModule().Resources["hyperv_port_forward.test"]
					if !ok {
						return "", fmt.Errorf("hyperv_port_forward.test not in state")
					}
					return fmt.Sprintf("%s:hyperv-pf-tcp-8080", rs.Primary.ID), nil
				},
			},
		},
	})
}

// portForwardConfig renders a NAT-switch resource paired with a
// port_forward that consumes its nat_name. The internal address
// (192.168.222.10) lives inside the switch's prefix (192.168.222.0/24).
// External port 8080 is non-privileged so it doesn't conflict with
// well-known services on the bench.
func portForwardConfig(switchName, natName string, internalPort int) string {
	return fmt.Sprintf(`
resource "hyperv_virtual_switch" "bench" {
  name                        = %q
  switch_type                 = "NAT"
  nat_name                    = %q
  nat_internal_address_prefix = "192.168.222.0/24"
  nat_host_address            = "192.168.222.1"
}

resource "hyperv_port_forward" "test" {
  nat_name      = hyperv_virtual_switch.bench.nat_name
  protocol      = "tcp"
  external_port = 8080
  internal_ip   = "192.168.222.10"
  internal_port = %d
}
`, switchName, natName, internalPort)
}
