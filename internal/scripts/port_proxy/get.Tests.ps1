# Locks the JSON contract for Get-HypervPortProxy; the Go DTO decodes these keys.

BeforeAll {
    . $PSScriptRoot/_test_helpers.ps1
    . $PSScriptRoot/../common/preamble.ps1
    . $PSScriptRoot/_rule.ps1
    . $PSScriptRoot/get.ps1
}

Describe 'Get-HypervPortProxy' {

    It 'emits the eight-field read result' {
        Mock Get-ItemProperty { New-HypervPortProxyRegistryItem }
        Mock Get-NetFirewallRule { New-HypervFirewallRuleSample }

        $parsed = Get-HypervPortProxy -ListenAddress '0.0.0.0' -ListenPort 15000 `
            -FirewallName 'hyperv-pp-0.0.0.0-15000' | ConvertFrom-Json

        $parsed.Id | Should -Be '0.0.0.0:15000'
        $parsed.ListenAddress | Should -Be '0.0.0.0'
        $parsed.ListenPort | Should -Be 15000
        $parsed.ConnectAddress | Should -Be '172.30.98.222'
        $parsed.ConnectPort | Should -Be 50000
        $parsed.FirewallRulePresent | Should -BeTrue
        $parsed.FirewallRuleName | Should -Be 'hyperv-pp-0.0.0.0-15000'
        $parsed.FirewallRuleProfile | Should -Be 'Any'
    }

    It 'reports FirewallRulePresent=false when the firewall rule is missing' {
        Mock Get-ItemProperty { New-HypervPortProxyRegistryItem }
        Mock Get-NetFirewallRule { }

        $parsed = Get-HypervPortProxy -ListenAddress '0.0.0.0' -ListenPort 15000 `
            -FirewallName 'hyperv-pp-0.0.0.0-15000' | ConvertFrom-Json

        $parsed.FirewallRulePresent | Should -BeFalse
        $parsed.FirewallRuleProfile | Should -Be ''
    }

    It 'reports FirewallRulePresent=false when the firewall rule is disabled' {
        Mock Get-ItemProperty { New-HypervPortProxyRegistryItem }
        Mock Get-NetFirewallRule { New-HypervFirewallRuleSample -Enabled 'False' }

        $parsed = Get-HypervPortProxy -ListenAddress '0.0.0.0' -ListenPort 15000 `
            -FirewallName 'hyperv-pp-0.0.0.0-15000' | ConvertFrom-Json

        $parsed.FirewallRulePresent | Should -BeFalse
    }

    It 'throws ObjectNotFound when no rule exists' {
        Mock Get-ItemProperty { }

        $err = $null
        try {
            Get-HypervPortProxy -ListenAddress '0.0.0.0' -ListenPort 15000 -FirewallName 'x'
        }
        catch { $err = $_ }

        $err | Should -Not -BeNullOrEmpty
        $err.CategoryInfo.Category | Should -Be 'ObjectNotFound'
        $err.FullyQualifiedErrorId | Should -BeLike 'PortProxyNotFound*'
    }
}
