package image_file_test

import (
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"

	"github.com/windsorcli/terraform-provider-hyperv/internal/acctest"
)

// TestValidate_URLDrivenByVariable pins why Model.URL must stay
// types.Object rather than a pointer-to-struct: a nil pointer can
// represent null but not unknown, and unknown is exactly what the
// framework marshals when this value is driven from an unresolved
// parent variable. A regression back to a pointer-to-struct surfaces
// here as a Value Conversion Error during terraform validate, which
// resource.UnitTest exercises via the plan-only step below. UnitTest,
// not Test, runs this without TF_ACC; the only dependency is the
// Terraform CLI on PATH.
func TestValidate_URLDrivenByVariable(t *testing.T) {
	t.Parallel()

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: acctest.ProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				// Empty default map plans no resources; the framework still validates the schema against the typed variable.
				Config: `
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
