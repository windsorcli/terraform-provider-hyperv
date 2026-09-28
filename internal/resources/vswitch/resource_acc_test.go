package vswitch_test

// Acceptance tests for hyperv_virtual_switch. These run only when
// TF_ACC=1 is set; `go test ./...` without it skips the
// framework-managed bodies. The bench setup is documented in
// docs/contributing/acceptance-tests.md: at minimum HYPERV_BACKEND and
// the per-backend vars (HYPERV_HOST, HYPERV_USERNAME for ssh/winrm)
// must be loaded, typically via .env.local (task test:acc reads it).
//
// Private is the first scenario since it needs no host NIC or
// management-OS toggle, independent of the bench's network topology.

import (
	"context"
	"fmt"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/knownvalue"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
	"github.com/hashicorp/terraform-plugin-testing/statecheck"
	"github.com/hashicorp/terraform-plugin-testing/tfjsonpath"

	"github.com/windsorcli/terraform-provider-hyperv/internal/hyperv"

	"github.com/windsorcli/terraform-provider-hyperv/internal/acctest"
)

// TestAcc_VirtualSwitch_basic exercises the create-read-update-import-
// delete path on a Private switch; each Step below is a separate
// plan-and-apply, with the framework asserting on state and (where
// configured) plan actions.
//
// Steps:
//  1. Create with notes = "<initial>". Verify name, switch_type, notes.
//  2. Update notes to "<updated>". Verify in-place update, not a
//     replace (a regression to RequiresReplace would surface here).
//  3. Import the resource by name and verify state matches.
func TestAcc_VirtualSwitch_basic(t *testing.T) {
	name := acctest.RandomName("vswitch-private")
	client := acctest.NewClient(t)

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { acctest.PreCheck(t) },
		ProtoV6ProviderFactories: acctest.ProtoV6ProviderFactories,
		// Verifies the switch is gone from the bench, not just absent from state, catching a silently-failing Remove-VMSwitch.
		CheckDestroy: acctest.CheckResourceGone("hyperv_virtual_switch",
			func(ctx context.Context, name string) (*hyperv.VMSwitch, error) {
				return client.GetVMSwitch(ctx, name, "")
			}),
		Steps: []resource.TestStep{
			{
				Config: vswitchPrivateConfig(name, "initial notes"),
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue(
						"hyperv_virtual_switch.test",
						tfjsonpath.New("name"),
						knownvalue.StringExact(name),
					),
					statecheck.ExpectKnownValue(
						"hyperv_virtual_switch.test",
						tfjsonpath.New("switch_type"),
						knownvalue.StringExact("Private"),
					),
					statecheck.ExpectKnownValue(
						"hyperv_virtual_switch.test",
						tfjsonpath.New("notes"),
						knownvalue.StringExact("initial notes"),
					),
				},
			},
			{
				Config: vswitchPrivateConfig(name, "updated notes"),
				// Pins the action to Update: a RequiresReplace regression would destroy-recreate but still pass the state checks below.
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{
						plancheck.ExpectResourceAction(
							"hyperv_virtual_switch.test",
							plancheck.ResourceActionUpdate,
						),
					},
				},
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue(
						"hyperv_virtual_switch.test",
						tfjsonpath.New("notes"),
						knownvalue.StringExact("updated notes"),
					),
					// Confirms name (RequiresReplace) survived the update unchanged.
					statecheck.ExpectKnownValue(
						"hyperv_virtual_switch.test",
						tfjsonpath.New("name"),
						knownvalue.StringExact(name),
					),
				},
			},
			{
				ResourceName:      "hyperv_virtual_switch.test",
				ImportState:       true,
				ImportStateId:     name,
				ImportStateVerify: true,
				// id is a Computed mirror of name; it round-trips cleanly through import without divergence.
			},
		},
	})
}

// vswitchPrivateConfig is the smallest valid HCL for a Private switch.
// allow_management_os and net_adapter_names are intentionally omitted --
// allow_management_os is rejected for Private by a config validator,
// and net_adapter_names is External-only.
func vswitchPrivateConfig(name, notes string) string {
	return fmt.Sprintf(`
resource "hyperv_virtual_switch" "test" {
  name        = %q
  switch_type = "Private"
  notes       = %q
}
`, name, notes)
}

