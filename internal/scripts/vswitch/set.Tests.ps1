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

        It 'Private + AllowManagementOS rejects with a clear error before reaching the cmdlet (symmetric with new.ps1)' {
            Mock Set-VMSwitch { }
            Mock Get-VMSwitch { New-HypervSwitchSample -SwitchType 'Private' }

            { Set-HypervSwitch -Name 'priv0' -SwitchType 'Private' -AllowManagementOS $true } |
                Should -Throw -ExpectedMessage '*allow_management_os is not valid for switch_type ''Private''*'

            # Get-VMSwitch fires once for the existence pre-check; Set-VMSwitch
            # never runs because the guard rejects after pre-check.
            Should -Invoke Set-VMSwitch -Times 0 -Exactly
            Should -Invoke Get-VMSwitch -Times 1 -Exactly
        }

        It 'throws ObjectNotFound when the switch is missing (skips Set-VMSwitch, symmetric with remove.ps1)' {
            # Asserts on CategoryInfo.Category because that's what the Go side
            # maps to ErrNotFound. ErrorId drift wouldn't change behavior; a
            # category drift would silently mis-route the typed error.
            Mock Set-VMSwitch { }
            Mock Get-VMSwitch { $null }

            $captured = $null
            try { Set-HypervSwitch -Name 'missing' -AllowManagementOS $true } catch { $captured = $_ }

            $captured | Should -Not -BeNullOrEmpty
            $captured.CategoryInfo.Category.ToString() | Should -Be 'ObjectNotFound'
            $captured.FullyQualifiedErrorId | Should -Match 'VMSwitchNotFound'
            Should -Invoke Set-VMSwitch -Times 0 -Exactly
        }

        It 'External + AllowManagementOS is allowed (no false positive on the Private guard)' {
            Mock Set-VMSwitch { }
            Mock Get-VMSwitch { New-HypervSwitchSample -SwitchType 'External' -AllowManagementOS $false }

            Set-HypervSwitch -Name 'ext0' -SwitchType 'External' -AllowManagementOS $false | Out-Null

            Should -Invoke Set-VMSwitch -Times 1 -Exactly -ParameterFilter {
                $null -ne $AllowManagementOS -and $AllowManagementOS -eq $false
            }
        }

        It 'omits SwitchType from the Set-VMSwitch splat (it is validation-only, not mutable)' {
            Mock Set-VMSwitch { }
            Mock Get-VMSwitch { New-HypervSwitchSample -SwitchType 'External' }

            Set-HypervSwitch -Name 'ext0' -SwitchType 'External' -AllowManagementOS $true | Out-Null

            Should -Invoke Set-VMSwitch -Times 1 -Exactly -ParameterFilter {
                $null -eq $SwitchType
            }
        }

        It 'rejects a no-mutable-fields call with a clear error before reaching the cmdlet' {
            Mock Set-VMSwitch { }
            Mock Get-VMSwitch { New-HypervSwitchSample }

            { Set-HypervSwitch -Name 'sw0' } |
                Should -Throw -ExpectedMessage '*requires at least one mutable attribute*'

            # Get-VMSwitch fires once for the existence pre-check; Set-VMSwitch
            # never runs because the count guard rejects after pre-check.
            Should -Invoke Set-VMSwitch -Times 0 -Exactly
            Should -Invoke Get-VMSwitch -Times 1 -Exactly
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

            # Get-VMSwitch is called twice: once for the existence pre-check,
            # once for the post-mutation read-back. Both target the same name.
            Should -Invoke Get-VMSwitch -Times 2 -Exactly -ParameterFilter {
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
