# Locks the JSON contract for Add-HypervVMHardDiskDrive. The Go-side
# typed wrapper builds the stdin payload with field tags that match
# what the script's entry block reads; any change to those keys is a
# wire-level break.

BeforeAll {
    . $PSScriptRoot/_test_helpers.ps1
    . $PSScriptRoot/../common/preamble.ps1
    . $PSScriptRoot/add-hard-disk-drive.ps1
}

Describe 'Add-HypervVMHardDiskDrive' {

    Context 'happy path' {

        It 'forwards every argument to Add-VMHardDiskDrive verbatim' {
            Mock Add-VMHardDiskDrive { }

            Add-HypervVMHardDiskDrive `
                -Name 'vm01' `
                -ControllerType 'SCSI' `
                -ControllerNumber 0 `
                -ControllerLocation 1 `
                -Path 'C:\hyperv\vhds\data.vhdx' | Out-Null

            Should -Invoke Add-VMHardDiskDrive -Times 1 -Exactly -ParameterFilter {
                $VMName -eq 'vm01' -and
                $ControllerType -eq 'SCSI' -and
                $ControllerNumber -eq 0 -and
                $ControllerLocation -eq 1 -and
                $Path -eq 'C:\hyperv\vhds\data.vhdx'
            }
        }

        It 'emits an empty JSON object on success' {
            # The Go-side caller decodes the payload but only checks
            # for non-error exit; the empty object honours the
            # Write-HypervResult contract (single-line JSON, parseable).
            Mock Add-VMHardDiskDrive { }

            $output = Add-HypervVMHardDiskDrive `
                -Name 'vm01' `
                -ControllerType 'SCSI' `
                -ControllerNumber 0 `
                -ControllerLocation 0 `
                -Path 'C:\foo.vhdx'

            $output | Should -BeOfType [string]
            ($output -split "`n" | Measure-Object).Count | Should -Be 1
            # Trim handles a possible trailing newline that some PS
            # write paths add. The exact `{}` pin is the simplest
            # contract: empty object, parseable, no inner properties.
            $output.Trim() | Should -Be '{}'
        }

        It 'accepts ControllerType=IDE for gen 1 disks' {
            # The cmdlet itself errors when IDE is used on gen 2; the
            # script-side ValidateSet only constrains to {SCSI, IDE}
            # and lets Hyper-V's clear "cannot attach IDE devices to a
            # generation 2 virtual machine" error surface for the
            # cross-gen case.
            Mock Add-VMHardDiskDrive { }

            Add-HypervVMHardDiskDrive `
                -Name 'gen1-vm' `
                -ControllerType 'IDE' `
                -ControllerNumber 0 `
                -ControllerLocation 0 `
                -Path 'C:\foo.vhd' | Out-Null

            Should -Invoke Add-VMHardDiskDrive -Times 1 -Exactly -ParameterFilter {
                $ControllerType -eq 'IDE'
            }
        }
    }

    Context 'parameter validation' {

        It 'rejects ControllerType outside {SCSI, IDE}' {
            # The ValidateSet is the script-side defense against typos
            # that the resource-layer schema validator would also catch.
            # Defense in depth: a misuse via direct cmdlet invocation
            # without going through the schema layer still gets a clear
            # error, not a confusing Hyper-V cmdlet message.
            { Add-HypervVMHardDiskDrive `
                -Name 'vm01' -ControllerType 'NVME' `
                -ControllerNumber 0 -ControllerLocation 0 `
                -Path 'C:\foo.vhdx' } |
                Should -Throw -ExpectedMessage '*does not belong to the set*'
        }
    }

    Context 'error propagation' {

        It 'propagates ObjectNotFound when the VM is missing' {
            Mock Add-VMHardDiskDrive {
                $exception = [System.Management.Automation.ItemNotFoundException]::new(
                    "Hyper-V was unable to find a VM with name 'missing'.")
                $errorRecord = [System.Management.Automation.ErrorRecord]::new(
                    $exception, 'AddVMHardDiskDrive,Microsoft.HyperV.PowerShell.Commands.AddVMHardDiskDrive',
                    [System.Management.Automation.ErrorCategory]::ObjectNotFound, $VMName)
                throw $errorRecord
            }

            $captured = $null
            try {
                Add-HypervVMHardDiskDrive `
                    -Name 'missing' -ControllerType 'SCSI' `
                    -ControllerNumber 0 -ControllerLocation 0 `
                    -Path 'C:\foo.vhdx'
            } catch { $captured = $_ }

            $captured | Should -Not -BeNullOrEmpty
            $captured.CategoryInfo.Category.ToString() | Should -Be 'ObjectNotFound'
        }

        It 'propagates InvalidArgument when the slot is already occupied' {
            # Hyper-V's "controller already has a disk at that location"
            # surfaces as InvalidArgument; the Go side maps that to
            # ErrPSExecution since it's not a transient state.
            Mock Add-VMHardDiskDrive {
                $exception = [System.ArgumentException]::new('slot already occupied')
                $errorRecord = [System.Management.Automation.ErrorRecord]::new(
                    $exception, 'InvalidParameter,Microsoft.HyperV.PowerShell.Commands.AddVMHardDiskDrive',
                    [System.Management.Automation.ErrorCategory]::InvalidArgument, $VMName)
                throw $errorRecord
            }

            { Add-HypervVMHardDiskDrive `
                -Name 'vm01' -ControllerType 'SCSI' `
                -ControllerNumber 0 -ControllerLocation 0 `
                -Path 'C:\foo.vhdx' } |
                Should -Throw -ExpectedMessage '*slot already occupied*'
        }
    }
}
