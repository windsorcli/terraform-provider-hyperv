package vm_state_test

// Acceptance test for the hyperv_vm_state data source. Creates a VM via
// the resource and reads it back via the data source in the same plan,
// asserting the data source surfaces the live power state. Pairs
// directly with the resource's TestAcc_VM_withState test, but from
// the read-only side: the resource manages transitions, the data
// source reports them.
//
// No bench prep required beyond the standard HYPERV_* env vars; the
// VM is created with Hyper-V's default storage path and torn down by
// CheckDestroy.

import (
	"fmt"
	"regexp"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/knownvalue"
	"github.com/hashicorp/terraform-plugin-testing/statecheck"
	"github.com/hashicorp/terraform-plugin-testing/tfjsonpath"

	"github.com/windsorcli/terraform-provider-hyperv/internal/acctest"
)

// vmStateMinMemoryBytes mirrors vmMinimumMemoryBytes in the vm
// package's acc tests: the smallest VM memory size that comfortably
// satisfies Hyper-V Server's StartupBytes minimum.
const vmStateMinMemoryBytes = 256 * 1024 * 1024

// TestAcc_DataVMState_TracksResource pins the data source's read
// against a live VM: create with state.desired = "Off" (current =
// "Off", ip_addresses = []), then flip to "Running" and confirm
// current refreshes on the next plan. ip_addresses stays empty
// throughout, since the acc-test fixtures never reach the
// integration-services handshake; the round-trip is what's pinned.
func TestAcc_DataVMState_TracksResource(t *testing.T) {
	name := acctest.RandomName("vm-state-data")
	client := acctest.NewClient(t)

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { acctest.PreCheck(t) },
		ProtoV6ProviderFactories: acctest.ProtoV6ProviderFactories,
		CheckDestroy:             acctest.CheckResourceGone("hyperv_vm", client.GetVM),
		Steps: []resource.TestStep{
			{
				Config: vmStateDataConfig(name, "Off"),
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue(
						"data.hyperv_vm_state.read",
						tfjsonpath.New("name"),
						knownvalue.StringExact(name),
					),
					statecheck.ExpectKnownValue(
						"data.hyperv_vm_state.read",
						tfjsonpath.New("id"),
						knownvalue.StringExact(name),
					),
					statecheck.ExpectKnownValue(
						"data.hyperv_vm_state.read",
						tfjsonpath.New("current"),
						knownvalue.StringExact("Off"),
					),
					statecheck.ExpectKnownValue(
						"data.hyperv_vm_state.read",
						tfjsonpath.New("ip_addresses"),
						knownvalue.ListSizeExact(0),
					),
				},
			},
			{
				Config: vmStateDataConfig(name, "Running"),
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue(
						"data.hyperv_vm_state.read",
						tfjsonpath.New("current"),
						knownvalue.StringExact("Running"),
					),
				},
			},
		},
	})
}

// TestAcc_DataVMState_NotFound exercises the missing-VM diagnostic
// path against the bench. Pins the attribute-anchored "not found"
// diagnostic the unit test already covers, end-to-end through the
// real cmdlet path -- catches any regression where the typed-error
// mapping breaks against actual cmdlet output (vs the canned
// JSON envelope the fakeRunner emits).
func TestAcc_DataVMState_NotFound(t *testing.T) {
	// RandomName-prefixed so a real bench VM can't shadow the literal and mask the regression as "expected error, got none".
	missingName := acctest.RandomName("no-such-vm")

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { acctest.PreCheck(t) },
		ProtoV6ProviderFactories: acctest.ProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: fmt.Sprintf(`
data "hyperv_vm_state" "missing" {
  name = %q
}
`, missingName),
				ExpectError: regexp.MustCompile(`Hyper-V virtual machine not found`),
			},
		},
	})
}

// vmStateDataConfig renders a hyperv_vm + data.hyperv_vm_state pair.
// The data source explicitly depends on the resource via the chained
// `name` reference so terraform-plugin-testing applies the resource
// before reading the data source on each step.
func vmStateDataConfig(vmName, desired string) string {
	return fmt.Sprintf(`
resource "hyperv_vm" "test" {
  name       = %q
  generation = 2
  cpu    = { count = 2 }
  memory = { startup_bytes = %d }
  state = {
    desired = %q
  }
}

data "hyperv_vm_state" "read" {
  name = hyperv_vm.test.name
}
`, vmName, vmStateMinMemoryBytes, desired)
}
