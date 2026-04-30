# Locks the JSON contract for Remove-HypervVMHardDiskDrive.

BeforeAll {
    . $PSScriptRoot/_test_helpers.ps1
    . $PSScriptRoot/../common/preamble.ps1
    . $PSScriptRoot/remove-hard-disk-drive.ps1
}

Describe 'Remove-HypervVMHardDiskDrive' {

    Context 'happy path' {

        It 'forwards every argument to Remove-VMHardDiskDrive verbatim' {
            Mock Remove-VMHardDiskDrive { }

            Remove-HypervVMHardDiskDrive `
                -Name 'vm01' `
                -ControllerType 'SCSI' `
                -ControllerNumber 0 `
                -ControllerLocation 1 | Out-Null

            Should -Invoke Remove-VMHardDiskDrive -Times 1 -Exactly -ParameterFilter {
                $VMName -eq 'vm01' -and
                $ControllerType -eq 'SCSI' -and
                $ControllerNumber -eq 0 -and
                $ControllerLocation -eq 1
            }
        }

        It 'emits an empty JSON object on success' {
            Mock Remove-VMHardDiskDrive { }

            $output = Remove-HypervVMHardDiskDrive `
                -Name 'vm01' -ControllerType 'SCSI' `
                -ControllerNumber 0 -ControllerLocation 0

            $output | Should -BeOfType [string]
            ($output -split "`n" | Measure-Object).Count | Should -Be 1
            $output.Trim() | Should -Be '{}'
        }
    }

    Context 'parameter validation' {

        It 'rejects ControllerType outside {SCSI, IDE}' {
            { Remove-HypervVMHardDiskDrive `
                -Name 'vm01' -ControllerType 'NVME' `
                -ControllerNumber 0 -ControllerLocation 0 } |
                Should -Throw -ExpectedMessage '*does not belong to the set*'
        }
    }

    Context 'error propagation' {

        It 'propagates ObjectNotFound when the VM is missing' {
            Mock Remove-VMHardDiskDrive {
                $exception = [System.Management.Automation.ItemNotFoundException]::new(
                    "Hyper-V was unable to find a VM with name 'missing'.")
                $errorRecord = [System.Management.Automation.ErrorRecord]::new(
                    $exception, 'RemoveVMHardDiskDrive,Microsoft.HyperV.PowerShell.Commands.RemoveVMHardDiskDrive',
                    [System.Management.Automation.ErrorCategory]::ObjectNotFound, $VMName)
                throw $errorRecord
            }

            $captured = $null
            try {
                Remove-HypervVMHardDiskDrive `
                    -Name 'missing' -ControllerType 'SCSI' `
                    -ControllerNumber 0 -ControllerLocation 0
            } catch { $captured = $_ }

            $captured | Should -Not -BeNullOrEmpty
            $captured.CategoryInfo.Category.ToString() | Should -Be 'ObjectNotFound'
        }

        It 'propagates ObjectNotFound when the slot is already empty' {
            # Same category for "VM missing" and "slot empty" -- the
            # cmdlet itself doesn't distinguish. The Go-side
            # reconciliation in Update treats either as a no-op since
            # the desired state (slot empty) is already true.
            Mock Remove-VMHardDiskDrive {
                $exception = [System.Management.Automation.ItemNotFoundException]::new(
                    'no disk drive at that controller slot')
                $errorRecord = [System.Management.Automation.ErrorRecord]::new(
                    $exception, 'RemoveVMHardDiskDrive,SlotEmpty',
                    [System.Management.Automation.ErrorCategory]::ObjectNotFound, $VMName)
                throw $errorRecord
            }

            $captured = $null
            try {
                Remove-HypervVMHardDiskDrive `
                    -Name 'vm01' -ControllerType 'SCSI' `
                    -ControllerNumber 0 -ControllerLocation 9
            } catch { $captured = $_ }

            $captured.CategoryInfo.Category.ToString() | Should -Be 'ObjectNotFound'
        }
    }
}
