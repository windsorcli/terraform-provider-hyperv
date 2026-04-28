# Locks the JSON contract for Add-HypervVMDvdDrive.

BeforeAll {
    . $PSScriptRoot/_test_helpers.ps1
    . $PSScriptRoot/../common/preamble.ps1
    . $PSScriptRoot/add-dvd-drive.ps1
}

Describe 'Add-HypervVMDvdDrive' {

    Context 'happy path with iso_path' {

        It 'forwards every argument to Add-VMDvdDrive verbatim, including -Path' {
            # Add-VMDvdDrive does NOT accept -ControllerType -- the
            # controller type is implicit from the VM's generation
            # (gen 1 -> IDE, gen 2 -> SCSI). The script accepts
            # ControllerType for slot identification but does not
            # forward it to the cmdlet.
            Mock Add-VMDvdDrive { }

            Add-HypervVMDvdDrive `
                -Name 'vm01' `
                -ControllerType 'SCSI' `
                -ControllerNumber 0 `
                -ControllerLocation 1 `
                -Path 'C:\hyperv\isos\boot.iso' | Out-Null

            Should -Invoke Add-VMDvdDrive -Times 1 -Exactly -ParameterFilter {
                $VMName -eq 'vm01' -and
                -not $PSBoundParameters.ContainsKey('ControllerType') -and
                $ControllerNumber -eq 0 -and
                $ControllerLocation -eq 1 -and
                $Path -eq 'C:\hyperv\isos\boot.iso'
            }
        }

        It 'emits an empty JSON object on success' {
            Mock Add-VMDvdDrive { }

            $output = Add-HypervVMDvdDrive `
                -Name 'vm01' -ControllerType 'SCSI' `
                -ControllerNumber 0 -ControllerLocation 1 `
                -Path 'C:\foo.iso'

            $output | Should -BeOfType [string]
            ($output -split "`n" | Measure-Object).Count | Should -Be 1
            $output.Trim() | Should -Be '{}'
        }
    }

    Context 'happy path without iso_path (empty drive)' {

        It 'omits -Path from the cmdlet call when iso_path is empty' {
            # Empty/absent iso_path is a legitimate config (an empty DVD
            # drive). The Add-VMDvdDrive cmdlet's behavior depends on
            # whether -Path is bound: bound = load that ISO; unbound =
            # create empty drive. The script's "if not empty" guard is
            # what selects between them.
            Mock Add-VMDvdDrive { }

            Add-HypervVMDvdDrive `
                -Name 'vm01' -ControllerType 'SCSI' `
                -ControllerNumber 0 -ControllerLocation 1 | Out-Null

            Should -Invoke Add-VMDvdDrive -Times 1 -Exactly -ParameterFilter {
                $VMName -eq 'vm01' -and
                -not $PSBoundParameters.ContainsKey('Path')
            }
        }

        It 'treats explicit empty-string Path the same as omitted Path' {
            # IsNullOrEmpty in the script defends against the wire
            # payload arriving with iso_path="" rather than null/missing.
            Mock Add-VMDvdDrive { }

            Add-HypervVMDvdDrive `
                -Name 'vm01' -ControllerType 'SCSI' `
                -ControllerNumber 0 -ControllerLocation 1 `
                -Path '' | Out-Null

            Should -Invoke Add-VMDvdDrive -Times 1 -Exactly -ParameterFilter {
                -not $PSBoundParameters.ContainsKey('Path')
            }
        }
    }

    Context 'parameter validation' {

        It 'rejects ControllerType outside {SCSI, IDE}' {
            { Add-HypervVMDvdDrive `
                -Name 'vm01' -ControllerType 'NVME' `
                -ControllerNumber 0 -ControllerLocation 1 } |
                Should -Throw -ExpectedMessage '*does not belong to the set*'
        }
    }

    Context 'error propagation' {

        It 'propagates ObjectNotFound when the VM is missing' {
            Mock Add-VMDvdDrive {
                $exception = [System.Management.Automation.ItemNotFoundException]::new(
                    "Hyper-V was unable to find a VM with name 'missing'.")
                $errorRecord = [System.Management.Automation.ErrorRecord]::new(
                    $exception, 'AddVMDvdDrive,Microsoft.HyperV.PowerShell.Commands.AddVMDvdDrive',
                    [System.Management.Automation.ErrorCategory]::ObjectNotFound, $VMName)
                throw $errorRecord
            }

            $captured = $null
            try {
                Add-HypervVMDvdDrive `
                    -Name 'missing' -ControllerType 'SCSI' `
                    -ControllerNumber 0 -ControllerLocation 1 `
                    -Path 'C:\foo.iso'
            } catch { $captured = $_ }

            $captured | Should -Not -BeNullOrEmpty
            $captured.CategoryInfo.Category.ToString() | Should -Be 'ObjectNotFound'
        }
    }
}
