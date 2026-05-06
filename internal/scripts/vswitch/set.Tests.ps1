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

        It 'Internal + AllowManagementOS rejects (External-only flag, symmetric with new.ps1)' {
            # Set-VMSwitch *would* accept -AllowManagementOS for an Internal
            # switch at the cmdlet level (parameter set ChangeManagementOS
            # matches), but setting it to $false on an Internal switch
            # silently converts it to Private -- a switch-type change that
            # would surface as state drift on the next refresh. Reject at
            # the script layer to keep the contract symmetric with new.ps1
            # ("AllowManagementOS is External-only") and to surface the
            # mismatch with a clear attribute-anchored error rather than
            # action-at-a-distance type churn.
            Mock Set-VMSwitch { }
            Mock Get-VMSwitch { New-HypervSwitchSample -SwitchType 'Internal' }

            { Set-HypervSwitch -Name 'int0' -SwitchType 'Internal' -AllowManagementOS $true } |
                Should -Throw -ExpectedMessage '*allow_management_os is not valid for switch_type ''Internal''*'

            Should -Invoke Set-VMSwitch -Times 0 -Exactly
            Should -Invoke Get-VMSwitch -Times 1 -Exactly
        }

        It 'Internal + AllowManagementOS rejects even when caller omits -SwitchType (host-side truth wins)' {
            # Regression: an earlier draft of the guard read $SwitchType
            # (the caller-supplied parameter) rather than $existing.SwitchType
            # (the host-side value populated by Get-VMSwitch). When the
            # Go-side Update omitted switch_type from its payload, the
            # guard short-circuited to false and AllowManagementOS=$false
            # got forwarded to Set-VMSwitch on a real Internal switch --
            # which silently converts it to Private. This test pins the
            # tightening: even with no caller hint, the guard reads the
            # actual switch type and rejects.
            Mock Set-VMSwitch { }
            Mock Get-VMSwitch { New-HypervSwitchSample -SwitchType 'Internal' }

            { Set-HypervSwitch -Name 'int0' -AllowManagementOS $false } |
                Should -Throw -ExpectedMessage '*allow_management_os is not valid for switch_type ''Internal''*'

            Should -Invoke Set-VMSwitch -Times 0 -Exactly
            # Pin that Get-VMSwitch was actually called -- the throw must
            # fire BECAUSE $existing.SwitchType was read, not from an
            # earlier parameter validator. Without this, a regression
            # that rejects up-front would still pass the test while
            # silently breaking the "host-side truth wins" invariant.
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

        It 'propagates non-ObjectNotFound errors from Get-VMSwitch (does not remap to missing)' {
            # Locks the fix for the SilentlyContinue pre-check bug: a permission
            # error or transient WMI fault must NOT be remapped to ObjectNotFound,
            # because the Go-side Update treats ObjectNotFound as RemoveFromState
            # -- after which the next apply calls New-VMSwitch and fails on a
            # name conflict, requiring manual import or taint to recover.
            Mock Get-VMSwitch {
                $exception = [System.Exception]::new('access denied')
                $errorRecord = [System.Management.Automation.ErrorRecord]::new(
                    $exception, 'AccessDenied',
                    [System.Management.Automation.ErrorCategory]::PermissionDenied, $Name)
                throw $errorRecord
            }
            Mock Set-VMSwitch { }

            $captured = $null
            try { Set-HypervSwitch -Name 'sw0' -AllowManagementOS $true } catch { $captured = $_ }

            $captured | Should -Not -BeNullOrEmpty
            $captured.CategoryInfo.Category.ToString() | Should -Be 'PermissionDenied'
            Should -Invoke Set-VMSwitch -Times 0 -Exactly
        }

        It 'remaps a real ObjectNotFound error from Get-VMSwitch to the typed envelope' {
            # The real cmdlet under -ErrorAction Stop raises a terminating
            # ObjectNotFound error rather than returning $null. The catch must
            # accept that path and re-emit the VMSwitchNotFound envelope so the
            # Go side maps it to ErrNotFound.
            Mock Get-VMSwitch {
                $exception = [System.Management.Automation.ItemNotFoundException]::new(
                    "Hyper-V was unable to find a virtual switch with name '$Name'.")
                $errorRecord = [System.Management.Automation.ErrorRecord]::new(
                    $exception, 'GetVMSwitch,Microsoft.HyperV.PowerShell.Commands.GetVMSwitch',
                    [System.Management.Automation.ErrorCategory]::ObjectNotFound, $Name)
                throw $errorRecord
            }
            Mock Set-VMSwitch { }

            $captured = $null
            try { Set-HypervSwitch -Name 'missing' -AllowManagementOS $true } catch { $captured = $_ }

            $captured | Should -Not -BeNullOrEmpty
            $captured.CategoryInfo.Category.ToString() | Should -Be 'ObjectNotFound'
            $captured.FullyQualifiedErrorId | Should -Match 'VMSwitchNotFound'
            Should -Invoke Set-VMSwitch -Times 0 -Exactly
        }
    }
}
