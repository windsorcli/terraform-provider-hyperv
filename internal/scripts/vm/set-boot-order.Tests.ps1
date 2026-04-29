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

    Context 'File-type firmware entry preservation' {

        # Set-VMFirmware -BootOrder replaces the entire firmware boot
        # sequence. The script must read the existing firmware first
        # and re-append File-type and Unknown-type entries (UEFI
        # bootloader paths the schema doesn't model) so they survive
        # the wholesale replace. Drive- and Network-type entries are
        # NOT preserved -- the user's declared boot_order owns those.

        It 'appends File-type entries from existing firmware to the end of BootOrder' {
            $hddDevice  = New-HypervVMHardDiskDriveSample -ControllerType 'SCSI' -ControllerNumber 0 -ControllerLocation 0
            $fileEntry  = [pscustomobject]@{
                BootType     = 'File'
                FirmwarePath = '\EFI\BOOT\BOOTX64.EFI'
            }
            $driveEntry = [pscustomobject]@{
                BootType = 'Drive'
                Device   = $hddDevice
            }

            Mock Get-VMHardDiskDrive { @($hddDevice) } -ParameterFilter { $VMName -eq 'vm01' }
            Mock Get-VMFirmware {
                [pscustomobject]@{
                    BootOrder = @($driveEntry, $fileEntry)
                }
            } -ParameterFilter { $VMName -eq 'vm01' }
            Mock Set-VMFirmware { }

            $bootOrder = @(
                [pscustomobject]@{ type = 'hard_disk_drive'; controller_type = 'SCSI'; controller_number = 0; controller_location = 0 }
            )

            Set-HypervVMBootOrder -Name 'vm01' -BootOrder $bootOrder | Out-Null

            Should -Invoke Get-VMFirmware -Times 1 -Exactly
            Should -Invoke Set-VMFirmware -Times 1 -Exactly -ParameterFilter {
                $VMName -eq 'vm01' -and
                $BootOrder.Count -eq 2 -and
                $BootOrder[0] -eq $hddDevice -and
                $BootOrder[1] -eq $fileEntry
            }
        }

        It 'preserves Unknown-type entries alongside File-type entries' {
            $hddDevice    = New-HypervVMHardDiskDriveSample
            $fileEntry    = [pscustomobject]@{ BootType = 'File';    FirmwarePath = '\EFI\BOOT\BOOTX64.EFI' }
            $unknownEntry = [pscustomobject]@{ BootType = 'Unknown'; FirmwarePath = '\EFI\Microsoft\Boot\bootmgfw.efi' }

            Mock Get-VMHardDiskDrive { @($hddDevice) } -ParameterFilter { $VMName -eq 'vm01' }
            Mock Get-VMFirmware {
                [pscustomobject]@{ BootOrder = @($fileEntry, $unknownEntry) }
            } -ParameterFilter { $VMName -eq 'vm01' }
            Mock Set-VMFirmware { }

            $bootOrder = @(
                [pscustomobject]@{ type = 'hard_disk_drive'; controller_type = 'SCSI'; controller_number = 0; controller_location = 0 }
            )

            Set-HypervVMBootOrder -Name 'vm01' -BootOrder $bootOrder | Out-Null

            Should -Invoke Set-VMFirmware -Times 1 -Exactly -ParameterFilter {
                $BootOrder.Count -eq 3 -and
                $BootOrder[0] -eq $hddDevice -and
                $BootOrder[1] -eq $fileEntry -and
                $BootOrder[2] -eq $unknownEntry
            }
        }

        It 'does NOT preserve Drive- or Network-type entries (those are owned by user-declared boot_order)' {
            $hddDevice   = New-HypervVMHardDiskDriveSample
            $staleDrive  = [pscustomobject]@{ BootType = 'Drive';   Device = New-HypervVMHardDiskDriveSample -Path 'C:\stale.vhdx' }
            $staleNet    = [pscustomobject]@{ BootType = 'Network'; Device = [pscustomobject]@{ Name = 'old-nic' } }

            Mock Get-VMHardDiskDrive { @($hddDevice) } -ParameterFilter { $VMName -eq 'vm01' }
            Mock Get-VMFirmware {
                [pscustomobject]@{ BootOrder = @($staleDrive, $staleNet) }
            } -ParameterFilter { $VMName -eq 'vm01' }
            Mock Set-VMFirmware { }

            $bootOrder = @(
                [pscustomobject]@{ type = 'hard_disk_drive'; controller_type = 'SCSI'; controller_number = 0; controller_location = 0 }
            )

            Set-HypervVMBootOrder -Name 'vm01' -BootOrder $bootOrder | Out-Null

            Should -Invoke Set-VMFirmware -Times 1 -Exactly -ParameterFilter {
                $BootOrder.Count -eq 1 -and
                $BootOrder[0] -eq $hddDevice
            }
        }

        It 'tolerates a firmware read with no BootOrder property (fresh VM, never booted)' {
            $hddDevice = New-HypervVMHardDiskDriveSample

            Mock Get-VMHardDiskDrive { @($hddDevice) } -ParameterFilter { $VMName -eq 'vm01' }
            Mock Get-VMFirmware { [pscustomobject]@{ BootOrder = @() } } -ParameterFilter { $VMName -eq 'vm01' }
            Mock Set-VMFirmware { }

            $bootOrder = @(
                [pscustomobject]@{ type = 'hard_disk_drive'; controller_type = 'SCSI'; controller_number = 0; controller_location = 0 }
            )

            { Set-HypervVMBootOrder -Name 'vm01' -BootOrder $bootOrder } | Should -Not -Throw

            Should -Invoke Set-VMFirmware -Times 1 -Exactly -ParameterFilter {
                $BootOrder.Count -eq 1 -and $BootOrder[0] -eq $hddDevice
            }
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
