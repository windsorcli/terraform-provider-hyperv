# Locks the Remove-HypervSwitch contract: -Force is always passed, the cmdlet
# emits no stdout (caller passes dst=nil), and missing-switch errors propagate
# so the entry block can convert them to the PLAN.md S5 envelope.

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

    Context 'NAT switch teardown' {
        # Multi-phase teardown order is load-bearing: Remove-VMSwitch fails
        # if the NetNat instance still references the switch's vNIC, so
        # NetNat must come down first. NetIPAddress next so the vNIC is
        # un-IP'd. Finally Remove-VMSwitch.

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
