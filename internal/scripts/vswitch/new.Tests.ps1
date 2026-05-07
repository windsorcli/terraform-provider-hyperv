# Locks the JSON contract for New-HypervSwitch -- both the input-side splat
# logic (which JSON keys map to which New-VMSwitch parameters) and the
# output-side read shape that round-trips through Get-HypervSwitch.

BeforeAll {
    . $PSScriptRoot/_test_helpers.ps1
    . $PSScriptRoot/../common/preamble.ps1
    . $PSScriptRoot/new.ps1
}

Describe 'New-HypervSwitch' {

    Context 'External switch' {

        It 'passes -NetAdapterName and skips -SwitchType' {
            Mock New-VMSwitch { New-HypervSwitchSample -Name $Name -SwitchType 'External' }
            New-HypervSwitch -Name 'ext0' -SwitchType 'External' -NetAdapterNames @('NIC1') | Out-Null

            Should -Invoke New-VMSwitch -Times 1 -Exactly -ParameterFilter {
                $Name -eq 'ext0' -and
                $NetAdapterName -contains 'NIC1' -and
                $null -eq $SwitchType
            }
        }

        It 'passes through multiple NIC names' {
            Mock New-VMSwitch { New-HypervSwitchSample }
            New-HypervSwitch -Name 'ext0' -SwitchType 'External' `
                -NetAdapterNames @('NIC1', 'NIC2') | Out-Null

            Should -Invoke New-VMSwitch -Times 1 -Exactly -ParameterFilter {
                $NetAdapterName.Count -eq 2 -and
                $NetAdapterName[0] -eq 'NIC1' -and
                $NetAdapterName[1] -eq 'NIC2'
            }
        }
    }

    Context 'Internal and Private switches' {

        It 'Internal: passes -SwitchType Internal and skips -NetAdapterName' {
            Mock New-VMSwitch { New-HypervSwitchSample -SwitchType 'Internal' }
            New-HypervSwitch -Name 'int0' -SwitchType 'Internal' | Out-Null

            Should -Invoke New-VMSwitch -Times 1 -Exactly -ParameterFilter {
                $Name -eq 'int0' -and
                $SwitchType -eq 'Internal' -and
                $null -eq $NetAdapterName
            }
        }

        It 'Private: passes -SwitchType Private' {
            Mock New-VMSwitch { New-HypervSwitchSample -SwitchType 'Private' }
            New-HypervSwitch -Name 'priv0' -SwitchType 'Private' | Out-Null

            Should -Invoke New-VMSwitch -Times 1 -Exactly -ParameterFilter {
                $SwitchType -eq 'Private' -and $null -eq $NetAdapterName
            }
        }

        It 'Private + AllowManagementOS rejects with a clear error before reaching the cmdlet' {
            Mock New-VMSwitch { New-HypervSwitchSample -SwitchType 'Private' }

            { New-HypervSwitch -Name 'priv0' -SwitchType 'Private' -AllowManagementOS $true } |
                Should -Throw -ExpectedMessage '*allow_management_os is not valid for switch_type ''Private''*'

            Should -Invoke New-VMSwitch -Times 0 -Exactly
        }

        It 'Internal + AllowManagementOS rejects (External-only flag; lives only on New-VMSwitch''s NetAdapter parameter sets)' {
            # Regression: prior to the fix the gate was `$SwitchType -ne 'Private'`,
            # which let Internal through and forwarded -AllowManagementOS to the
            # SwitchType parameter set -- ambiguous, so PowerShell errored with
            # "Parameter set cannot be resolved using the specified named
            # parameters." Pester didn't catch it because New-VMSwitch is mocked
            # here; the bug only surfaced live on the bench.
            Mock New-VMSwitch { New-HypervSwitchSample -SwitchType 'Internal' }

            { New-HypervSwitch -Name 'int0' -SwitchType 'Internal' -AllowManagementOS $true } |
                Should -Throw -ExpectedMessage '*allow_management_os is not valid for switch_type ''Internal''*'

            Should -Invoke New-VMSwitch -Times 0 -Exactly
        }
    }

    Context 'optional parameters' {

        It 'omits -AllowManagementOS when not specified (cmdlet default applies)' {
            Mock New-VMSwitch { New-HypervSwitchSample }
            New-HypervSwitch -Name 'ext0' -SwitchType 'External' -NetAdapterNames @('NIC1') | Out-Null

            Should -Invoke New-VMSwitch -Times 1 -Exactly -ParameterFilter {
                $null -eq $AllowManagementOS
            }
        }

        It 'forwards -AllowManagementOS=$false when explicitly false' {
            Mock New-VMSwitch { New-HypervSwitchSample -AllowManagementOS $false }
            New-HypervSwitch -Name 'ext0' -SwitchType 'External' `
                -NetAdapterNames @('NIC1') -AllowManagementOS $false | Out-Null

            Should -Invoke New-VMSwitch -Times 1 -Exactly -ParameterFilter {
                $null -ne $AllowManagementOS -and $AllowManagementOS -eq $false
            }
        }

        It 'omits -Notes when not specified' {
            Mock New-VMSwitch { New-HypervSwitchSample }
            New-HypervSwitch -Name 'ext0' -SwitchType 'External' -NetAdapterNames @('NIC1') | Out-Null

            Should -Invoke New-VMSwitch -Times 1 -Exactly -ParameterFilter {
                $null -eq $Notes
            }
        }

        It 'forwards -Notes when specified (including empty string)' {
            Mock New-VMSwitch { New-HypervSwitchSample -Notes '' }
            New-HypervSwitch -Name 'ext0' -SwitchType 'External' `
                -NetAdapterNames @('NIC1') -Notes '' | Out-Null

            Should -Invoke New-VMSwitch -Times 1 -Exactly -ParameterFilter {
                $null -ne $Notes -and $Notes -eq ''
            }
        }
    }

    Context 'output shape' {

        It 'emits the same six-field shape as Get-HypervSwitch' {
            Mock New-VMSwitch { New-HypervSwitchSample }
            $parsed = (New-HypervSwitch -Name 'ext0' -SwitchType 'External' `
                -NetAdapterNames @('NIC1')) | ConvertFrom-Json

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
}