// TestAcc_VirtualSwitch_internal exercises the Internal-switch create
// path. Distinct from Private because Internal switches go through a
// different New-VMSwitch parameter set that doesn't accept
// -AllowManagementOS; a regression that forwards it surfaces here as
// "Parameter set cannot be resolved" at apply time. Internal switches
// need no host NIC binding, so the test is topology-independent, like
// the Private scenario.
func TestAcc_VirtualSwitch_internal(t *testing.T) {
	name := acctest.RandomName("vswitch-internal")
	client := acctest.NewClient(t)

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { acctest.PreCheck(t) },
		ProtoV6ProviderFactories: acctest.ProtoV6ProviderFactories,
		CheckDestroy: acctest.CheckResourceGone("hyperv_virtual_switch",
			func(ctx context.Context, name string) (*hyperv.VMSwitch, error) {
				return client.GetVMSwitch(ctx, name, "")
			}),
		Steps: []resource.TestStep{
			{
				Config: vswitchInternalConfig(name),
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue(
						"hyperv_virtual_switch.test",
						tfjsonpath.New("name"),
						knownvalue.StringExact(name),
					),
					statecheck.ExpectKnownValue(
						"hyperv_virtual_switch.test",
						tfjsonpath.New("switch_type"),
						knownvalue.StringExact("Internal"),
					),
				},
			},
		},
	})
}

// vswitchInternalConfig is the smallest valid HCL for an Internal switch.
// allow_management_os is intentionally omitted -- the script-layer guard
// rejects AllowManagementOS for non-External switches, and Internal
// switches always have a host vNIC implicitly anyway.
func vswitchInternalConfig(name string) string {
	return fmt.Sprintf(`
resource "hyperv_virtual_switch" "test" {
  name        = %q
  switch_type = "Internal"
}
`, name)
}

// TestAcc_VirtualSwitch_nat exercises the NAT switch_type. NAT switches
// orchestrate three host-side cmdlets (New-VMSwitch, New-NetIPAddress,
// New-NetNat) and are constrained by Microsoft's one-NetNat-per-host
// rule, both only visible against a real bench; topology-independent
// like Private and Internal. The update step exercises Notes, the only
// in-place mutation NAT reaches, since Set-NetNat doesn't accept
// -InternalIPInterfaceAddressPrefix and every other NAT input is
// RequiresReplace. CheckDestroy passes nat_name through GetVMSwitch so
// the read joins NetNat and NetIPAddress, surfacing a half-torn-down
// NAT triple here instead of leaving orphan state on the host.
func TestAcc_VirtualSwitch_nat(t *testing.T) {
	name := acctest.RandomName("vswitch-nat")
	natName := acctest.RandomName("nat")
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
				Config: vswitchNATConfig(name, natName, "192.168.100.0/24", "192.168.100.1", "initial notes"),
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue("hyperv_virtual_switch.test",
						tfjsonpath.New("switch_type"), knownvalue.StringExact("NAT")),
					statecheck.ExpectKnownValue("hyperv_virtual_switch.test",
						tfjsonpath.New("nat_name"), knownvalue.StringExact(natName)),
					statecheck.ExpectKnownValue("hyperv_virtual_switch.test",
						tfjsonpath.New("nat_internal_address_prefix"),
						knownvalue.StringExact("192.168.100.0/24")),
					statecheck.ExpectKnownValue("hyperv_virtual_switch.test",
						tfjsonpath.New("nat_host_address"),
						knownvalue.StringExact("192.168.100.1")),
					statecheck.ExpectKnownValue("hyperv_virtual_switch.test",
						tfjsonpath.New("notes"),
						knownvalue.StringExact("initial notes")),
				},
			},
			{
				// notes routes through Set-VMSwitch on the underlying Internal switch; no NetNat/NetIPAddress teardown.
				Config: vswitchNATConfig(name, natName, "192.168.100.0/24", "192.168.100.1", "updated notes"),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{
						plancheck.ExpectResourceAction(
							"hyperv_virtual_switch.test",
							plancheck.ResourceActionUpdate,
						),
					},
				},
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue("hyperv_virtual_switch.test",
						tfjsonpath.New("notes"),
						knownvalue.StringExact("updated notes")),
					statecheck.ExpectKnownValue("hyperv_virtual_switch.test",
						tfjsonpath.New("switch_type"),
						knownvalue.StringExact("NAT")),
				},
			},
		},
	})
}

// vswitchNATConfig is the canonical NAT-switch HCL fixture used by the
// acceptance test. Notably absent: net_adapter_names and
// allow_management_os (rejected for NAT by the resource validators).
func vswitchNATConfig(name, natName, prefix, hostAddr, notes string) string {
	return fmt.Sprintf(`
resource "hyperv_virtual_switch" "test" {
  name                        = %q
  switch_type                 = "NAT"
  nat_name                    = %q
  nat_internal_address_prefix = %q
  nat_host_address            = %q
  notes                       = %q
}
`, name, natName, prefix, hostAddr, notes)
}
