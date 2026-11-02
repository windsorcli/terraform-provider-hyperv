# Locks the firewall_rule read, reconcile, and remove helpers both NAT and port proxy scripts call.

BeforeAll {
    function Get-NetFirewallRule {
        [CmdletBinding()]
        param([string] $DisplayName)
    }
    function New-NetFirewallRule {
        [CmdletBinding()]
        param(
            [string] $DisplayName,
            [string] $Direction,
            [string] $Action,
            [string] $Protocol,
            [string] $LocalAddress,
            [int]    $LocalPort,
            [string] $Profile
        )
    }
    function Set-NetFirewallRule {
        [CmdletBinding()]
        param(
            [string] $DisplayName,
            [string] $Enabled,
            [string] $Profile,
            [string] $LocalAddress
        )
    }
    function Remove-NetFirewallRule {
        [CmdletBinding()]
        param([string] $DisplayName)
    }

    . $PSScriptRoot/preamble.ps1
    . $PSScriptRoot/firewall-rule.ps1

    $script:SyncArgs = @{
        Name            = 'hyperv-pf-tcp-8080'
        Protocol        = 'TCP'
        LocalPort       = 8080
        FirewallProfile = 'Any'
    }
}

Describe 'Get-HypervFirewallRuleState' {

    It 'reports an enabled rule as present with its named profile' {
        Mock Get-NetFirewallRule { [pscustomobject]@{ Enabled = 'True'; Profile = 'Domain, Private' } }

        $state = Get-HypervFirewallRuleState -Name 'r'

        $state.Present | Should -BeTrue
        $state.Profile | Should -Be 'Domain, Private'
    }

    It 'reports a disabled rule as absent but keeps its profile' {
        Mock Get-NetFirewallRule { [pscustomobject]@{ Enabled = 'False'; Profile = 'Public' } }

        $state = Get-HypervFirewallRuleState -Name 'r'

        $state.Present | Should -BeFalse
        $state.Profile | Should -Be 'Public'
    }

    It 'reports a missing rule as absent with an empty profile' {
        Mock Get-NetFirewallRule { }

        $state = Get-HypervFirewallRuleState -Name 'r'

        $state.Present | Should -BeFalse
        $state.Profile | Should -Be ''
    }
}

Describe 'Sync-HypervFirewallRule' {

    BeforeEach {
        Mock Set-NetFirewallRule { }
        Mock New-NetFirewallRule { }
    }

    It 'enables an existing rule with the string enum value' {
        Mock Get-NetFirewallRule { [pscustomobject]@{ Enabled = 'False'; Profile = 'Any' } }

        Sync-HypervFirewallRule -Enabled $true @script:SyncArgs

        Should -Invoke Set-NetFirewallRule -Times 1 -Exactly -ParameterFilter {
            $DisplayName -eq 'hyperv-pf-tcp-8080' -and $Enabled -eq 'True' -and $Profile -eq 'Any' -and -not $LocalAddress
        }
        Should -Invoke New-NetFirewallRule -Times 0 -Exactly
    }

    It 'disables an existing rule instead of deleting it' {
        Mock Get-NetFirewallRule { [pscustomobject]@{ Enabled = 'True'; Profile = 'Any' } }

        Sync-HypervFirewallRule -Enabled $false @script:SyncArgs

        Should -Invoke Set-NetFirewallRule -Times 1 -Exactly -ParameterFilter { $Enabled -eq 'False' }
    }

    It 'passes LocalAddress to Set-NetFirewallRule when given' {
        Mock Get-NetFirewallRule { [pscustomobject]@{ Enabled = 'True'; Profile = 'Any' } }

        Sync-HypervFirewallRule -Enabled $true -LocalAddress '192.168.3.77' @script:SyncArgs

        Should -Invoke Set-NetFirewallRule -Times 1 -Exactly -ParameterFilter { $LocalAddress -eq '192.168.3.77' }
    }

    It 'recreates a rule deleted out of band when enabled' {
        Mock Get-NetFirewallRule { }

        Sync-HypervFirewallRule -Enabled $true -LocalAddress 'Any' @script:SyncArgs

        Should -Invoke New-NetFirewallRule -Times 1 -Exactly -ParameterFilter {
            $DisplayName -eq 'hyperv-pf-tcp-8080' -and $Direction -eq 'Inbound' -and $Action -eq 'Allow' -and
            $Protocol -eq 'TCP' -and $LocalPort -eq 8080 -and $Profile -eq 'Any' -and $LocalAddress -eq 'Any'
        }
        Should -Invoke Set-NetFirewallRule -Times 0 -Exactly
    }

    It 'does nothing when the rule is missing and disabled' {
        Mock Get-NetFirewallRule { }

        Sync-HypervFirewallRule -Enabled $false @script:SyncArgs

        Should -Invoke New-NetFirewallRule -Times 0 -Exactly
        Should -Invoke Set-NetFirewallRule -Times 0 -Exactly
    }
}

Describe 'New-HypervFirewallRule' {

    It 'omits LocalAddress when the caller passes none' {
        Mock New-NetFirewallRule { }

        New-HypervFirewallRule -Name 'r' -Protocol 'UDP' -LocalPort 53 -FirewallProfile 'Private'

        Should -Invoke New-NetFirewallRule -Times 1 -Exactly -ParameterFilter {
            $Protocol -eq 'UDP' -and $LocalPort -eq 53 -and $Profile -eq 'Private' -and
            -not $PSBoundParameters.ContainsKey('LocalAddress')
        }
    }
}

Describe 'Remove-HypervFirewallRule' {

    BeforeEach {
        Mock Remove-NetFirewallRule { }
    }

    It 'removes an existing rule' {
        Mock Get-NetFirewallRule { [pscustomobject]@{ Enabled = 'True'; Profile = 'Any' } }

        Remove-HypervFirewallRule -Name 'r'

        Should -Invoke Remove-NetFirewallRule -Times 1 -Exactly -ParameterFilter { $DisplayName -eq 'r' }
    }

    It 'skips a rule that is already gone' {
        Mock Get-NetFirewallRule { }

        Remove-HypervFirewallRule -Name 'r'

        Should -Invoke Remove-NetFirewallRule -Times 0 -Exactly
    }
}
