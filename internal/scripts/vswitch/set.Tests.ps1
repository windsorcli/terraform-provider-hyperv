# Locks the partial-update semantics of Set-HypervSwitch -- only the keys
# present in the input get forwarded to Set-VMSwitch -- and confirms the
# read-back shape matches Get-HypervSwitch exactly.

BeforeAll {
    . $PSScriptRoot/_test_helpers.ps1
    . $PSScriptRoot/../common/preamble.ps1
    . $PSScriptRoot/set.ps1
}

Describe 'Set-HypervSwitch' {

    Context 'partial updates' {

        It 'forwards only -AllowManagementOS when only that is specified' {
            Mock Set-VMSwitch { }
            Mock Get-VMSwitch { New-HypervSwitchSample -Name $Name }

            Set-HypervSwitch -Name 'sw0' -AllowManagementOS $false | Out-Null

            Should -Invoke Set-VMSwitch -Times 1 -Exactly -ParameterFilter {
                $Name -eq 'sw0' -and
                $null -ne $AllowManagementOS -and $AllowManagementOS -eq $false -and
                $null -eq $NetAdapterName -and
                $null -eq $Notes
            }
        }

        It 'forwards only -NetAdapterName when only adapters are specified' {
            Mock Set-VMSwitch { }
            Mock Get-VMSwitch { New-HypervSwitchSample -Name $Name }

            Set-HypervSwitch -Name 'sw0' -NetAdapterNames @('NIC2') | Out-Null

            Should -Invoke Set-VMSwitch -Times 1 -Exactly -ParameterFilter {
                $NetAdapterName -contains 'NIC2' -and
                $null -eq $AllowManagementOS -and
                $null -eq $Notes
            }
        }

        It 'forwards -Notes including empty string (clear semantics)' {
            Mock Set-VMSwitch { }
            Mock Get-VMSwitch { New-HypervSwitchSample -Name $Name }

            Set-HypervSwitch -Name 'sw0' -Notes '' | Out-Null

            Should -Invoke Set-VMSwitch -Times 1 -Exactly -ParameterFilter {
                $null -ne $Notes -and $Notes -eq ''
            }
        }

        It 'forwards all three when all three are specified' {
            Mock Set-VMSwitch { }
            Mock Get-VMSwitch { New-HypervSwitchSample -Name $Name }

            Set-HypervSwitch -Name 'sw0' `
                -NetAdapterNames @('NIC2') `
                -AllowManagementOS $true `
                -Notes 'production' | Out-Null

            Should -Invoke Set-VMSwitch -Times 1 -Exactly -ParameterFilter {
                $NetAdapterName -contains 'NIC2' -and
                $AllowManagementOS -eq $true -and
                $Notes -eq 'production'
            }
        }
    }

    Context 'read-back' {

        It 'follows Set-VMSwitch with a Get-VMSwitch by the same name' {
            Mock Set-VMSwitch { }
            Mock Get-VMSwitch { New-HypervSwitchSample -Name $Name }

            Set-HypervSwitch -Name 'sw0' -AllowManagementOS $true | Out-Null

            Should -Invoke Get-VMSwitch -Times 1 -Exactly -ParameterFilter {
                $Name -eq 'sw0'
            }
        }

        It 'emits the same six-field read shape as get.ps1' {
            Mock Set-VMSwitch { }
            Mock Get-VMSwitch { New-HypervSwitchSample -Name $Name -SwitchType 'External' }

            $parsed = Set-HypervSwitch -Name 'sw0' -AllowManagementOS $true | ConvertFrom-Json

            $parsed.PSObject.Properties.Name | Sort-Object | Should -Be @(
                'AllowManagementOS',
                'Id',
                'Name',
                'NetAdapterInterfaceDescription',
                'Notes',
                'SwitchType'
            )
        }
    }

    Context 'error propagation' {

        It 'lets Set-VMSwitch terminating errors propagate' {
            Mock Set-VMSwitch {
                $exception = [System.InvalidOperationException]::new('vmms not running')
                $errorRecord = [System.Management.Automation.ErrorRecord]::new(
                    $exception, 'VmmsUnavailable',
                    [System.Management.Automation.ErrorCategory]::ResourceUnavailable,
                    'sw0')
                throw $errorRecord
            }
            Mock Get-VMSwitch { New-HypervSwitchSample }

            { Set-HypervSwitch -Name 'sw0' -AllowManagementOS $true } |
                Should -Throw -ErrorId 'VmmsUnavailable'
        }
    }
}
