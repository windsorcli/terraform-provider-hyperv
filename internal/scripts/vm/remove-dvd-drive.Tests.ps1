# Locks the JSON contract for Remove-HypervVMDvdDrive.

BeforeAll {
    . $PSScriptRoot/_test_helpers.ps1
    . $PSScriptRoot/../common/preamble.ps1
    . $PSScriptRoot/remove-dvd-drive.ps1
}

Describe 'Remove-HypervVMDvdDrive' {

    Context 'happy path' {

        It 'forwards every argument to Remove-VMDvdDrive verbatim (no -ControllerType -- cmdlet doesn''t accept it)' {
            # Remove-VMDvdDrive's parameter set keys on slot number +
            # location only. ControllerType is part of the WIRE
            # payload (matches Add for symmetry, makes the slot tuple
            # explicit on the Go side) but isn't passed through to
            # the cmdlet -- gen 1 vs gen 2 each have only one
            # controller type, so it's redundant from Hyper-V's POV.
            Mock Remove-VMDvdDrive { }

            Remove-HypervVMDvdDrive `
                -Name 'vm01' `
                -ControllerType 'SCSI' `
                -ControllerNumber 0 `
                -ControllerLocation 1 | Out-Null

            Should -Invoke Remove-VMDvdDrive -Times 1 -Exactly -ParameterFilter {
                $VMName -eq 'vm01' -and
                $ControllerNumber -eq 0 -and
                $ControllerLocation -eq 1 -and
                -not $PSBoundParameters.ContainsKey('ControllerType')
            }
        }

        It 'emits an empty JSON object on success' {
            Mock Remove-VMDvdDrive { }

            $output = Remove-HypervVMDvdDrive `
                -Name 'vm01' -ControllerType 'SCSI' `
                -ControllerNumber 0 -ControllerLocation 1

            $output | Should -BeOfType [string]
            ($output -split "`n" | Measure-Object).Count | Should -Be 1
            $output.Trim() | Should -Be '{}'
        }
    }

    Context 'parameter validation' {

        It 'rejects ControllerType outside {SCSI, IDE}' {
            { Remove-HypervVMDvdDrive `
                -Name 'vm01' -ControllerType 'NVME' `
                -ControllerNumber 0 -ControllerLocation 1 } |
                Should -Throw -ExpectedMessage '*does not belong to the set*'
        }
    }

    Context 'error propagation' {

        It 'propagates ObjectNotFound when the VM is missing' {
            Mock Remove-VMDvdDrive {
                $exception = [System.Management.Automation.ItemNotFoundException]::new(
                    "Hyper-V was unable to find a VM with name 'missing'.")
                $errorRecord = [System.Management.Automation.ErrorRecord]::new(
                    $exception, 'RemoveVMDvdDrive,Microsoft.HyperV.PowerShell.Commands.RemoveVMDvdDrive',
                    [System.Management.Automation.ErrorCategory]::ObjectNotFound, $VMName)
                throw $errorRecord
            }

            $captured = $null
            try {
                Remove-HypervVMDvdDrive `
                    -Name 'missing' -ControllerType 'SCSI' `
                    -ControllerNumber 0 -ControllerLocation 1
            } catch { $captured = $_ }

            $captured | Should -Not -BeNullOrEmpty
            $captured.CategoryInfo.Category.ToString() | Should -Be 'ObjectNotFound'
        }
    }
}
