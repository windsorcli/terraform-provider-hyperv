# Locks the Remove-HypervPortForward contract: tears down the static
# mapping and the firewall rule (if present); both steps tolerate
# ObjectNotFound so a partial out-of-band cleanup doesn't fail Delete.

BeforeAll {
    . $PSScriptRoot/_test_helpers.ps1
    . $PSScriptRoot/../common/preamble.ps1
    . $PSScriptRoot/remove.ps1
}

Describe 'Remove-HypervPortForward' {

    It 'removes the static mapping by StaticMappingID and the firewall rule by DisplayName' {
        Mock Get-NetNatStaticMapping { New-HypervPortForwardSample -StaticMappingID 1 }
        Mock Get-NetFirewallRule { New-HypervFirewallRuleSample }
        Mock Remove-NetNatStaticMapping { }
        Mock Remove-NetFirewallRule { }

        Remove-HypervPortForward `
            -NatName 'windsor-nat' `
            -Protocol 'tcp' `
            -ExternalIPAddress '0.0.0.0' `
            -ExternalPort 80 `
            -FirewallName 'windsor-pf-tcp-80'

        Should -Invoke Remove-NetNatStaticMapping -Times 1 -Exactly -ParameterFilter {
            $StaticMappingID -eq 1
        }
        Should -Invoke Remove-NetFirewallRule -Times 1 -Exactly -ParameterFilter {
            $DisplayName -eq 'windsor-pf-tcp-80'
        }
    }

    It 'tolerates a missing static mapping (best-effort destroy)' {
        # If the mapping was already removed out-of-band (Hyper-V Manager,
        # another tool), Delete should still succeed -- the goal is
        # "no mapping by this tuple exists on the host," and that's
        # already true.
        Mock Get-NetNatStaticMapping { @() }
        Mock Get-NetFirewallRule { New-HypervFirewallRuleSample }
        Mock Remove-NetNatStaticMapping { }
        Mock Remove-NetFirewallRule { }

        { Remove-HypervPortForward `
            -NatName 'windsor-nat' `
            -Protocol 'tcp' `
            -ExternalIPAddress '0.0.0.0' `
            -ExternalPort 80 `
            -FirewallName 'windsor-pf-tcp-80' } |
            Should -Not -Throw

        Should -Invoke Remove-NetNatStaticMapping -Times 0 -Exactly
        Should -Invoke Remove-NetFirewallRule -Times 1 -Exactly
    }

    It 'tolerates a missing firewall rule (best-effort destroy)' {
        Mock Get-NetNatStaticMapping { New-HypervPortForwardSample -StaticMappingID 1 }
        Mock Get-NetFirewallRule { }
        Mock Remove-NetNatStaticMapping { }
        Mock Remove-NetFirewallRule { }

        { Remove-HypervPortForward `
            -NatName 'windsor-nat' `
            -Protocol 'tcp' `
            -ExternalIPAddress '0.0.0.0' `
            -ExternalPort 80 `
            -FirewallName 'windsor-pf-tcp-80' } |
            Should -Not -Throw

        Should -Invoke Remove-NetNatStaticMapping -Times 1 -Exactly
        Should -Invoke Remove-NetFirewallRule -Times 0 -Exactly
    }

    It 'emits no stdout on success (caller relies on dst=nil + exit 0)' {
        Mock Get-NetNatStaticMapping { New-HypervPortForwardSample }
        Mock Get-NetFirewallRule { New-HypervFirewallRuleSample }
        Mock Remove-NetNatStaticMapping { }
        Mock Remove-NetFirewallRule { }

        $output = Remove-HypervPortForward `
            -NatName 'windsor-nat' `
            -Protocol 'tcp' `
            -ExternalIPAddress '0.0.0.0' `
            -ExternalPort 80 `
            -FirewallName 'windsor-pf-tcp-80'

        $output | Should -BeNullOrEmpty
    }

    It 'propagates Remove-NetNatStaticMapping errors instead of swallowing them' {
        # If the mapping exists but Remove fails (busy resource, WMI
        # fault), the Go side must see the error -- otherwise Delete
        # would succeed-on-paper while leaving the mapping live on the
        # host.
        Mock Get-NetNatStaticMapping { New-HypervPortForwardSample }
        Mock Get-NetFirewallRule { New-HypervFirewallRuleSample }
        Mock Remove-NetNatStaticMapping { throw 'simulated WMI fault' }
        Mock Remove-NetFirewallRule { }

        { Remove-HypervPortForward `
            -NatName 'windsor-nat' `
            -Protocol 'tcp' `
            -ExternalIPAddress '0.0.0.0' `
            -ExternalPort 80 `
            -FirewallName 'windsor-pf-tcp-80' } |
            Should -Throw -ExpectedMessage '*WMI fault*'
    }
}
