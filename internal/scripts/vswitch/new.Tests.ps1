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

        It 'emits the canonical nine-field shape (six base + three NAT) as Get-HypervSwitch' {
            Mock New-VMSwitch { New-HypervSwitchSample }
            $parsed = (New-HypervSwitch -Name 'ext0' -SwitchType 'External' `
                -NetAdapterNames @('NIC1')) | ConvertFrom-Json

            # NAT fields are always present in the output. Non-NAT switches
            # emit them as empty strings so the wire shape is constant
            # across switch types -- the typed client decodes the same
            # struct regardless of branch.
            $parsed.PSObject.Properties.Name | Sort-Object | Should -Be @(
                'AllowManagementOS',
                'Id',
                'Name',
                'NatHostAddress',
                'NatInternalAddressPrefix',
                'NatName',
                'NetAdapterInterfaceDescription',
                'Notes',
                'SwitchType'
            )
        }

        It 'NAT-empty fields are empty strings for non-NAT switches (round-trip via JSON, not null)' {
            Mock New-VMSwitch { New-HypervSwitchSample }
            $parsed = (New-HypervSwitch -Name 'ext0' -SwitchType 'External' `
                -NetAdapterNames @('NIC1')) | ConvertFrom-Json

            $parsed.NatName | Should -Be ''
            $parsed.NatInternalAddressPrefix | Should -Be ''
            $parsed.NatHostAddress | Should -Be ''
        }
    }

    Context 'NAT switch' {
        # NAT is an Internal VMSwitch + a New-NetIPAddress on the host vNIC +
        # a New-NetNat tying the prefix to that vNIC. The script orchestrates
        # all three; tests pin the cmdlet sequence and the singleton guard.

        It 'creates an Internal VMSwitch and provisions NetIPAddress + NetNat' {
            Mock Get-NetNat { }  # no existing NetNat -- fresh host
            Mock New-VMSwitch { New-HypervSwitchSample -Name $Name -SwitchType 'Internal' `
                -AllowManagementOS $false -NetAdapterInterfaceDescription '' }
            Mock New-NetIPAddress { New-HypervNetIPAddressSample }
            Mock New-NetNat { New-HypervNetNatSample }

            New-HypervSwitch -Name 'windsor-nat' -SwitchType 'NAT' `
                -NatName 'windsor-nat' `
                -NatInternalAddressPrefix '192.168.100.0/24' `
                -NatHostAddress '192.168.100.1' | Out-Null

            Should -Invoke New-VMSwitch -Times 1 -Exactly -ParameterFilter {
                $Name -eq 'windsor-nat' -and
                $SwitchType -eq 'Internal' -and
                $null -eq $NetAdapterName
            }
            Should -Invoke New-NetIPAddress -Times 1 -Exactly -ParameterFilter {
                $InterfaceAlias -eq 'vEthernet (windsor-nat)' -and
                $IPAddress -eq '192.168.100.1' -and
                $PrefixLength -eq 24
            }
            Should -Invoke New-NetNat -Times 1 -Exactly -ParameterFilter {
                $Name -eq 'windsor-nat' -and
                $InternalIPInterfaceAddressPrefix -eq '192.168.100.0/24'
            }
        }

        It 'emits SwitchType=NAT and populates all NAT fields in the output' {
            Mock Get-NetNat { }
            Mock New-VMSwitch { New-HypervSwitchSample -Name $Name -SwitchType 'Internal' `
                -AllowManagementOS $false -NetAdapterInterfaceDescription '' }
            Mock New-NetIPAddress { New-HypervNetIPAddressSample }
            Mock New-NetNat { New-HypervNetNatSample }

            $parsed = New-HypervSwitch -Name 'windsor-nat' -SwitchType 'NAT' `
                -NatName 'windsor-nat' `
                -NatInternalAddressPrefix '192.168.100.0/24' `
                -NatHostAddress '192.168.100.1' | ConvertFrom-Json

            $parsed.SwitchType | Should -Be 'NAT'
            $parsed.NatName | Should -Be 'windsor-nat'
            $parsed.NatInternalAddressPrefix | Should -Be '192.168.100.0/24'
            $parsed.NatHostAddress | Should -Be '192.168.100.1'
            $parsed.AllowManagementOS | Should -BeFalse
        }

        It 'rejects when a NetNat with a different name already exists (one-NAT-per-host)' {
            # Singleton: Windows allows exactly one NetNat instance per host.
            # If one already exists with a different name, fail before
            # creating the VMSwitch so we don't leave a half-provisioned
            # NAT-less Internal switch on the host.
            Mock Get-NetNat { New-HypervNetNatSample -Name 'someone-elses-nat' }
            Mock New-VMSwitch { New-HypervSwitchSample }
            Mock New-NetIPAddress { New-HypervNetIPAddressSample }
            Mock New-NetNat { New-HypervNetNatSample }

            { New-HypervSwitch -Name 'windsor-nat' -SwitchType 'NAT' `
                -NatName 'windsor-nat' `
                -NatInternalAddressPrefix '192.168.100.0/24' `
                -NatHostAddress '192.168.100.1' } |
                Should -Throw -ExpectedMessage '*someone-elses-nat*'

            Should -Invoke New-VMSwitch -Times 0 -Exactly
            Should -Invoke New-NetIPAddress -Times 0 -Exactly
            Should -Invoke New-NetNat -Times 0 -Exactly
        }

        It 'adopts a same-named pre-existing NetNat (idempotent re-apply path)' {
            # If a NetNat with our planned name already exists, treat it as
            # ours and skip New-NetNat. The VMSwitch + NetIPAddress still
            # need to be created (they may have been torn down).
            Mock Get-NetNat { New-HypervNetNatSample -Name 'windsor-nat' `
                -InternalIPInterfaceAddressPrefix '192.168.100.0/24' }
            Mock New-VMSwitch { New-HypervSwitchSample -Name $Name -SwitchType 'Internal' `
                -AllowManagementOS $false -NetAdapterInterfaceDescription '' }
            Mock New-NetIPAddress { New-HypervNetIPAddressSample }
            Mock New-NetNat { New-HypervNetNatSample }

            New-HypervSwitch -Name 'windsor-nat' -SwitchType 'NAT' `
                -NatName 'windsor-nat' `
                -NatInternalAddressPrefix '192.168.100.0/24' `
                -NatHostAddress '192.168.100.1' | Out-Null

            Should -Invoke New-VMSwitch -Times 1 -Exactly
            Should -Invoke New-NetIPAddress -Times 1 -Exactly
            Should -Invoke New-NetNat -Times 0 -Exactly
        }
    }
}
