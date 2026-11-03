# Locks the Remove-HypervSwitch contract: -Force is always passed, the cmdlet
# emits no stdout (caller passes dst=nil), and missing-switch errors propagate
# so the entry block can convert them to the structured error envelope.

BeforeAll {
    . $PSScriptRoot/_test_helpers.ps1
    . $PSScriptRoot/../common/preamble.ps1
    . $PSScriptRoot/remove.ps1
}

Describe 'Remove-HypervSwitch' {

    It 'invokes Remove-VMSwitch with -Force when the switch exists' {
        Mock Get-VMSwitch { New-HypervSwitchSample -Name $Name }
        Mock Remove-VMSwitch { }
        Remove-HypervSwitch -Name 'sw0'
        Should -Invoke Remove-VMSwitch -Times 1 -Exactly -ParameterFilter {
            $Name -eq 'sw0' -and $Force -eq $true
        }
    }

    It 'emits no stdout on success (caller relies on dst=nil + exit 0)' {
        Mock Get-VMSwitch { New-HypervSwitchSample -Name $Name }
        Mock Remove-VMSwitch { }
        $output = Remove-HypervSwitch -Name 'sw0'
        $output | Should -BeNullOrEmpty
    }

    It 'propagates Remove-VMSwitch errors instead of swallowing them' {
        # Locks the fix for the SilentlyContinue bug: a transient WMI fault or
        # busy-resource error from Remove-VMSwitch must surface so the Go side
        # fails Delete -- otherwise the resource would be dropped from state
        # while still present on the host.
        Mock Get-VMSwitch { New-HypervSwitchSample -Name $Name }
        Mock Remove-VMSwitch { throw 'simulated WMI service fault' }

        { Remove-HypervSwitch -Name 'sw0' } | Should -Throw -ExpectedMessage '*WMI service fault*'
    }

    It 'propagates non-ObjectNotFound errors from Get-VMSwitch (does not remap to missing)' {
        # Locks the fix for the SilentlyContinue pre-check bug: a permission
        # error or transient WMI fault must NOT be remapped to ObjectNotFound,
        # because the Go side treats ObjectNotFound as idempotent Delete success
        # and would drop a still-present switch from state.
        Mock Get-VMSwitch {
            $exception = [System.Exception]::new('access denied')
            $errorRecord = [System.Management.Automation.ErrorRecord]::new(
                $exception, 'AccessDenied',
                [System.Management.Automation.ErrorCategory]::PermissionDenied, $Name)
            throw $errorRecord
        }
        Mock Remove-VMSwitch { }

        $captured = $null
        try { Remove-HypervSwitch -Name 'sw0' } catch { $captured = $_ }

        $captured | Should -Not -BeNullOrEmpty
        $captured.CategoryInfo.Category.ToString() | Should -Be 'PermissionDenied'
        Should -Invoke Remove-VMSwitch -Times 0 -Exactly
    }

    It 'throws ObjectNotFound when the switch is missing (skips Remove-VMSwitch)' {
        # Asserts on CategoryInfo.Category because that's what the Go side
        # maps to ErrNotFound. ErrorId drift wouldn't change behavior; a
        # category drift would silently mis-route the typed error.
        Mock Get-VMSwitch { $null }
        Mock Remove-VMSwitch { }

        $captured = $null
        try { Remove-HypervSwitch -Name 'missing' } catch { $captured = $_ }

        $captured | Should -Not -BeNullOrEmpty
        $captured.CategoryInfo.Category.ToString() | Should -Be 'ObjectNotFound'
        $captured.FullyQualifiedErrorId | Should -Match 'VMSwitchNotFound'
        Should -Invoke Remove-VMSwitch -Times 0 -Exactly
    }

    Context 'attached VMs' {
        # Any refusal must come before the first Remove-* call, so the switch stays whole and stays in state.

        It 'refuses with ResourceBusy and removes nothing when a running VM is attached' {
            Mock Get-VMSwitch {
                New-HypervSwitchSample -Name $Name -SwitchType 'Internal' `
                    -AllowManagementOS $false -NetAdapterInterfaceDescription ''
            }
            Mock Get-VM { New-HypervVMSample -Name 'worker-1' -State 'Running' }
            Mock Get-VMNetworkAdapter {
                New-HypervVMNetworkAdapterSample -VMName 'worker-1' -SwitchName 'windsor-nat'
            }
            Mock Get-NetNat { New-HypervNetNatSample }
            Mock Get-NetIPAddress { New-HypervNetIPAddressSample }
            Mock Remove-NetNat { }
            Mock Remove-NetIPAddress { }
            Mock Remove-VMSwitch { }

            $captured = $null
            try { Remove-HypervSwitch -Name 'windsor-nat' -NatName 'windsor-nat' } catch { $captured = $_ }

            $captured | Should -Not -BeNullOrEmpty
            $captured.CategoryInfo.Category.ToString() | Should -Be 'ResourceBusy'
            $captured.Exception.Message | Should -Match 'worker-1'
            Should -Invoke Remove-NetNat -Times 0 -Exactly
            Should -Invoke Remove-NetIPAddress -Times 0 -Exactly
            Should -Invoke Remove-VMSwitch -Times 0 -Exactly
        }

        It 'refuses for a non-NAT switch with a running VM attached' {
            Mock Get-VMSwitch { New-HypervSwitchSample -Name $Name }
            Mock Get-VM { New-HypervVMSample -Name 'vm0' -State 'Running' }
            Mock Get-VMNetworkAdapter { New-HypervVMNetworkAdapterSample -SwitchName 'sw0' }
            Mock Remove-VMSwitch { }

            { Remove-HypervSwitch -Name 'sw0' } | Should -Throw -ExpectedMessage '*vm0*'
            Should -Invoke Remove-VMSwitch -Times 0 -Exactly
        }

        It 'treats Saved and Paused VMs as attached' {
            Mock Get-VMSwitch { New-HypervSwitchSample -Name $Name }
            Mock Get-VM {
                New-HypervVMSample -Name 'saved-vm' -State 'Saved' -Id 'aaaaaaaa-0000-0000-0000-000000000001'
                New-HypervVMSample -Name 'paused-vm' -State 'Paused' -Id 'aaaaaaaa-0000-0000-0000-000000000002'
            }
            Mock Get-VMNetworkAdapter {
                New-HypervVMNetworkAdapterSample -VMName 'saved-vm' -VMId 'aaaaaaaa-0000-0000-0000-000000000001' -SwitchName 'sw0'
                New-HypervVMNetworkAdapterSample -VMName 'paused-vm' -VMId 'aaaaaaaa-0000-0000-0000-000000000002' -SwitchName 'sw0'
            }
            Mock Remove-VMSwitch { }

            { Remove-HypervSwitch -Name 'sw0' } | Should -Throw -ExpectedMessage '*paused-vm*saved-vm*'
            Should -Invoke Remove-VMSwitch -Times 0 -Exactly
        }

        It 'proceeds when the only attached VM is Off' {
            # Remove-VMSwitch on the bench has succeeded with an Off VM attached; the VM's NIC is left disconnected.
            Mock Get-VMSwitch { New-HypervSwitchSample -Name $Name }
            Mock Get-VM { New-HypervVMSample -State 'Off' }
            Mock Get-VMNetworkAdapter { New-HypervVMNetworkAdapterSample -SwitchName 'sw0' }
            Mock Remove-VMSwitch { }

            { Remove-HypervSwitch -Name 'sw0' } | Should -Not -Throw
            Should -Invoke Remove-VMSwitch -Times 1 -Exactly
        }

        It 'ignores running VMs attached to other switches' {
            Mock Get-VMSwitch { New-HypervSwitchSample -Name $Name }
            Mock Get-VM { New-HypervVMSample -State 'Running' }
            Mock Get-VMNetworkAdapter { New-HypervVMNetworkAdapterSample -SwitchName 'other-switch' }
            Mock Remove-VMSwitch { }

            { Remove-HypervSwitch -Name 'sw0' } | Should -Not -Throw
            Should -Invoke Remove-VMSwitch -Times 1 -Exactly
        }

        It 'propagates a Get-VM failure without removing anything' {
            Mock Get-VMSwitch { New-HypervSwitchSample -Name $Name }
            Mock Get-VM { throw 'simulated WMI service fault' }
            Mock Remove-VMSwitch { }

            { Remove-HypervSwitch -Name 'sw0' } | Should -Throw -ExpectedMessage '*WMI service fault*'
            Should -Invoke Remove-VMSwitch -Times 0 -Exactly
        }
    }

    Context 'NAT switch teardown' {
        # Remove-VMSwitch runs last so a failed earlier step leaves the VMSwitch that Get-HypervSwitch keys on.

        It 'tears down NAT, then NetIPAddress, then VMSwitch in that order' {
            Mock Get-VMSwitch {
                New-HypervSwitchSample -Name $Name -SwitchType 'Internal' `
                    -AllowManagementOS $false -NetAdapterInterfaceDescription ''
            }
            Mock Get-NetIPAddress { New-HypervNetIPAddressSample }
            Mock Get-NetNat { New-HypervNetNatSample }

            $callOrder = New-Object System.Collections.Generic.List[string]
            Mock Remove-NetNat { $callOrder.Add('Remove-NetNat') }
            Mock Remove-NetIPAddress { $callOrder.Add('Remove-NetIPAddress') }
            Mock Remove-VMSwitch { $callOrder.Add('Remove-VMSwitch') }

            Remove-HypervSwitch -Name 'windsor-nat' -NatName 'windsor-nat'

            $callOrder.Count | Should -Be 3
            $callOrder[0] | Should -Be 'Remove-NetNat'
            $callOrder[1] | Should -Be 'Remove-NetIPAddress'
            $callOrder[2] | Should -Be 'Remove-VMSwitch'
        }

        It 'tolerates ObjectNotFound on Remove-NetNat (best-effort destroy: NetNat already removed out-of-band)' {
            Mock Get-VMSwitch {
                New-HypervSwitchSample -Name $Name -SwitchType 'Internal' `
                    -AllowManagementOS $false -NetAdapterInterfaceDescription ''
            }
            Mock Get-NetIPAddress { New-HypervNetIPAddressSample }
            Mock Get-NetNat { $null }
            Mock Remove-NetIPAddress { }
            Mock Remove-VMSwitch { }
            Mock Remove-NetNat { }

            { Remove-HypervSwitch -Name 'windsor-nat' -NatName 'windsor-nat' } |
                Should -Not -Throw

            Should -Invoke Remove-NetNat -Times 0 -Exactly
            Should -Invoke Remove-VMSwitch -Times 1 -Exactly
        }

        It 'tolerates ObjectNotFound on Remove-NetIPAddress (best-effort destroy)' {
            Mock Get-VMSwitch {
                New-HypervSwitchSample -Name $Name -SwitchType 'Internal' `
                    -AllowManagementOS $false -NetAdapterInterfaceDescription ''
            }
            Mock Get-NetIPAddress { $null }
            Mock Get-NetNat { New-HypervNetNatSample }
            Mock Remove-NetNat { }
            Mock Remove-VMSwitch { }
            Mock Remove-NetIPAddress { }

            { Remove-HypervSwitch -Name 'windsor-nat' -NatName 'windsor-nat' } |
                Should -Not -Throw

            Should -Invoke Remove-NetIPAddress -Times 0 -Exactly
            Should -Invoke Remove-VMSwitch -Times 1 -Exactly
        }

        It 'non-NAT remove path is unchanged when nat_name is absent' {
            Mock Get-VMSwitch { New-HypervSwitchSample -Name $Name }
            Mock Remove-VMSwitch { }
            Mock Remove-NetNat { }
            Mock Remove-NetIPAddress { }

            Remove-HypervSwitch -Name 'sw0'

            Should -Invoke Remove-NetNat -Times 0 -Exactly
            Should -Invoke Remove-NetIPAddress -Times 0 -Exactly
            Should -Invoke Remove-VMSwitch -Times 1 -Exactly
        }
    }

    It 'remaps a real ObjectNotFound error from Get-VMSwitch to the typed envelope' {
        # The real cmdlet under -ErrorAction Stop raises a terminating
        # ObjectNotFound error rather than returning $null. The catch must
        # accept that path and re-emit the VMSwitchNotFound envelope.
        Mock Get-VMSwitch {
            $exception = [System.Management.Automation.ItemNotFoundException]::new(
                "Hyper-V was unable to find a virtual switch with name '$Name'.")
            $errorRecord = [System.Management.Automation.ErrorRecord]::new(
                $exception, 'GetVMSwitch,Microsoft.HyperV.PowerShell.Commands.GetVMSwitch',
                [System.Management.Automation.ErrorCategory]::ObjectNotFound, $Name)
            throw $errorRecord
        }
        Mock Remove-VMSwitch { }

        $captured = $null
        try { Remove-HypervSwitch -Name 'missing' } catch { $captured = $_ }

        $captured | Should -Not -BeNullOrEmpty
        $captured.CategoryInfo.Category.ToString() | Should -Be 'ObjectNotFound'
        $captured.FullyQualifiedErrorId | Should -Match 'VMSwitchNotFound'
        Should -Invoke Remove-VMSwitch -Times 0 -Exactly
    }
}
