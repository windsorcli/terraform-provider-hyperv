package vm_test

import (
	"fmt"
	"regexp"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"

	"github.com/windsorcli/terraform-provider-hyperv/internal/acctest"
)

// TestValidate_DvdDriveAndBootOrderDrivenByVariable pins why
// Model.DvdDrives and Model.BootOrder must stay types.List rather than
// []Struct: a nil slice can represent null but not unknown, and
// unknown is exactly what the framework marshals when these values
// are driven from an unresolved parent variable. A regression back to
// []Struct surfaces here as a Value Conversion Error during terraform
// validate, which resource.UnitTest exercises via the plan-only step
// below. UnitTest, not Test, runs this without TF_ACC; the only
// dependency is the Terraform CLI on PATH.
func TestValidate_DvdDriveAndBootOrderDrivenByVariable(t *testing.T) {
	t.Parallel()

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: acctest.ProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				// Empty default map plans no resources; the framework still validates the schema against the typed variable.
				Config: `
variable "vms" {
  type = map(object({
    dvd_iso_path = optional(string)
    boot_first   = optional(string, "hard_disk_drive")
  }))
  default = {}
}

resource "hyperv_vm" "vms" {
  for_each   = var.vms
  name       = each.key
  generation = 2
  cpu        = { count = 1 }
  memory     = { startup_bytes = 1073741824 }

  hard_disk_drive = []
  network_adapter = []

  dvd_drive = each.value.dvd_iso_path == null ? null : [
    {
      iso_path            = each.value.dvd_iso_path
      controller_number   = 0
      controller_location = 1
    }
  ]

  boot_order = each.value.dvd_iso_path == null ? null : [
    {
      type                = each.value.boot_first
      controller_number   = 0
      controller_location = 0
    }
  ]
}
`,
				PlanOnly: true,
			},
		},
	})
}

// TestValidate_HardDiskDriveAndNetworkAdapterDrivenByVariable is the
// hard_disk_drive/network_adapter analog of
// TestValidate_DvdDriveAndBootOrderDrivenByVariable.
func TestValidate_HardDiskDriveAndNetworkAdapterDrivenByVariable(t *testing.T) {
	t.Parallel()

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: acctest.ProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: `
variable "vms" {
  type = map(object({
    disk_path   = optional(string)
    switch_name = optional(string)
  }))
  default = {}
}

resource "hyperv_vm" "vms" {
  for_each   = var.vms
  name       = each.key
  generation = 2
  cpu        = { count = 1 }
  memory     = { startup_bytes = 1073741824 }

  dvd_drive  = []
  boot_order = []

  hard_disk_drive = each.value.disk_path == null ? null : [
    {
      path                = each.value.disk_path
      controller_number   = 0
      controller_location = 0
    }
  ]

  network_adapter = each.value.switch_name == null ? null : [
    {
      name        = "primary"
      switch_name = each.value.switch_name
    }
  ]
}
`,
				PlanOnly: true,
			},
		},
	})
}

// TestValidate_WaitForGuestIPTimeoutRequiresRunning pins the plan-time
// rejection of a guest-IP wait on a VM that Create leaves Off, through
// Terraform's own validate pass.
func TestValidate_WaitForGuestIPTimeoutRequiresRunning(t *testing.T) {
	t.Parallel()

	cfg := func(stateBlock string) string {
		return fmt.Sprintf(`
resource "hyperv_vm" "test" {
  name       = "wait-validate"
  generation = 2
  cpu        = { count = 1 }
  memory     = { startup_bytes = 1073741824 }

  wait_for_guest_ip_timeout = "5m"
  %s
}
`, stateBlock)
	}
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: acctest.ProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config:      cfg(""),
				PlanOnly:    true,
				ExpectError: regexp.MustCompile(`requires state.desired = "Running"`),
			},
			{
				Config:      cfg(`state = { desired = "Off" }`),
				PlanOnly:    true,
				ExpectError: regexp.MustCompile(`requires state.desired = "Running"`),
			},
		},
	})
}

// A NIC can carry an access VLAN or a VLAN isolation ID, not both.
func TestValidate_NetworkAdapterIsolationIDConflictsWithVlanID(t *testing.T) {
	t.Parallel()

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: acctest.ProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: `
resource "hyperv_vm" "test" {
  name       = "vm01"
  generation = 2
  cpu        = { count = 1 }
  memory     = { startup_bytes = 1073741824 }

  network_adapter = [
    {
      name         = "primary"
      switch_name  = "ics"
      vlan_id      = 10
      isolation_id = 2
    }
  ]
}
`,
				PlanOnly:    true,
				ExpectError: regexp.MustCompile(`Invalid Attribute Combination`),
			},
		},
	})
}
