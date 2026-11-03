package port_proxy_test

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

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

// The listener test binds a TEST-NET-2 address it assigns to an Internal switch's host vNIC.
const (
	accListenerAddress = "198.51.100.1"
	accListenerPort    = 15124
)

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

// TestAcc_PortProxy_listenerReadded covers a rule netsh holds with no listener, the state a rule on a
// DHCP address has been observed in after a host reboot. The next plan updates it in place and the
// apply re-adds it, whether or not connect_address changes.
func TestAcc_PortProxy_listenerReadded(t *testing.T) {
	client := acctest.NewClient(t)
	switchName := acctest.RandomName("pp")
	hostIf := fmt.Sprintf("vEthernet (%s)", switchName)
	fwName := fmt.Sprintf("hyperv-pp-%s-%d", accListenerAddress, accListenerPort)

	listenerChecks := []statecheck.StateCheck{
		statecheck.ExpectKnownValue("hyperv_port_proxy.test", tfjsonpath.New("listening"), knownvalue.Bool(true)),
	}
	updateInPlace := resource.ConfigPlanChecks{
		PreApply: []plancheck.PlanCheck{
			plancheck.ExpectResourceAction("hyperv_port_proxy.test", plancheck.ResourceActionUpdate),
			plancheck.ExpectKnownValue("hyperv_port_proxy.test", tfjsonpath.New("listening"), knownvalue.Bool(true)),
		},
	}

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { acctest.PreCheck(t) },
		ProtoV6ProviderFactories: acctest.ProtoV6ProviderFactories,
		CheckDestroy: acctest.CheckResourceGone("hyperv_port_proxy",
			func(ctx context.Context, _ string) (*hyperv.PortProxy, error) {
				return client.GetPortProxy(ctx, hyperv.GetPortProxyInput{
					ListenAddress: accListenerAddress,
					ListenPort:    accListenerPort,
					FirewallName:  fwName,
				})
			}),
		Steps: []resource.TestStep{
			{
				Config: listenerSwitchConfig(switchName),
			},
			{
				PreConfig:         func() { addHostAddress(t, client, hostIf) },
				Config:            listenerConfig(switchName, "203.0.113.10"),
				ConfigStateChecks: listenerChecks,
			},
			{
				PreConfig:         func() { dropListener(t, client, hostIf) },
				Config:            listenerConfig(switchName, "203.0.113.10"),
				ConfigPlanChecks:  updateInPlace,
				ConfigStateChecks: listenerChecks,
			},
			{
				PreConfig:        func() { dropListener(t, client, hostIf) },
				Config:           listenerConfig(switchName, "203.0.113.11"),
				ConfigPlanChecks: updateInPlace,
				ConfigStateChecks: append(listenerChecks,
					statecheck.ExpectKnownValue("hyperv_port_proxy.test",
						tfjsonpath.New("connect_address"), knownvalue.StringExact("203.0.113.11"))),
			},
		},
	})
}

// addHostAddress assigns accListenerAddress to the switch's host vNIC and waits until it is usable.
func addHostAddress(t *testing.T, client *hyperv.Client, hostIf string) {
	t.Helper()
	runBench(t, client, fmt.Sprintf(`
$ErrorActionPreference = 'Stop'
New-NetIPAddress -InterfaceAlias '%[1]s' -IPAddress '%[2]s' -PrefixLength 24 | Out-Null
%[3]s
'ok'`, hostIf, accListenerAddress, waitPreferredPS))
}

// dropListener leaves the rule in netsh with no listener: it re-adds the rule while the address is
// absent, then restores the address, which the IP Helper service has been observed not to bind.
func dropListener(t *testing.T, client *hyperv.Client, hostIf string) {
	t.Helper()
	runBench(t, client, fmt.Sprintf(`
$ErrorActionPreference = 'Stop'
$rule = Get-ItemPropertyValue 'HKLM:\SYSTEM\CurrentControlSet\Services\PortProxy\v4tov4\tcp' -Name '%[2]s/%[3]d'
$connectAddress, $connectPort = $rule.Split('/', 2)
Remove-NetIPAddress -InterfaceAlias '%[1]s' -IPAddress '%[2]s' -Confirm:$false
netsh interface portproxy delete v4tov4 listenaddress=%[2]s listenport=%[3]d | Out-Null
netsh interface portproxy add v4tov4 listenaddress=%[2]s listenport=%[3]d connectaddress=$connectAddress connectport=$connectPort | Out-Null
New-NetIPAddress -InterfaceAlias '%[1]s' -IPAddress '%[2]s' -PrefixLength 24 | Out-Null
%[4]s
$owner = (Get-CimInstance Win32_Service -Filter "Name='iphlpsvc'").ProcessId
$bound = @(Get-NetTCPConnection -State Listen -LocalPort %[3]d -ErrorAction SilentlyContinue |
    Where-Object { $_.LocalAddress -eq '%[2]s' -and $_.OwningProcess -eq $owner })
if ($bound.Count -gt 0) { throw 'the IP Helper service bound the rule; the test cannot reproduce a missing listener' }
'ok'`, hostIf, accListenerAddress, accListenerPort, waitPreferredPS))
}

// waitPreferredPS polls until accListenerAddress leaves the Tentative state.
var waitPreferredPS = fmt.Sprintf(`
for ($i = 0; $i -lt 40; $i++) {
    if ((Get-NetIPAddress -IPAddress '%s' -ErrorAction SilentlyContinue).AddressState -eq 'Preferred') { break }
    Start-Sleep -Milliseconds 250
}`, accListenerAddress)

// runBench runs a PowerShell snippet on the bench and fails the test unless it prints ok.
func runBench(t *testing.T, client *hyperv.Client, script string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	res, err := client.RunScript(ctx, script, nil)
	if err != nil {
		t.Fatalf("bench script: %v", err)
	}
	if strings.TrimSpace(string(res.Stdout)) != "ok" {
		t.Fatalf("bench script failed (exit %d): stdout=%s stderr=%s", res.ExitCode, res.Stdout, res.Stderr)
	}
}

// listenerSwitchConfig renders the Internal switch whose host vNIC carries accListenerAddress.
func listenerSwitchConfig(switchName string) string {
	return fmt.Sprintf(`
resource "hyperv_virtual_switch" "host" {
  name        = %q
  switch_type = "Internal"
}
`, switchName)
}

// listenerConfig renders the switch plus one rule on accListenerAddress.
func listenerConfig(switchName, connectAddress string) string {
	return listenerSwitchConfig(switchName) + fmt.Sprintf(`
resource "hyperv_port_proxy" "test" {
  listen_address  = %q
  listen_port     = %d
  connect_address = %q
  connect_port    = 50000

  depends_on = [hyperv_virtual_switch.host]
}
`, accListenerAddress, accListenerPort, connectAddress)
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
