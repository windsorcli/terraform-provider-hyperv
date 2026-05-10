# Locks the partial-update semantics of Set-HypervPortForward.
#
# NetNatStaticMapping has no in-place edit cmdlet -- mutating
# internal_ip / internal_port requires Remove-NetNatStaticMapping +
# Add-NetNatStaticMapping (the new mapping gets a fresh StaticMappingID,
# which the read-back returns and the resource layer threads back into
# state). The firewall rule, by contrast, has Set-NetFirewallRule for
# in-place mutation.

BeforeAll {
    . $PSScriptRoot/_test_helpers.ps1
    . $PSScriptRoot/../common/preamble.ps1
    . $PSScriptRoot/set.ps1
}

Describe 'Set-HypervPortForward' {

    Context 'mapping mutation (internal_ip / internal_port change)' {

        It 'tears down the existing mapping and re-adds with the new internal target' {
            Mock Get-NetNatStaticMapping { New-HypervPortForwardSample -StaticMappingID 1 }
            Mock Remove-NetNatStaticMapping { }
            Mock Add-NetNatStaticMapping { New-HypervPortForwardSample -StaticMappingID 2 -InternalIPAddress '192.168.100.20' }
            Mock Set-NetFirewallRule { }
            Mock Get-NetFirewallRule { New-HypervFirewallRuleSample }

            Set-HypervPortForward `
                -NatName 'windsor-nat' `
                -Protocol 'tcp' `
                -ExternalIPAddress '0.0.0.0' `
                -ExternalPort 80 `
                -InternalIPAddress '192.168.100.20' `
                -InternalPort 30080 `
                -FirewallEnabled $true `
                -FirewallName 'windsor-pf-tcp-80' `
                -FirewallProfile 'Any' | Out-Null

            Should -Invoke Remove-NetNatStaticMapping -Times 1 -Exactly -ParameterFilter {
                $StaticMappingID -eq 1
            }
            Should -Invoke Add-NetNatStaticMapping -Times 1 -Exactly -ParameterFilter {
                $InternalIPAddress -eq '192.168.100.20'
            }
        }

        It 'returns the NEW StaticMappingID after the Remove + Add (the ID changes)' {
            Mock Get-NetNatStaticMapping { New-HypervPortForwardSample -StaticMappingID 1 }
            Mock Remove-NetNatStaticMapping { }
            Mock Add-NetNatStaticMapping { New-HypervPortForwardSample -StaticMappingID 42 -InternalIPAddress '192.168.100.20' }
            Mock Set-NetFirewallRule { }
            Mock Get-NetFirewallRule { New-HypervFirewallRuleSample }

            $parsed = Set-HypervPortForward `
                -NatName 'windsor-nat' `
                -Protocol 'tcp' `
                -ExternalIPAddress '0.0.0.0' `
                -ExternalPort 80 `
                -InternalIPAddress '192.168.100.20' `
                -InternalPort 30080 `
                -FirewallEnabled $true `
                -FirewallName 'windsor-pf-tcp-80' `
                -FirewallProfile 'Any' | ConvertFrom-Json

            $parsed.StaticMappingId | Should -Be 42
        }
    }

    Context 'firewall mutation' {

        It 'forwards Set-NetFirewallRule with the new profile' {
            Mock Get-NetNatStaticMapping { New-HypervPortForwardSample -StaticMappingID 1 }
            Mock Remove-NetNatStaticMapping { }
            Mock Add-NetNatStaticMapping { New-HypervPortForwardSample -StaticMappingID 1 }
            Mock Set-NetFirewallRule { }
            Mock Get-NetFirewallRule { New-HypervFirewallRuleSample -Profile 'Domain' }

            Set-HypervPortForward `
                -NatName 'windsor-nat' `
                -Protocol 'tcp' `
                -ExternalIPAddress '0.0.0.0' `
                -ExternalPort 80 `
                -InternalIPAddress '192.168.100.10' `
                -InternalPort 30080 `
                -FirewallEnabled $true `
                -FirewallName 'windsor-pf-tcp-80' `
                -FirewallProfile 'Domain' | Out-Null

            Should -Invoke Set-NetFirewallRule -Times 1 -Exactly -ParameterFilter {
                $DisplayName -eq 'windsor-pf-tcp-80' -and
                $Enabled -eq $true -and
                $Profile -eq 'Domain'
            }
        }
    }

    Context 'error propagation' {

        It 'throws ObjectNotFound when no existing mapping matches the lookup tuple' {
            # Symmetric with get.ps1: an Update against a missing mapping
            # surfaces ObjectNotFound so the Go-side resource Update can
            # treat it as state drift (RemoveResource semantics) rather
            # than an opaque ErrPSExecution.
            Mock Get-NetNatStaticMapping { @() }
            Mock Remove-NetNatStaticMapping { }
            Mock Add-NetNatStaticMapping { New-HypervPortForwardSample }
            Mock Set-NetFirewallRule { }
            Mock Get-NetFirewallRule { }

            $captured = $null
            try {
                Set-HypervPortForward `
                    -NatName 'windsor-nat' `
                    -Protocol 'tcp' `
                    -ExternalIPAddress '0.0.0.0' `
                    -ExternalPort 80 `
                    -InternalIPAddress '192.168.100.20' `
                    -InternalPort 30080 `
                    -FirewallEnabled $true `
                    -FirewallName 'windsor-pf-tcp-80' `
                    -FirewallProfile 'Any'
            } catch { $captured = $_ }

            $captured | Should -Not -BeNullOrEmpty
            $captured.CategoryInfo.Category.ToString() | Should -Be 'ObjectNotFound'
            Should -Invoke Remove-NetNatStaticMapping -Times 0 -Exactly
            Should -Invoke Add-NetNatStaticMapping -Times 0 -Exactly
        }
    }

    Context 'output shape' {

        It 'emits the same eleven-field shape as Get-HypervPortForward' {
            Mock Get-NetNatStaticMapping { New-HypervPortForwardSample -StaticMappingID 1 }
            Mock Remove-NetNatStaticMapping { }
            Mock Add-NetNatStaticMapping { New-HypervPortForwardSample -StaticMappingID 2 }
            Mock Set-NetFirewallRule { }
            Mock Get-NetFirewallRule { New-HypervFirewallRuleSample }

            $parsed = Set-HypervPortForward `
                -NatName 'windsor-nat' `
                -Protocol 'tcp' `
                -ExternalIPAddress '0.0.0.0' `
                -ExternalPort 80 `
                -InternalIPAddress '192.168.100.20' `
                -InternalPort 30080 `
                -FirewallEnabled $true `
                -FirewallName 'windsor-pf-tcp-80' `
                -FirewallProfile 'Any' | ConvertFrom-Json

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
    }
}
