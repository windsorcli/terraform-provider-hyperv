# Locks Set-HypervPortProxy's retarget and firewall reconciliation.

BeforeAll {
    . $PSScriptRoot/_test_helpers.ps1
    . $PSScriptRoot/../common/preamble.ps1
    . $PSScriptRoot/_rule.ps1
    . $PSScriptRoot/set.ps1

    $script:SetArgs = @{
        ListenAddress   = '0.0.0.0'
        ListenPort      = 15000
        ConnectAddress  = '172.30.98.177'
        ConnectPort     = 50000
        FirewallEnabled = $true
        FirewallName    = 'hyperv-pp-0.0.0.0-15000'
        FirewallProfile = 'Any'
    }
}

Describe 'Set-HypervPortProxy' {

    BeforeEach {
        $script:connect = '172.30.98.222'
        Mock Get-ItemProperty { New-HypervPortProxyRegistryItem -ConnectAddress $script:connect }
        Mock Invoke-HypervNetsh { $script:connect = '172.30.98.177' }
        Mock Get-NetFirewallRule { New-HypervFirewallRuleSample }
        Mock Set-NetFirewallRule { }
        Mock New-NetFirewallRule { }
    }

    It 'retargets the rule with netsh set' {
        $parsed = Set-HypervPortProxy @script:SetArgs | ConvertFrom-Json

        Should -Invoke Invoke-HypervNetsh -Times 1 -ParameterFilter {
            ($ArgumentList -join ' ') -eq ('interface portproxy set v4tov4 listenaddress=0.0.0.0 listenport=15000 ' +
                'connectaddress=172.30.98.177 connectport=50000')
        }
        $parsed.ConnectAddress | Should -Be '172.30.98.177'
    }

    It 'skips netsh when the connect pair already matches' {
        $script:connect = '172.30.98.177'

        Set-HypervPortProxy @script:SetArgs | Out-Null

        Should -Invoke Invoke-HypervNetsh -Times 0
    }

    It 'updates an existing firewall rule with the enum string form of -Enabled' {
        $in = $script:SetArgs.Clone()
        $in.FirewallEnabled = $false

        Set-HypervPortProxy @in | Out-Null

        Should -Invoke Set-NetFirewallRule -Times 1 -ParameterFilter { $Enabled -eq 'False' -and $Profile -eq 'Any' }
    }

    It 'recreates a firewall rule deleted out of band' {
        Mock Get-NetFirewallRule { }

        Set-HypervPortProxy @script:SetArgs | Out-Null

        Should -Invoke New-NetFirewallRule -Times 1 -ParameterFilter { $LocalPort -eq 15000 -and $Protocol -eq 'TCP' }
    }

    It 'throws ObjectNotFound when the rule is gone' {
        Mock Get-ItemProperty { }

        $err = $null
        try { Set-HypervPortProxy @script:SetArgs } catch { $err = $_ }

        $err.CategoryInfo.Category | Should -Be 'ObjectNotFound'
        Should -Invoke Invoke-HypervNetsh -Times 0
    }
}
