package vswitch_test

// Acceptance tests for hyperv_virtual_switch. These run only when TF_ACC=1
// is set (terraform-plugin-testing's default gate); `go test ./...` without
// it skips the framework-managed bodies.
//
// The bench setup is documented in docs/contributing/acceptance-tests.md.
// At minimum HYPERV_BACKEND and the per-backend vars (HYPERV_HOST,
// HYPERV_USERNAME for ssh/winrm) must be loaded -- task test:acc reads
// .env.local so a maintainer's bench creds stay out of the repo.
//
// Why Private as the first scenario: it requires no host NIC and no
// management-OS toggle, so the test is independent of the bench's
// network topology. External-switch tests come in a follow-up that
// gates on HYPERV_TEST_NET_ADAPTER for the bench's bound NIC name.

import (
	"context"
	"fmt"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/knownvalue"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
	"github.com/hashicorp/terraform-plugin-testing/statecheck"
	"github.com/hashicorp/terraform-plugin-testing/tfjsonpath"

	"github.com/xeitu/terraform-provider-hyperv/internal/hyperv"

	"github.com/xeitu/terraform-provider-hyperv/internal/acctest"
)

// TestAcc_VirtualSwitch_basic exercises the create-read-update-import-delete
// path on a Private switch. The Step list is the canonical resource.Test
// shape: every Step is a separate `terraform plan && apply`, with the
// framework asserting on state and (where Configured) plan actions.
//
// Steps:
//  1. Create with notes = "<initial>". Verify name, switch_type, notes.
//  2. Update notes to "<updated>". Verify in-place update -- not a replace
//     (the schema marks `notes` as in-place updatable; a regression to
//     RequiresReplace would surface here).
//  3. Import the resource by name and verify state matches.
func TestAcc_VirtualSwitch_basic(t *testing.T) {
	name := acctest.RandomName("vswitch-private")
	client := acctest.NewClient(t)

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { acctest.PreCheck(t) },
		ProtoV6ProviderFactories: acctest.ProtoV6ProviderFactories,
		// CheckDestroy verifies the switch is actually gone from the
		// bench after destroy, not just absent from Terraform state.
		// Without this, a silently-failing Remove-VMSwitch would let
		// the test pass green while leaving an orphan switch behind.
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
				// Plan-action assertion: a RequiresReplace regression on
				// `notes` would silently destroy-and-recreate the switch,
				// and the state checks below would still pass against the
				// fresh resource. Pin the action to Update so a schema
				// regression fails this step explicitly.
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
					// Name is immutable (RequiresReplace); confirm it
					// survived the update unchanged. A regression that
					// flipped name to in-place updatable would trip a
					// different state-check failure.
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
				// `id` is a Computed mirror of `name`; it round-trips
				// cleanly through import without divergence.
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
// path. Distinct from the Private scenario in TestAcc_VirtualSwitch_basic
// because Internal switches go through a different New-VMSwitch
// parameter set (one that does NOT accept -AllowManagementOS) -- a
// regression that forwards AllowManagementOS to the cmdlet for Internal
// switches surfaces here as a "Parameter set cannot be resolved" error
// at apply time.
//
// Why this test didn't exist before: the original TestAcc_VirtualSwitch_basic
// used Private only, and the Pester unit tests mock New-VMSwitch so the
// parameter-set ambiguity is invisible at the script-test layer. The
// bug only surfaces against a real cmdlet on a real host.
//
// Internal switches need no host NIC binding, so the test is independent
// of bench network topology -- same property as the Private scenario.
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
// orchestrate three host-side cmdlets (New-VMSwitch + New-NetIPAddress +
// New-NetNat) and are constrained by Microsoft's one-NetNat-per-host rule
// -- both wrinkles only show up against a real bench. Topology-independent
// like the Private and Internal scenarios; no bound NIC required.
//
// Update step exercises Notes -- the only in-place mutation that reaches
// Update for a NAT switch (every NAT-specific input is RequiresReplace,
// because Set-NetNat does not accept -InternalIPInterfaceAddressPrefix
// on the bench: verified by an earlier draft of this test against Server
// 2022 + PS 5.1, which failed with "A parameter cannot be found that
// matches parameter name 'InternalIPInterfaceAddressPrefix'.").
//
// CheckDestroy passes nat_name through GetVMSwitch so the read joins
// NetNat + NetIPAddress -- a half-torn-down NAT triple would surface
// here rather than silently leaving orphan state on the host.
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
				// In-place update: notes mutation routes through
				// Set-VMSwitch on the underlying Internal switch. No
				// teardown of NetNat or NetIPAddress.
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
