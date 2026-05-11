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
    . $PSScriptRoot/_retry.ps1
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

            # Production cmdlet's -Enabled takes the string form of the
            # NetSecurity.Enabled enum ("True" / "False"), not a bool.
            # The script converts the bool input before forwarding, so
            # the assertion is on the string form here.
            Should -Invoke Set-NetFirewallRule -Times 1 -Exactly -ParameterFilter {
                $DisplayName -eq 'windsor-pf-tcp-80' -and
                $Enabled -eq 'True' -and
                $Profile -eq 'Domain'
            }
        }

        It 'recreates the rule when firewall.enabled=true and the rule is absent (out-of-band delete recovery)' {
            # Closes the loop the reviewer flagged: without this branch,
            # Read reports enabled=false, terraform plans an Update,
            # Update silently skips (because no rule existed to mutate),
            # and the next refresh re-detects the same diff forever.
            # Mocks: mapping reconciliation proceeds as usual; the
            # firewall probe returns $null on the first call (rule
            # absent), then a freshly-created rule on the read-back.
            Mock Get-NetNatStaticMapping { New-HypervPortForwardSample -StaticMappingID 1 }
            Mock Remove-NetNatStaticMapping { }
            Mock Add-NetNatStaticMapping { New-HypervPortForwardSample -StaticMappingID 1 }
            Mock Set-NetFirewallRule { }
            $script:fwCallCount = 0
            Mock Get-NetFirewallRule {
                $script:fwCallCount++
                if ($script:fwCallCount -eq 1) { return $null }
                return New-HypervFirewallRuleSample -Profile 'Any'
            }
            Mock New-NetFirewallRule { }

            Set-HypervPortForward `
                -NatName 'windsor-nat' `
                -Protocol 'tcp' `
                -ExternalIPAddress '0.0.0.0' `
                -ExternalPort 80 `
                -InternalIPAddress '192.168.100.10' `
                -InternalPort 30080 `
                -FirewallEnabled $true `
                -FirewallName 'windsor-pf-tcp-80' `
                -FirewallProfile 'Any' | Out-Null

            Should -Invoke Set-NetFirewallRule -Times 0 -Exactly
            Should -Invoke New-NetFirewallRule -Times 1 -Exactly -ParameterFilter {
                $DisplayName -eq 'windsor-pf-tcp-80' -and
                $Direction -eq 'Inbound' -and
                $Action -eq 'Allow' -and
                $Protocol -eq 'TCP' -and
                $LocalPort -eq 80 -and
                $Profile -eq 'Any'
            }
        }
    }

    Context 'Add-NetNatStaticMapping transient retry' {
        # Mirror of new.Tests.ps1's retry context. The Remove + Add
        # pattern in Set-HypervPortForward is just as exposed to the
        # transient Win32 errors NetSetup/WMI surfaces under concurrent
        # pressure (ERROR_DUP_NAME 0x80070034 and ERROR_SHARING_VIOLATION
        # 0x80070020) -- the cmdlet is idempotent on retry, so the same
        # Invoke-WithNetNatRetry helper wraps the Add call.

        BeforeEach {
            Mock Get-NetNatStaticMapping { New-HypervPortForwardSample -StaticMappingID 1 }
            Mock Remove-NetNatStaticMapping { }
            Mock Set-NetFirewallRule { }
            Mock Get-NetFirewallRule { New-HypervFirewallRuleSample }
            Mock Start-Sleep { }
        }

        It 'retries Add-NetNatStaticMapping when ERROR_DUP_NAME bubbles transiently' {
            $script:dupCalls = 0
            Mock Add-NetNatStaticMapping {
                $script:dupCalls++
                if ($script:dupCalls -lt 2) {
                    # Marshal.GetExceptionForHR yields a COMException whose
                    # HResult is exactly -2147024844 (0x80070034) on every
                    # platform, matching what NetSetup/WMI surfaces in prod.
                    throw [System.Runtime.InteropServices.Marshal]::GetExceptionForHR(-2147024844)
                }
                New-HypervPortForwardSample -StaticMappingID 2
            }

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

            Should -Invoke Add-NetNatStaticMapping -Times 2 -Exactly
            Should -Invoke Remove-NetNatStaticMapping -Times 1 -Exactly
        }

        It 'gives up after the retry cap on persistent ERROR_DUP_NAME' {
            Mock Add-NetNatStaticMapping {
                throw [System.Runtime.InteropServices.Marshal]::GetExceptionForHR(-2147024844)
            }

            { Set-HypervPortForward `
                -NatName 'windsor-nat' `
                -Protocol 'tcp' `
                -ExternalIPAddress '0.0.0.0' `
                -ExternalPort 80 `
                -InternalIPAddress '192.168.100.20' `
                -InternalPort 30080 `
                -FirewallEnabled $true `
                -FirewallName 'windsor-pf-tcp-80' `
                -FirewallProfile 'Any' } |
                Should -Throw

            Should -Invoke Add-NetNatStaticMapping -Times 4 -Exactly
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
