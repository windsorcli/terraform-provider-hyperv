# Locks the JSON contract for Get-HypervSwitch. The Go-side typed wrapper
# (PR4) decodes the output with field tags that match the keys asserted here;
# any change to those keys or types is a wire-level break.

BeforeAll {
    . $PSScriptRoot/_test_helpers.ps1
    . $PSScriptRoot/../common/preamble.ps1
    . $PSScriptRoot/get.ps1
}

Describe 'Get-HypervSwitch' {

    Context 'happy path' {

        It 'emits the canonical nine-field shape (six base + three NAT)' {
            # The wire shape is constant across switch types: NAT fields
            # are always present, populated only when SwitchType=NAT and
            # nat_name is supplied, empty strings otherwise. Keeps the
            # typed-client decode path branch-free.
            Mock Get-VMSwitch { New-HypervSwitchSample -Name 'sw0' -SwitchType 'External' }
            $parsed = Get-HypervSwitch -Name 'sw0' | ConvertFrom-Json

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

        It 'emits empty NAT fields for non-NAT switches' {
            Mock Get-VMSwitch { New-HypervSwitchSample -Name 'sw0' -SwitchType 'Private' }
            $parsed = Get-HypervSwitch -Name 'sw0' | ConvertFrom-Json
            $parsed.NatName | Should -Be ''
            $parsed.NatInternalAddressPrefix | Should -Be ''
            $parsed.NatHostAddress | Should -Be ''
        }

        It 'stringifies the SwitchType enum' {
            Mock Get-VMSwitch { New-HypervSwitchSample -SwitchType 'Internal' }
            $parsed = Get-HypervSwitch -Name 'sw0' | ConvertFrom-Json
            $parsed.SwitchType | Should -BeOfType [string]
            $parsed.SwitchType | Should -Be 'Internal'
        }

        It 'stringifies the Id Guid' {
            Mock Get-VMSwitch { New-HypervSwitchSample -Id 'aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee' }
            $parsed = Get-HypervSwitch -Name 'sw0' | ConvertFrom-Json
            $parsed.Id | Should -Be 'aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee'
        }

        It 'preserves AllowManagementOS as a bool' {
            Mock Get-VMSwitch { New-HypervSwitchSample -AllowManagementOS $false }
            $parsed = Get-HypervSwitch -Name 'sw0' | ConvertFrom-Json
            $parsed.AllowManagementOS | Should -BeFalse
        }

        It 'forwards -Name to Get-VMSwitch verbatim' {
            Mock Get-VMSwitch { New-HypervSwitchSample -Name 'lookup-target' }
            Get-HypervSwitch -Name 'lookup-target' | Out-Null
            Should -Invoke Get-VMSwitch -Times 1 -Exactly `
                -ParameterFilter { $Name -eq 'lookup-target' }
        }

        It 'compresses output to a single line (Write-HypervResult contract)' {
            Mock Get-VMSwitch { New-HypervSwitchSample }
            $output = Get-HypervSwitch -Name 'sw0'
            $output | Should -BeOfType [string]
            $output -split "`n" | Measure-Object | Select-Object -ExpandProperty Count | Should -Be 1
        }
    }

    Context 'error propagation' {

        It 'throws ObjectNotFound when the switch is missing' {
            # The Go side keys on CategoryInfo.Category (the wire envelope's
            # `category` field), so assert on Category, not just ErrorId --
            # ErrorId drifting wouldn't break the typed-error mapping but a
            # category drift would silently mis-route ErrNotFound.
            Mock Get-VMSwitch { $null }

            $captured = $null
            try { Get-HypervSwitch -Name 'missing' } catch { $captured = $_ }

            $captured | Should -Not -BeNullOrEmpty
            $captured.CategoryInfo.Category.ToString() | Should -Be 'ObjectNotFound'
            $captured.FullyQualifiedErrorId | Should -Match 'VMSwitchNotFound'
        }

        It 'propagates non-ObjectNotFound errors from Get-VMSwitch (does not remap to missing)' {
            # Locks the fix for the SilentlyContinue pre-check bug: a permission
            # error or transient WMI fault must NOT be remapped to ObjectNotFound,
            # because the Go-side resource Read treats ObjectNotFound as
            # RemoveResource -- after which the next apply calls New-VMSwitch
            # and fails on a name conflict, requiring manual import or taint.
            Mock Get-VMSwitch {
                $exception = [System.Exception]::new('access denied')
                $errorRecord = [System.Management.Automation.ErrorRecord]::new(
                    $exception, 'AccessDenied',
                    [System.Management.Automation.ErrorCategory]::PermissionDenied, $Name)
                throw $errorRecord
            }

            $captured = $null
            try { Get-HypervSwitch -Name 'sw0' } catch { $captured = $_ }

            $captured | Should -Not -BeNullOrEmpty
            $captured.CategoryInfo.Category.ToString() | Should -Be 'PermissionDenied'
        }

        It 'remaps the cmdlet''s actual "switch not found" error (InvalidArgument + FQId) to the typed envelope' {
            # Get-VMSwitch on Server 2022 + PS 5.1 reports a missing switch
            # with category=InvalidArgument and FullyQualifiedErrorId
            # 'InvalidParameter,Microsoft.HyperV.PowerShell.Commands.GetVMSwitch'
            # -- NOT the documented ObjectNotFound. Verified against a real
            # bench 2026-04 by an acceptance-test CheckDestroy failure:
            # the previous version of this test mocked the *documented*
            # shape (ObjectNotFound) and let the production catch only
            # handle that shape, so the bench-side reality slipped through
            # to the Go side as ErrPSExecution. Pinning the actual FQId
            # here keeps the test honest about the cmdlet's behavior.
            Mock Get-VMSwitch {
                $exception = [System.ArgumentException]::new(
                    "Hyper-V was unable to find a virtual switch with name `"$Name`".")
                $errorRecord = [System.Management.Automation.ErrorRecord]::new(
                    $exception,
                    'InvalidParameter,Microsoft.HyperV.PowerShell.Commands.GetVMSwitch',
                    [System.Management.Automation.ErrorCategory]::InvalidArgument,
                    $Name)
                throw $errorRecord
            }

            $captured = $null
            try { Get-HypervSwitch -Name 'missing' } catch { $captured = $_ }

            $captured | Should -Not -BeNullOrEmpty
            $captured.CategoryInfo.Category.ToString() | Should -Be 'ObjectNotFound'
            $captured.FullyQualifiedErrorId | Should -Match 'VMSwitchNotFound'
        }

        It 'NAT switch reports SwitchType=NAT and populates NAT fields when nat_name is supplied' {
            # When the caller passes nat_name (Read pulls it from prior state
            # for NAT-typed resources), the script reads NetIPAddress + NetNat
            # and synthesizes SwitchType=NAT in the output -- Hyper-V itself
            # only knows the underlying Internal type.
            Mock Get-VMSwitch {
                New-HypervSwitchSample -Name $Name -SwitchType 'Internal' `
                    -AllowManagementOS $false -NetAdapterInterfaceDescription ''
            }
            Mock Get-NetIPAddress { New-HypervNetIPAddressSample }
            Mock Get-NetNat { New-HypervNetNatSample }

            $parsed = Get-HypervSwitch -Name 'windsor-nat' -NatName 'windsor-nat' |
                ConvertFrom-Json

            $parsed.SwitchType | Should -Be 'NAT'
            $parsed.NatName | Should -Be 'windsor-nat'
            $parsed.NatInternalAddressPrefix | Should -Be '192.168.100.0/24'
            $parsed.NatHostAddress | Should -Be '192.168.100.1'
        }

        It 'NAT switch with missing NetNat surfaces ObjectNotFound (out-of-band teardown)' {
            Mock Get-VMSwitch {
                New-HypervSwitchSample -Name $Name -SwitchType 'Internal' `
                    -AllowManagementOS $false -NetAdapterInterfaceDescription ''
            }
            Mock Get-NetIPAddress { New-HypervNetIPAddressSample }
            Mock Get-NetNat { $null }

            $captured = $null
            try {
                Get-HypervSwitch -Name 'windsor-nat' -NatName 'windsor-nat'
            } catch { $captured = $_ }

            $captured | Should -Not -BeNullOrEmpty
            $captured.CategoryInfo.Category.ToString() | Should -Be 'ObjectNotFound'
        }

        It 'still handles the documented ObjectNotFound shape (defensive: older Hyper-V versions)' {
            # Belt-and-suspenders against future Hyper-V versions or other
            # cmdlet paths that might emit the documented ObjectNotFound
            # shape. The catch accepts both shapes and routes both to the
            # same VMSwitchNotFound envelope.
            Mock Get-VMSwitch {
                $exception = [System.Management.Automation.ItemNotFoundException]::new(
                    "Hyper-V was unable to find a virtual switch with name '$Name'.")
                $errorRecord = [System.Management.Automation.ErrorRecord]::new(
                    $exception,
                    'GetVMSwitch,Microsoft.HyperV.PowerShell.Commands.GetVMSwitch',
                    [System.Management.Automation.ErrorCategory]::ObjectNotFound,
                    $Name)
                throw $errorRecord
            }

            $captured = $null
            try { Get-HypervSwitch -Name 'missing' } catch { $captured = $_ }

            $captured | Should -Not -BeNullOrEmpty
            $captured.CategoryInfo.Category.ToString() | Should -Be 'ObjectNotFound'
            $captured.FullyQualifiedErrorId | Should -Match 'VMSwitchNotFound'
        }
    }
}
