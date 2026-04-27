# Locks the Remove-HypervVM contract: stop-then-remove when running,
# direct remove when off, missing-VM raises ObjectNotFound, errors from
# either cmdlet propagate untouched.

BeforeAll {
    . $PSScriptRoot/_test_helpers.ps1
    . $PSScriptRoot/../common/preamble.ps1
    . $PSScriptRoot/remove.ps1
}

Describe 'Remove-HypervVM' {

    Context 'happy paths' {

        It 'calls Stop-VM -Force -TurnOff then Remove-VM -Force when the VM is Running' {
            # -TurnOff is the hard-power-off flag (vs the default graceful
            # Stop-VM which can hang indefinitely on guests with absent /
            # unresponsive integration services). Locks the destroy-as-
            # hard-stop convention -- documented in the resource's
            # MarkdownDescription.
            Mock Get-VM { New-HypervVMSample -State 'Running' }
            Mock Stop-VM { }
            Mock Remove-VM { }

            Remove-HypervVM -Name 'vm01'

            Should -Invoke Stop-VM -Times 1 -Exactly -ParameterFilter {
                $Name -eq 'vm01' -and $Force -eq $true -and $TurnOff -eq $true
            }
            Should -Invoke Remove-VM -Times 1 -Exactly -ParameterFilter {
                $Name -eq 'vm01' -and $Force -eq $true
            }
        }

        It 'skips Stop-VM and goes straight to Remove-VM when the VM is Off' {
            # Stop-VM on an already-off VM errors with "the VM is not in
            # a state to support this operation". Skip it.
            Mock Get-VM { New-HypervVMSample -State 'Off' }
            Mock Stop-VM { }
            Mock Remove-VM { }

            Remove-HypervVM -Name 'vm01'

            Should -Invoke Stop-VM   -Times 0 -Exactly
            Should -Invoke Remove-VM -Times 1 -Exactly
        }

        It 'stops then removes from Saved state too (anything not Off counts)' {
            Mock Get-VM { New-HypervVMSample -State 'Saved' }
            Mock Stop-VM { }
            Mock Remove-VM { }

            Remove-HypervVM -Name 'vm01'

            Should -Invoke Stop-VM   -Times 1 -Exactly
            Should -Invoke Remove-VM -Times 1 -Exactly
        }

        It 'emits no stdout on success (caller relies on dst=nil + exit 0)' {
            Mock Get-VM { New-HypervVMSample -State 'Off' }
            Mock Remove-VM { }

            $output = Remove-HypervVM -Name 'vm01'
            $output | Should -BeNullOrEmpty
        }
    }

    Context 'error propagation' {

        It 'throws ObjectNotFound when the VM is missing (skips Stop-VM and Remove-VM)' {
            # Asserts on CategoryInfo.Category because that's what the Go
            # side maps to ErrNotFound -- which Delete treats as success
            # (already-gone is the desired end state for destroy).
            Mock Get-VM {
                $exception = [System.Management.Automation.ItemNotFoundException]::new(
                    "Hyper-V was unable to find a VM with name 'missing'.")
                $errorRecord = [System.Management.Automation.ErrorRecord]::new(
                    $exception, 'GetVM,Microsoft.HyperV.PowerShell.Commands.GetVM',
                    [System.Management.Automation.ErrorCategory]::ObjectNotFound, $Name)
                throw $errorRecord
            }
            Mock Stop-VM { }
            Mock Remove-VM { }

            $captured = $null
            try { Remove-HypervVM -Name 'missing' } catch { $captured = $_ }

            $captured | Should -Not -BeNullOrEmpty
            $captured.CategoryInfo.Category.ToString() | Should -Be 'ObjectNotFound'
            $captured.FullyQualifiedErrorId | Should -Match 'VMNotFound'
            Should -Invoke Stop-VM   -Times 0 -Exactly
            Should -Invoke Remove-VM -Times 0 -Exactly
        }

        It 'propagates non-ObjectNotFound errors from Get-VM (e.g. permission denied)' {
            # Same SilentlyContinue lesson as the other vm scripts: a
            # permission error must NOT be remapped to ObjectNotFound,
            # because Delete treats that as already-gone success.
            Mock Get-VM {
                $exception = [System.UnauthorizedAccessException]::new('access denied')
                $errorRecord = [System.Management.Automation.ErrorRecord]::new(
                    $exception, 'AccessDenied',
                    [System.Management.Automation.ErrorCategory]::PermissionDenied, $Name)
                throw $errorRecord
            }
            Mock Stop-VM { }
            Mock Remove-VM { }

            $captured = $null
            try { Remove-HypervVM -Name 'restricted-vm' } catch { $captured = $_ }

            $captured | Should -Not -BeNullOrEmpty
            $captured.CategoryInfo.Category.ToString() | Should -Be 'PermissionDenied'
        }

        It 'propagates Stop-VM errors instead of swallowing them' {
            # If Stop-VM hangs or errors (stuck VM, integration services
            # frozen), Terraform's apply surfaces the failure -- the
            # operator decides how to recover (Hyper-V Manager force-kill,
            # restart vmms, etc.).
            Mock Get-VM { New-HypervVMSample -State 'Running' }
            Mock Stop-VM { throw 'simulated stop failure' }
            Mock Remove-VM { }

            { Remove-HypervVM -Name 'vm01' } |
                Should -Throw -ExpectedMessage '*stop failure*'

            Should -Invoke Remove-VM -Times 0 -Exactly
        }

        It 'propagates Remove-VM errors instead of swallowing them' {
            Mock Get-VM { New-HypervVMSample -State 'Off' }
            Mock Remove-VM { throw 'simulated remove failure (e.g. VHD still attached out-of-band)' }

            { Remove-HypervVM -Name 'vm01' } |
                Should -Throw -ExpectedMessage '*remove failure*'
        }
    }
}
