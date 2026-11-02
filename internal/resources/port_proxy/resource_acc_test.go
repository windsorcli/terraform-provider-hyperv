package port_proxy_test

import (
	"context"
	"fmt"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/knownvalue"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
	"github.com/hashicorp/terraform-plugin-testing/statecheck"
	"github.com/hashicorp/terraform-plugin-testing/tfjsonpath"

	"github.com/windsorcli/terraform-provider-hyperv/internal/acctest"
	"github.com/windsorcli/terraform-provider-hyperv/internal/hyperv"
)

// The rule forwards to a TEST-NET-3 address, since netsh doesn't require the target to answer.
const accListenPort = 15123

// TestAcc_PortProxy_basic covers create, an in-place retarget, import, and destroy.
func TestAcc_PortProxy_basic(t *testing.T) {
	client := acctest.NewClient(t)
	fwName := fmt.Sprintf("hyperv-pp-0.0.0.0-%d", accListenPort)

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { acctest.PreCheck(t) },
		ProtoV6ProviderFactories: acctest.ProtoV6ProviderFactories,
		CheckDestroy: acctest.CheckResourceGone("hyperv_port_proxy",
			func(ctx context.Context, _ string) (*hyperv.PortProxy, error) {
				return client.GetPortProxy(ctx, hyperv.GetPortProxyInput{
					ListenAddress: "0.0.0.0",
					ListenPort:    accListenPort,
					FirewallName:  fwName,
				})
			}),
		Steps: []resource.TestStep{
			{
				Config: portProxyConfig("203.0.113.10"),
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue("hyperv_port_proxy.test",
						tfjsonpath.New("id"),
						knownvalue.StringExact(fmt.Sprintf("0.0.0.0:%d", accListenPort))),
					statecheck.ExpectKnownValue("hyperv_port_proxy.test",
						tfjsonpath.New("connect_address"),
						knownvalue.StringExact("203.0.113.10")),
					statecheck.ExpectKnownValue("hyperv_port_proxy.test",
						tfjsonpath.New("firewall_rule").AtMapKey("enabled"),
						knownvalue.Bool(true)),
					statecheck.ExpectKnownValue("hyperv_port_proxy.test",
						tfjsonpath.New("firewall_rule").AtMapKey("name"),
						knownvalue.StringExact(fwName)),
				},
			},
			{
				Config: portProxyConfig("203.0.113.11"),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{
						plancheck.ExpectResourceAction("hyperv_port_proxy.test", plancheck.ResourceActionUpdate),
					},
				},
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue("hyperv_port_proxy.test",
						tfjsonpath.New("connect_address"),
						knownvalue.StringExact("203.0.113.11")),
				},
			},
			{
				ResourceName:      "hyperv_port_proxy.test",
				ImportState:       true,
				ImportStateVerify: true,
			},
		},
	})
}

// portProxyConfig renders one rule on accListenPort forwarding to connectAddress:50000.
func portProxyConfig(connectAddress string) string {
	return fmt.Sprintf(`
resource "hyperv_port_proxy" "test" {
  listen_port     = %d
  connect_address = %q
  connect_port    = 50000
}
`, accListenPort, connectAddress)
}
