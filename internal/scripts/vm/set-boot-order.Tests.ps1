# Locks the JSON contract for Set-HypervVMBootOrder.

BeforeAll {
    . $PSScriptRoot/_test_helpers.ps1
    . $PSScriptRoot/../common/preamble.ps1
    . $PSScriptRoot/set-boot-order.ps1
}

Describe 'Set-HypervVMBootOrder' {

    Context 'happy path: mixed boot order' {

        It 'resolves each entry to its device handle and forwards them in order to Set-VMFirmware -BootOrder' {
            # The script does fetch-all-then-filter for HDDs and DVDs
            # because Get-VM*Drive's parameter sets on Server 2022 +
            # PS 5.1 reject the (-VMName, -ControllerType, -Number,
            # -Location) combination. The mocks return a list that
            # includes the targeted slot; the script's Where-Object
            # picks it.
            $hddDevice = New-HypervVMHardDiskDriveSample -ControllerType 'SCSI' -ControllerNumber 0 -ControllerLocation 0
            $dvdDevice = [pscustomobject]@{
                ControllerType     = 'SCSI'
                ControllerNumber   = 0
                ControllerLocation = 1
            }
            $nicDevice = New-HypervVMNetworkAdapterSample -Name 'primary'

            Mock Get-VMHardDiskDrive { @($hddDevice) } -ParameterFilter { $VMName -eq 'vm01' }
            Mock Get-VMDvdDrive      { @($dvdDevice) } -ParameterFilter { $VMName -eq 'vm01' }
            Mock Get-VMNetworkAdapter { $nicDevice } -ParameterFilter {
                $VMName -eq 'vm01' -and $Name -eq 'primary'
            }
            Mock Set-VMFirmware { }

            $bootOrder = @(
                [pscustomobject]@{ type = 'dvd_drive';       controller_type = 'SCSI'; controller_number = 0; controller_location = 1 },
                [pscustomobject]@{ type = 'hard_disk_drive'; controller_type = 'SCSI'; controller_number = 0; controller_location = 0 },
                [pscustomobject]@{ type = 'network_adapter'; name = 'primary' }
            )

            Set-HypervVMBootOrder -Name 'vm01' -BootOrder $bootOrder | Out-Null

            Should -Invoke Get-VMDvdDrive -Times 1 -Exactly
            Should -Invoke Get-VMHardDiskDrive -Times 1 -Exactly
            Should -Invoke Get-VMNetworkAdapter -Times 1 -Exactly
            Should -Invoke Set-VMFirmware -Times 1 -Exactly -ParameterFilter {
                $VMName -eq 'vm01' -and
                $BootOrder.Count -eq 3 -and
                $BootOrder[0] -eq $dvdDevice -and
                $BootOrder[1] -eq $hddDevice -and
                $BootOrder[2] -eq $nicDevice
            }
        }

        It 'throws when boot_order references a slot that has no drive attached' {
            Mock Get-VMHardDiskDrive { @() } -ParameterFilter { $VMName -eq 'vm01' }

            $bootOrder = @(
                [pscustomobject]@{ type = 'hard_disk_drive'; controller_type = 'SCSI'; controller_number = 0; controller_location = 0 }
            )

            { Set-HypervVMBootOrder -Name 'vm01' -BootOrder $bootOrder } |
                Should -Throw -ExpectedMessage "*hard_disk_drive at SCSI 0:0*"
        }

        It 'emits an empty JSON object on success' {
            Mock Get-VMHardDiskDrive { @(New-HypervVMHardDiskDriveSample) }
            Mock Set-VMFirmware { }

            $bootOrder = @(
                [pscustomobject]@{ type = 'hard_disk_drive'; controller_type = 'SCSI'; controller_number = 0; controller_location = 0 }
            )

            $output = Set-HypervVMBootOrder -Name 'vm01' -BootOrder $bootOrder

            $output | Should -BeOfType [string]
            ($output -split "`n" | Measure-Object).Count | Should -Be 1
            $output.Trim() | Should -Be '{}'
        }
    }

    Context 'parameter validation' {

        It 'throws on unsupported entry type' {
            $bootOrder = @(
                [pscustomobject]@{ type = 'floppy' }
            )
            { Set-HypervVMBootOrder -Name 'vm01' -BootOrder $bootOrder } |
                Should -Throw -ExpectedMessage "*floppy*"
        }
    }

    Context 'error propagation' {

        It 'propagates ObjectNotFound when the VM itself is missing (Get-VM*Drive raises)' {
            # The fetch-all path means a missing VM surfaces as the
            # cmdlet erroring on the first lookup -- not as a "no
            # match" Where-Object miss (which is the
            # in-script-thrown error tested separately above).
            Mock Get-VMHardDiskDrive {
                $exception = [System.Management.Automation.ItemNotFoundException]::new(
                    "Hyper-V was unable to find a VM with name 'missing'.")
                $errorRecord = [System.Management.Automation.ErrorRecord]::new(
                    $exception, 'GetVMHardDiskDrive,Microsoft.HyperV.PowerShell.Commands.GetVMHardDiskDrive',
                    [System.Management.Automation.ErrorCategory]::ObjectNotFound, $VMName)
                throw $errorRecord
            }

            $bootOrder = @(
                [pscustomobject]@{ type = 'hard_disk_drive'; controller_type = 'SCSI'; controller_number = 0; controller_location = 0 }
            )

            $captured = $null
            try {
                Set-HypervVMBootOrder -Name 'missing' -BootOrder $bootOrder
            } catch { $captured = $_ }

            $captured | Should -Not -BeNullOrEmpty
            $captured.CategoryInfo.Category.ToString() | Should -Be 'ObjectNotFound'
        }
    }
}
