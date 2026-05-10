# Locks the JSON contract for Get-HypervPortForward. The Go-side typed
# wrapper decodes the output with field tags that match the keys
# asserted here; any change is a wire-level break.

BeforeAll {
    . $PSScriptRoot/_test_helpers.ps1
    . $PSScriptRoot/../common/preamble.ps1
    . $PSScriptRoot/get.ps1
}

Describe 'Get-HypervPortForward' {

    Context 'happy path' {

        It 'finds the mapping by (nat_name, protocol, external_ip, external_port) tuple' {
            # Get-NetNatStaticMapping accepts -NatName but does not accept
            # an external-port filter, so the script enumerates by NatName
            # and filters in-process. Locking the lookup tuple here pins
            # what "this is THE mapping" means for the resource layer.
            Mock Get-NetNatStaticMapping {
                @(
                    New-HypervPortForwardSample -StaticMappingID 1 -ExternalPort 80 -Protocol 'TCP'
                    New-HypervPortForwardSample -StaticMappingID 2 -ExternalPort 443 -Protocol 'TCP'
                    New-HypervPortForwardSample -StaticMappingID 3 -ExternalPort 80 -Protocol 'UDP'
                )
            }
            Mock Get-NetFirewallRule { New-HypervFirewallRuleSample }

            $parsed = Get-HypervPortForward `
                -NatName 'windsor-nat' `
                -Protocol 'tcp' `
                -ExternalIPAddress '0.0.0.0' `
                -ExternalPort 80 `
                -FirewallName 'windsor-pf-tcp-80' | ConvertFrom-Json

            $parsed.StaticMappingId | Should -Be 1
            $parsed.Protocol | Should -Be 'TCP'
            $parsed.ExternalPort | Should -Be 80
        }

        It 'reports FirewallRulePresent=true when the rule exists' {
            Mock Get-NetNatStaticMapping { New-HypervPortForwardSample }
            Mock Get-NetFirewallRule { New-HypervFirewallRuleSample }

            $parsed = Get-HypervPortForward `
                -NatName 'windsor-nat' `
                -Protocol 'tcp' `
                -ExternalIPAddress '0.0.0.0' `
                -ExternalPort 80 `
                -FirewallName 'windsor-pf-tcp-80' | ConvertFrom-Json

            $parsed.FirewallRulePresent | Should -BeTrue
            $parsed.FirewallRuleName | Should -Be 'windsor-pf-tcp-80'
            $parsed.FirewallRuleProfile | Should -Be 'Any'
        }

        It 'reports FirewallRulePresent=false when the rule is missing' {
            # The firewall rule is optional: a user may have set
            # firewall.enabled=false at create time, or removed the rule
            # out-of-band. Read must reflect the actual state without
            # erroring.
            Mock Get-NetNatStaticMapping { New-HypervPortForwardSample }
            Mock Get-NetFirewallRule { }

            $parsed = Get-HypervPortForward `
                -NatName 'windsor-nat' `
                -Protocol 'tcp' `
                -ExternalIPAddress '0.0.0.0' `
                -ExternalPort 80 `
                -FirewallName 'windsor-pf-tcp-80' | ConvertFrom-Json

            $parsed.FirewallRulePresent | Should -BeFalse
            $parsed.FirewallRuleName | Should -Be 'windsor-pf-tcp-80'
        }

        It 'composite Id encodes (nat_name, protocol, external_ip, external_port) lowercase protocol' {
            Mock Get-NetNatStaticMapping { New-HypervPortForwardSample }
            Mock Get-NetFirewallRule { New-HypervFirewallRuleSample }

            $parsed = Get-HypervPortForward `
                -NatName 'windsor-nat' `
                -Protocol 'tcp' `
                -ExternalIPAddress '0.0.0.0' `
                -ExternalPort 80 `
                -FirewallName 'windsor-pf-tcp-80' | ConvertFrom-Json

            $parsed.Id | Should -Be 'windsor-nat:tcp:0.0.0.0:80'
        }
    }

    Context 'error propagation' {

        It 'throws ObjectNotFound when no mapping matches the lookup tuple' {
            # The Go-side typed client maps category=ObjectNotFound to
            # ErrNotFound; resource Read calls RemoveResource on that
            # path. Asserting on Category, not just ErrorId, guards the
            # mapping (an ErrorId drift wouldn't break behavior; a
            # Category drift would silently mis-route).
            Mock Get-NetNatStaticMapping { @() }
            Mock Get-NetFirewallRule { New-HypervFirewallRuleSample }

            $captured = $null
            try {
                Get-HypervPortForward `
                    -NatName 'windsor-nat' `
                    -Protocol 'tcp' `
                    -ExternalIPAddress '0.0.0.0' `
                    -ExternalPort 999 `
                    -FirewallName 'windsor-pf-tcp-999'
            } catch { $captured = $_ }

            $captured | Should -Not -BeNullOrEmpty
            $captured.CategoryInfo.Category.ToString() | Should -Be 'ObjectNotFound'
            $captured.FullyQualifiedErrorId | Should -Match 'PortForwardNotFound'
        }

        It 'tuple disagreement on Protocol surfaces ObjectNotFound (port can be reused across protocols)' {
            # NetNat allows the same external_port across different
            # protocols (TCP/80 + UDP/80 coexist). Lookup must match
            # ALL of (protocol, external_ip, external_port) -- a port-
            # only match would silently return the wrong mapping.
            Mock Get-NetNatStaticMapping {
                New-HypervPortForwardSample -StaticMappingID 99 -Protocol 'UDP' -ExternalPort 80
            }
            Mock Get-NetFirewallRule { }

            $captured = $null
            try {
                Get-HypervPortForward `
                    -NatName 'windsor-nat' `
                    -Protocol 'tcp' `
                    -ExternalIPAddress '0.0.0.0' `
                    -ExternalPort 80 `
                    -FirewallName 'windsor-pf-tcp-80'
            } catch { $captured = $_ }

            $captured | Should -Not -BeNullOrEmpty
            $captured.CategoryInfo.Category.ToString() | Should -Be 'ObjectNotFound'
        }
    }

    Context 'output shape' {

        It 'emits the canonical eleven-field shape' {
            Mock Get-NetNatStaticMapping { New-HypervPortForwardSample }
            Mock Get-NetFirewallRule { New-HypervFirewallRuleSample }

            $parsed = Get-HypervPortForward `
                -NatName 'windsor-nat' `
                -Protocol 'tcp' `
                -ExternalIPAddress '0.0.0.0' `
                -ExternalPort 80 `
                -FirewallName 'windsor-pf-tcp-80' | ConvertFrom-Json

            $parsed.PSObject.Properties.Name | Sort-Object | Should -Be @(
                'ExternalIPAddress',
                'ExternalPort',
                'FirewallRuleName',
                'FirewallRulePresent',
                'FirewallRuleProfile',
                'Id',
                'InternalIPAddress',
                'InternalPort',
                'NatName',
                'Protocol',
                'StaticMappingId'
            )
        }

        It 'compresses output to a single line (Write-HypervResult contract)' {
            Mock Get-NetNatStaticMapping { New-HypervPortForwardSample }
            Mock Get-NetFirewallRule { New-HypervFirewallRuleSample }

            $output = Get-HypervPortForward `
                -NatName 'windsor-nat' `
                -Protocol 'tcp' `
                -ExternalIPAddress '0.0.0.0' `
                -ExternalPort 80 `
                -FirewallName 'windsor-pf-tcp-80'

            $output | Should -BeOfType [string]
            $output -split "`n" | Measure-Object | Select-Object -ExpandProperty Count | Should -Be 1
        }
    }
}
