package vm_test

import (
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"

	"github.com/xeitu/terraform-provider-hyperv/internal/acctest"
)

// TestValidate_DvdDriveAndBootOrderDrivenByVariable nails the
// regression that motivated switching the model's DvdDrives and
// BootOrder fields from []Struct to types.List.
//
// Before the switch, `terraform validate` against this exact config
// (Optional list-nested-attributes driven from `each.value.dvd_drive`
// of an `optional(list(object(...)))`-typed variable) failed with:
//
//	Error: Value Conversion Error
//	Path: dvd_drive
//	Target Type: []vm.DvdDriveModel
//	Suggested Type: basetypes.ListValue
//
// (and the same shape on boot_order). The framework can represent
// null with a nil slice but has no shape for unknown -- and unknown is
// what the framework marshals when a parent variable hasn't fully
// resolved (the for_each-with-empty-default pattern below). Switching
// the fields to types.List closes this gap. Same fix shape PR #70
// applied to URLConfig on hyperv_image_file.
//
// Uses resource.UnitTest rather than resource.Test so this protective
// test runs without TF_ACC -- it does not touch a Hyper-V bench. The
// only external dependency is the Terraform CLI being on PATH.
func TestValidate_DvdDriveAndBootOrderDrivenByVariable(t *testing.T) {
	t.Parallel()

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: acctest.ProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				// Empty default map means no resources are actually
				// planned -- the framework still validates the resource
				// schema against the typed variable, which is where the
				// broken shape used to fire. Both dvd_drive and
				// boot_order are wired through `optional(list(object))`
				// + null-vs-populated conditional, mirroring the
				// "compute/hyperv driving Talos VMs from a map of
				// instance specs" pattern that surfaced the bug.
				Config: `
terraform {
  required_providers {
    hyperv = {
      source = "xeitu/hyperv"
    }
  }
}

provider "hyperv" {
  skip_auth_probe = true
}

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
