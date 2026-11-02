# Locks Remove-HypervPortProxy's best-effort delete.

BeforeAll {
    . $PSScriptRoot/_test_helpers.ps1
    . $PSScriptRoot/../common/preamble.ps1
    . $PSScriptRoot/../common/firewall-rule.ps1
    . $PSScriptRoot/_rule.ps1
    . $PSScriptRoot/remove.ps1
}

Describe 'Remove-HypervPortProxy' {

    BeforeEach {
        Mock Invoke-HypervNetsh { }
        Mock Remove-NetFirewallRule { }
    }

    It 'deletes the rule and the firewall rule' {
        Mock Get-ItemProperty { New-HypervPortProxyRegistryItem }
        Mock Get-NetFirewallRule { New-HypervFirewallRuleSample }

        Remove-HypervPortProxy -ListenAddress '0.0.0.0' -ListenPort 15000 -FirewallName 'hyperv-pp-0.0.0.0-15000'

        Should -Invoke Invoke-HypervNetsh -Times 1 -ParameterFilter {
            ($ArgumentList -join ' ') -eq 'interface portproxy delete v4tov4 listenaddress=0.0.0.0 listenport=15000'
        }
        Should -Invoke Remove-NetFirewallRule -Times 1 -ParameterFilter { $DisplayName -eq 'hyperv-pp-0.0.0.0-15000' }
    }

    It 'succeeds when both are already gone' {
        Mock Get-ItemProperty { }
        Mock Get-NetFirewallRule { }

        { Remove-HypervPortProxy -ListenAddress '0.0.0.0' -ListenPort 15000 -FirewallName 'x' } | Should -Not -Throw
        Should -Invoke Invoke-HypervNetsh -Times 0
        Should -Invoke Remove-NetFirewallRule -Times 0
    }
}
