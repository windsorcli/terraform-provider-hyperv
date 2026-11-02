# Locks New-HypervPortProxy's preconditions, netsh arguments, and rollback paths.

BeforeAll {
    . $PSScriptRoot/_test_helpers.ps1
    . $PSScriptRoot/../common/preamble.ps1
    . $PSScriptRoot/_rule.ps1
    . $PSScriptRoot/new.ps1

    $script:NewArgs = @{
        ListenAddress   = '0.0.0.0'
        ListenPort      = 15000
        ConnectAddress  = '172.30.98.222'
        ConnectPort     = 50000
        FirewallEnabled = $true
        FirewallName    = 'hyperv-pp-0.0.0.0-15000'
        FirewallProfile = 'Any'
    }
}

Describe 'New-HypervPortProxy' {

    BeforeEach {
        $script:ruleAdded = $false
        Mock Get-CimInstance { New-HypervIpHelperService }
        Mock Get-ItemProperty { if ($script:ruleAdded) { New-HypervPortProxyRegistryItem } }
        Mock Invoke-HypervNetsh { $script:HypervExcludedRangeText } -ParameterFilter { $ArgumentList -contains 'excludedportrange' }
        Mock Invoke-HypervNetsh { $script:ruleAdded = $true } -ParameterFilter { $ArgumentList -contains 'add' }
        Mock Invoke-HypervNetsh { $script:ruleAdded = $false } -ParameterFilter { $ArgumentList -contains 'delete' }
        Mock Get-NetTCPConnection { New-HypervListenerSample }
        Mock New-NetFirewallRule { }
        Mock Get-NetFirewallRule { New-HypervFirewallRuleSample }
        Mock Start-Sleep { }
    }

    Context 'happy path' {

        It 'adds the v4tov4 rule with the listen and connect pairs' {
            New-HypervPortProxy @script:NewArgs | Out-Null

            Should -Invoke Invoke-HypervNetsh -Times 1 -ParameterFilter {
                ($ArgumentList -join ' ') -eq ('interface portproxy add v4tov4 listenaddress=0.0.0.0 listenport=15000 ' +
                    'connectaddress=172.30.98.222 connectport=50000')
            }
        }

        It 'creates an inbound TCP firewall rule on the listen port' {
            New-HypervPortProxy @script:NewArgs | Out-Null

            Should -Invoke New-NetFirewallRule -Times 1 -ParameterFilter {
                $DisplayName -eq 'hyperv-pp-0.0.0.0-15000' -and $Direction -eq 'Inbound' -and
                $Protocol -eq 'TCP' -and $LocalPort -eq 15000 -and $Profile -eq 'Any'
            }
        }

        It 'skips the firewall rule when disabled' {
            $in = $script:NewArgs.Clone()
            $in.FirewallEnabled = $false
            Mock Get-NetFirewallRule { }

            $parsed = New-HypervPortProxy @in | ConvertFrom-Json

            Should -Invoke New-NetFirewallRule -Times 0
            $parsed.FirewallRulePresent | Should -BeFalse
        }

        It 'emits the read result from the registry after the add' {
            $parsed = New-HypervPortProxy @script:NewArgs | ConvertFrom-Json

            $parsed.Id | Should -Be '0.0.0.0:15000'
            $parsed.ConnectAddress | Should -Be '172.30.98.222'
            $parsed.FirewallRulePresent | Should -BeTrue
        }
    }

    Context 'preconditions' {

        It 'refuses to overwrite an existing rule' {
            Mock Get-ItemProperty { New-HypervPortProxyRegistryItem -ConnectAddress '10.0.0.5' }

            { New-HypervPortProxy @script:NewArgs } | Should -Throw '*already exists*'
            Should -Invoke Invoke-HypervNetsh -Times 0 -ParameterFilter { $ArgumentList -contains 'add' }
        }

        It 'rejects a port inside an exclusion range before adding' {
            $in = $script:NewArgs.Clone()
            $in.ListenPort = 50123

            { New-HypervPortProxy @in } | Should -Throw '*exclusion range 50074-50173*'
            Should -Invoke Invoke-HypervNetsh -Times 0 -ParameterFilter { $ArgumentList -contains 'add' }
        }

        It 'fails before adding when iphlpsvc is stopped' {
            Mock Get-CimInstance { New-HypervIpHelperService -State 'Stopped' -ProcessId 0 }

            { New-HypervPortProxy @script:NewArgs } | Should -Throw '*iphlpsvc*'
            Should -Invoke Invoke-HypervNetsh -Times 0 -ParameterFilter { $ArgumentList -contains 'add' }
        }
    }

    Context 'rollback' {

        It 'deletes the rule when the listener never binds' {
            Mock Get-NetTCPConnection { }

            { New-HypervPortProxy @script:NewArgs } | Should -Throw '*isn''t listening*'
            Should -Invoke Invoke-HypervNetsh -Times 1 -ParameterFilter {
                ($ArgumentList -join ' ') -eq 'interface portproxy delete v4tov4 listenaddress=0.0.0.0 listenport=15000'
            }
        }

        It 'deletes the rule and rethrows when the firewall rule fails' {
            Mock New-NetFirewallRule { throw 'firewall boom' }

            { New-HypervPortProxy @script:NewArgs } | Should -Throw '*firewall boom*'
            Should -Invoke Invoke-HypervNetsh -Times 1 -ParameterFilter { $ArgumentList -contains 'delete' }
        }
    }
}
