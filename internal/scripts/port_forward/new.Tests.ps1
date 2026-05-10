# Locks the JSON contract for New-HypervPortForward -- both the input-side
# splat logic (which JSON keys map to which Add-NetNatStaticMapping +
# New-NetFirewallRule parameters) and the output-side read shape that
# round-trips through Get-HypervPortForward.

BeforeAll {
    . $PSScriptRoot/_test_helpers.ps1
    . $PSScriptRoot/../common/preamble.ps1
    . $PSScriptRoot/new.ps1
}

Describe 'New-HypervPortForward' {

    Context 'NAT precondition' {

        It 'rejects when the referenced NetNat does not exist (clear pre-cmdlet error)' {
            # Cross-resource: nat_name must resolve to an existing NetNat
            # before any host mutation. Without this precondition, the
            # Add-NetNatStaticMapping cmdlet errors with an opaque "no
            # NetNat by that name" message that obscures the actual
            # config dependency. Throwing here surfaces the real cause.
            Mock Get-NetNat { }
            Mock Add-NetNatStaticMapping { New-HypervPortForwardSample }
            Mock New-NetFirewallRule { New-HypervFirewallRuleSample }

            { New-HypervPortForward `
                -NatName 'missing-nat' `
                -Protocol 'tcp' `
                -ExternalIPAddress '0.0.0.0' `
                -ExternalPort 80 `
                -InternalIPAddress '192.168.100.10' `
                -InternalPort 30080 `
                -FirewallEnabled $true `
                -FirewallName 'windsor-pf-tcp-80' `
                -FirewallProfile 'Any' } |
                Should -Throw -ExpectedMessage '*missing-nat*'

            Should -Invoke Add-NetNatStaticMapping -Times 0 -Exactly
            Should -Invoke New-NetFirewallRule -Times 0 -Exactly
        }
    }

    Context 'happy path' {

        It 'forwards all six required mapping params to Add-NetNatStaticMapping' {
            Mock Get-NetNat { New-HypervNetNatSample }
            Mock Add-NetNatStaticMapping { New-HypervPortForwardSample }
            Mock New-NetFirewallRule { New-HypervFirewallRuleSample }
            Mock Get-NetFirewallRule { New-HypervFirewallRuleSample }

            New-HypervPortForward `
                -NatName 'windsor-nat' `
                -Protocol 'tcp' `
                -ExternalIPAddress '0.0.0.0' `
                -ExternalPort 80 `
                -InternalIPAddress '192.168.100.10' `
                -InternalPort 30080 `
                -FirewallEnabled $true `
                -FirewallName 'windsor-pf-tcp-80' `
                -FirewallProfile 'Any' | Out-Null

            # Cmdlet expects uppercase Protocol enum (TCP / UDP). The
            # script normalizes the lowercase wire value before forwarding.
            Should -Invoke Add-NetNatStaticMapping -Times 1 -Exactly -ParameterFilter {
                $NatName -eq 'windsor-nat' -and
                $Protocol -eq 'TCP' -and
                $ExternalIPAddress -eq '0.0.0.0' -and
                $ExternalPort -eq 80 -and
                $InternalIPAddress -eq '192.168.100.10' -and
                $InternalPort -eq 30080
            }
        }

        It 'creates the firewall rule when firewall.enabled = true' {
            Mock Get-NetNat { New-HypervNetNatSample }
            Mock Add-NetNatStaticMapping { New-HypervPortForwardSample }
            Mock New-NetFirewallRule { New-HypervFirewallRuleSample }
            Mock Get-NetFirewallRule { New-HypervFirewallRuleSample }

            New-HypervPortForward `
                -NatName 'windsor-nat' `
                -Protocol 'tcp' `
                -ExternalIPAddress '0.0.0.0' `
                -ExternalPort 80 `
                -InternalIPAddress '192.168.100.10' `
                -InternalPort 30080 `
                -FirewallEnabled $true `
                -FirewallName 'windsor-pf-tcp-80' `
                -FirewallProfile 'Any' | Out-Null

            Should -Invoke New-NetFirewallRule -Times 1 -Exactly -ParameterFilter {
                $DisplayName -eq 'windsor-pf-tcp-80' -and
                $Direction -eq 'Inbound' -and
                $Action -eq 'Allow' -and
                $Protocol -eq 'TCP' -and
                $LocalPort -eq 80 -and
                $Profile -eq 'Any'
            }
        }

        It 'skips New-NetFirewallRule when firewall.enabled = false' {
            Mock Get-NetNat { New-HypervNetNatSample }
            Mock Add-NetNatStaticMapping { New-HypervPortForwardSample }
            Mock New-NetFirewallRule { New-HypervFirewallRuleSample }
            Mock Get-NetFirewallRule { }

            New-HypervPortForward `
                -NatName 'windsor-nat' `
                -Protocol 'tcp' `
                -ExternalIPAddress '0.0.0.0' `
                -ExternalPort 80 `
                -InternalIPAddress '192.168.100.10' `
                -InternalPort 30080 `
                -FirewallEnabled $false `
                -FirewallName 'windsor-pf-tcp-80' `
                -FirewallProfile 'Any' | Out-Null

            Should -Invoke Add-NetNatStaticMapping -Times 1 -Exactly
            Should -Invoke New-NetFirewallRule -Times 0 -Exactly
        }

        It 'forwards UDP protocol uppercased to both cmdlets' {
            Mock Get-NetNat { New-HypervNetNatSample }
            Mock Add-NetNatStaticMapping { New-HypervPortForwardSample -Protocol 'UDP' }
            Mock New-NetFirewallRule { New-HypervFirewallRuleSample }
            Mock Get-NetFirewallRule { New-HypervFirewallRuleSample }

            New-HypervPortForward `
                -NatName 'windsor-nat' `
                -Protocol 'udp' `
                -ExternalIPAddress '0.0.0.0' `
                -ExternalPort 53 `
                -InternalIPAddress '192.168.100.10' `
                -InternalPort 53 `
                -FirewallEnabled $true `
                -FirewallName 'windsor-pf-udp-53' `
                -FirewallProfile 'Any' | Out-Null

            Should -Invoke Add-NetNatStaticMapping -Times 1 -Exactly -ParameterFilter {
                $Protocol -eq 'UDP'
            }
            Should -Invoke New-NetFirewallRule -Times 1 -Exactly -ParameterFilter {
                $Protocol -eq 'UDP'
            }
        }
    }

    Context 'rollback on partial failure' {
        # Symmetric with vswitch/new.ps1's NAT branch: once the static
        # mapping lands, a subsequent New-NetFirewallRule failure must
        # tear down the mapping before re-throwing -- otherwise an orphan
        # mapping survives on the host with no Terraform state and the
        # next apply trips on a duplicate (NetNat enforces uniqueness on
        # the (Protocol, ExternalIP, ExternalPort) tuple).

        It 'rolls back the static mapping when New-NetFirewallRule fails' {
            Mock Get-NetNat { New-HypervNetNatSample }
            Mock Add-NetNatStaticMapping { New-HypervPortForwardSample }
            Mock New-NetFirewallRule { throw 'simulated firewall failure' }
            Mock Remove-NetNatStaticMapping { }
            Mock Get-NetFirewallRule { }

            { New-HypervPortForward `
                -NatName 'windsor-nat' `
                -Protocol 'tcp' `
                -ExternalIPAddress '0.0.0.0' `
                -ExternalPort 80 `
                -InternalIPAddress '192.168.100.10' `
                -InternalPort 30080 `
                -FirewallEnabled $true `
                -FirewallName 'windsor-pf-tcp-80' `
                -FirewallProfile 'Any' } |
                Should -Throw -ExpectedMessage '*firewall failure*'

            Should -Invoke Remove-NetNatStaticMapping -Times 1 -Exactly -ParameterFilter {
                $StaticMappingID -eq 1
            }
        }

        It 'rollback re-throws the ORIGINAL failure (not cleanup chatter)' {
            Mock Get-NetNat { New-HypervNetNatSample }
            Mock Add-NetNatStaticMapping { New-HypervPortForwardSample }
            Mock New-NetFirewallRule { throw 'original firewall failure' }
            Mock Remove-NetNatStaticMapping { throw 'cleanup chatter' }
            Mock Get-NetFirewallRule { }

            { New-HypervPortForward `
                -NatName 'windsor-nat' `
                -Protocol 'tcp' `
                -ExternalIPAddress '0.0.0.0' `
                -ExternalPort 80 `
                -InternalIPAddress '192.168.100.10' `
                -InternalPort 30080 `
                -FirewallEnabled $true `
                -FirewallName 'windsor-pf-tcp-80' `
                -FirewallProfile 'Any' } |
                Should -Throw -ExpectedMessage '*original firewall failure*'
        }
    }

    Context 'output shape' {

        It 'emits the canonical eleven-field read shape' {
            Mock Get-NetNat { New-HypervNetNatSample }
            Mock Add-NetNatStaticMapping { New-HypervPortForwardSample }
            Mock New-NetFirewallRule { New-HypervFirewallRuleSample }
            Mock Get-NetFirewallRule { New-HypervFirewallRuleSample }

            $parsed = (New-HypervPortForward `
                -NatName 'windsor-nat' `
                -Protocol 'tcp' `
                -ExternalIPAddress '0.0.0.0' `
                -ExternalPort 80 `
                -InternalIPAddress '192.168.100.10' `
                -InternalPort 30080 `
                -FirewallEnabled $true `
                -FirewallName 'windsor-pf-tcp-80' `
                -FirewallProfile 'Any') | ConvertFrom-Json

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

        It 'composite Id encodes (nat_name, protocol, external_ip, external_port)' {
            Mock Get-NetNat { New-HypervNetNatSample }
            Mock Add-NetNatStaticMapping { New-HypervPortForwardSample }
            Mock New-NetFirewallRule { New-HypervFirewallRuleSample }
            Mock Get-NetFirewallRule { New-HypervFirewallRuleSample }

            $parsed = (New-HypervPortForward `
                -NatName 'windsor-nat' `
                -Protocol 'tcp' `
                -ExternalIPAddress '0.0.0.0' `
                -ExternalPort 80 `
                -InternalIPAddress '192.168.100.10' `
                -InternalPort 30080 `
                -FirewallEnabled $true `
                -FirewallName 'windsor-pf-tcp-80' `
                -FirewallProfile 'Any') | ConvertFrom-Json

            # Lowercase protocol in the Id even though the wire reports
            # uppercase from Get-NetNatStaticMapping -- the Id is the
            # resource-side identifier consumers reference, and the
            # schema's `protocol` attribute is lowercase. Keeping them
            # consistent avoids a stringly-typed mismatch.
            $parsed.Id | Should -Be 'windsor-nat:tcp:0.0.0.0:80'
        }
    }
}
