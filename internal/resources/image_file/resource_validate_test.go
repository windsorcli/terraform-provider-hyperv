package image_file_test

import (
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"

	"github.com/xeitu/terraform-provider-hyperv/internal/acctest"
)

// TestValidate_URLDrivenByVariable nails the regression that motivated
// switching the model's URL field from *URLConfig to types.Object.
//
// Before the switch, `terraform validate` against this exact config
// (an Optional nested-attribute driven from `each.value.url` of an
// `optional(object(...))`-typed variable) failed during the framework's
// config-marshal step with:
//
//	Received unknown value, however the target type cannot handle
//	unknown values. ... Target Type: *image_file.URLConfig
//	Suggested Type: basetypes.ObjectValue
//
// The framework can represent null with a nil pointer-to-struct but
// has no shape for unknown -- and unknown is what the framework
// marshals when a parent variable hasn't fully resolved. Switching
// the field to types.Object closes this gap. This test pins that
// closure: a regression that re-introduces the pointer-to-struct
// shape would surface here as an "Error: Value Conversion Error"
// failure at the validate step (which resource.UnitTest exercises
// as part of the plan-only step below).
//
// Uses resource.UnitTest rather than resource.Test so this protective
// test runs without TF_ACC -- it does not touch a Hyper-V bench. The
// only external dependency is the Terraform CLI being on PATH.
func TestValidate_URLDrivenByVariable(t *testing.T) {
	t.Parallel()

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: acctest.ProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				// Empty default map means no resources are actually planned
				// -- the framework still validates the resource schema
				// against the typed variable, which is where the broken
				// shape used to fire.
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

variable "imgs" {
  type = map(object({
    destination_path = string
    url              = optional(object({
      url      = string
      checksum = string
    }))
  }))
  default = {}
}

resource "hyperv_image_file" "images" {
  for_each         = var.imgs
  destination_path = each.value.destination_path
  url              = each.value.url
}
`,
				PlanOnly: true,
			},
		},
	})
}
