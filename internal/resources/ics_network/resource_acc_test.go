package ics_network_test

import (
	"fmt"
	"regexp"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/compare"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/knownvalue"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
	"github.com/hashicorp/terraform-plugin-testing/statecheck"
	"github.com/hashicorp/terraform-plugin-testing/tfjsonpath"

	"github.com/windsorcli/terraform-provider-hyperv/internal/acctest"
)

// TestAcc_ICSNetwork_basic covers create, import, a replacing prefix change, and destroy.
func TestAcc_ICSNetwork_basic(t *testing.T) {
	client := acctest.NewClient(t)
	name := acctest.RandomName("ics")

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { acctest.PreCheck(t) },
		ProtoV6ProviderFactories: acctest.ProtoV6ProviderFactories,
		CheckDestroy:             acctest.CheckResourceGone("hyperv_ics_network", client.GetICSNetwork),
		Steps: []resource.TestStep{
			{
				Config: icsNetworkConfig(name, "172.30.91.0/24"),
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue("hyperv_ics_network.test",
						tfjsonpath.New("id"), knownvalue.StringRegexp(guidRegexp)),
					statecheck.ExpectKnownValue("hyperv_ics_network.test",
						tfjsonpath.New("address_prefix"), knownvalue.StringExact("172.30.91.0/24")),
					statecheck.ExpectKnownValue("hyperv_ics_network.test",
						tfjsonpath.New("host_address"), knownvalue.StringExact("172.30.91.1")),
					statecheck.ExpectKnownValue("hyperv_ics_network.test",
						tfjsonpath.New("switch_name"), knownvalue.NotNull()),
					statecheck.ExpectKnownValue("hyperv_ics_network.test",
						tfjsonpath.New("isolation_id"), knownvalue.NotNull()),
				},
			},
			{
				ResourceName:      "hyperv_ics_network.test",
				ImportState:       true,
				ImportStateVerify: true,
			},
			{
				Config: icsNetworkConfig(name, "172.30.92.0/24"),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{
						plancheck.ExpectResourceAction("hyperv_ics_network.test", plancheck.ResourceActionDestroyBeforeCreate),
					},
				},
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue("hyperv_ics_network.test",
						tfjsonpath.New("host_address"), knownvalue.StringExact("172.30.92.1")),
				},
			},
		},
	})
}

// TestAcc_ICSNetwork_sharedSwitch pins that two ICS networks share one switch and get distinct isolation IDs.
func TestAcc_ICSNetwork_sharedSwitch(t *testing.T) {
	client := acctest.NewClient(t)
	first := acctest.RandomName("ics-a")
	second := acctest.RandomName("ics-b")

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { acctest.PreCheck(t) },
		ProtoV6ProviderFactories: acctest.ProtoV6ProviderFactories,
		CheckDestroy:             acctest.CheckResourceGone("hyperv_ics_network", client.GetICSNetwork),
		Steps: []resource.TestStep{
			{
				Config: fmt.Sprintf(`
resource "hyperv_ics_network" "a" {
  name           = %q
  address_prefix = "172.30.93.0/24"
}

resource "hyperv_ics_network" "b" {
  name           = %q
  address_prefix = "172.30.94.0/24"
  depends_on     = [hyperv_ics_network.a]
}
`, first, second),
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.CompareValuePairs(
						"hyperv_ics_network.a", tfjsonpath.New("switch_name"),
						"hyperv_ics_network.b", tfjsonpath.New("switch_name"),
						compare.ValuesSame()),
					statecheck.CompareValuePairs(
						"hyperv_ics_network.a", tfjsonpath.New("isolation_id"),
						"hyperv_ics_network.b", tfjsonpath.New("isolation_id"),
						compare.ValuesDiffer()),
				},
			},
		},
	})
}

var guidRegexp = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

// icsNetworkConfig renders one ICS network.
func icsNetworkConfig(name, prefix string) string {
	return fmt.Sprintf(`
resource "hyperv_ics_network" "test" {
  name           = %q
  address_prefix = %q
}
`, name, prefix)
}
