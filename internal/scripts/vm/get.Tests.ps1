# Locks the JSON contract for Get-HypervVM. The Go-side typed wrapper
# decodes the output with field tags that match the keys asserted here;
# any change to those keys or types is a wire-level break.

BeforeAll {
    . $PSScriptRoot/_test_helpers.ps1
    . $PSScriptRoot/../common/preamble.ps1
    . $PSScriptRoot/read-result.ps1
    . $PSScriptRoot/get.ps1
}

Describe 'Get-HypervVM' {

    Context 'happy path' {

        # Read-HypervVMResult always calls Get-VMMemory (added in the
        # dynamic-memory slice). The default mock returns a static-only
        # shape; tests that exercise dynamic memory override per-It.
        BeforeEach {
            Mock Get-VMMemory { New-HypervVMMemorySample -DynamicMemoryEnabled $false }
        }

        It 'emits the canonical read shape (gen 2, no HDDs attached)' {
            Mock Get-VM { New-HypervVMSample -Generation 2 }
            Mock Get-VMFirmware { New-HypervVMFirmwareSample -SecureBoot 'On' }
            Mock Get-VMHardDiskDrive { @() }

            $parsed = Get-HypervVM -Name 'sample-vm' | ConvertFrom-Json

            $parsed.PSObject.Properties.Name | Sort-Object | Should -Be @(
                'BootOrder', 'DvdDrives', 'Generation', 'HardDiskDrives', 'Id',
                'MemoryAssignedBytes', 'MemoryDynamicEnabled', 'MemoryMaximumBytes',
                'MemoryMinimumBytes', 'MemoryStartupBytes', 'Name', 'NetworkAdapters',
                'Notes', 'Path', 'ProcessorCount', 'SecureBootEnabled', 'State'
            )
        }

        It 'emits Memory dynamic fields as null when DynamicMemoryEnabled is false' {
            # Static-memory case: Get-VMMemory still returns Hyper-V's
            # Min/Max defaults (512MiB / 1TiB), but those values aren't
            # in effect, so the read-back surfaces null. Keeps the wire
            # honest about what's actually being managed.
            Mock Get-VM { New-HypervVMSample -Generation 2 }
            Mock Get-VMFirmware { New-HypervVMFirmwareSample }
            Mock Get-VMHardDiskDrive { @() }
            Mock Get-VMMemory { New-HypervVMMemorySample -DynamicMemoryEnabled $false }

            $parsed = Get-HypervVM -Name 'static-vm' | ConvertFrom-Json

            $parsed.MemoryDynamicEnabled | Should -BeFalse
            $parsed.MemoryMinimumBytes   | Should -BeNullOrEmpty
            $parsed.MemoryMaximumBytes   | Should -BeNullOrEmpty
        }

        It 'emits Memory dynamic fields verbatim when DynamicMemoryEnabled is true' {
            Mock Get-VM { New-HypervVMSample -Generation 2 -MemoryStartup 4294967296 }
            Mock Get-VMFirmware { New-HypervVMFirmwareSample }
            Mock Get-VMHardDiskDrive { @() }
            Mock Get-VMMemory {
                New-HypervVMMemorySample -DynamicMemoryEnabled $true `
                    -Startup 4294967296 -Minimum 2147483648 -Maximum 8589934592
            }

            $parsed = Get-HypervVM -Name 'dyn-vm' | ConvertFrom-Json

            $parsed.MemoryDynamicEnabled | Should -BeTrue
            $parsed.MemoryMinimumBytes   | Should -Be 2147483648
            $parsed.MemoryMaximumBytes   | Should -Be 8589934592
        }

        It 'emits an empty HardDiskDrives array when no disks attached' {
            # The @() wrapper in Read-HypervVMResult forces array-shape on
            # the JSON wire even when the cmdlet returns nothing. Without
            # it, ConvertTo-Json would emit `null` for an empty pipeline,
            # which Go's []HardDiskDrive decode rejects.
            Mock Get-VM { New-HypervVMSample -Generation 2 }
            Mock Get-VMFirmware { New-HypervVMFirmwareSample }
            Mock Get-VMHardDiskDrive { @() }

            $raw = Get-HypervVM -Name 'sample-vm'
            $raw | Should -Match '"HardDiskDrives":\[\]'
        }

        It 'emits HardDiskDrives with the four-field shape per attached disk' {
            Mock Get-VM { New-HypervVMSample -Generation 2 }
            Mock Get-VMFirmware { New-HypervVMFirmwareSample }
            Mock Get-VMHardDiskDrive {
                @(
                    New-HypervVMHardDiskDriveSample `
                        -Path 'C:\hyperv\vhds\root.vhdx' `
                        -ControllerType 'SCSI' -ControllerNumber 0 -ControllerLocation 0
                    New-HypervVMHardDiskDriveSample `
                        -Path 'C:\hyperv\vhds\data.vhdx' `
                        -ControllerType 'SCSI' -ControllerNumber 0 -ControllerLocation 1
                )
            }

            $parsed = Get-HypervVM -Name 'sample-vm' | ConvertFrom-Json

            $parsed.HardDiskDrives.Count | Should -Be 2

            $first = $parsed.HardDiskDrives[0]
            $first.Path               | Should -Be 'C:\hyperv\vhds\root.vhdx'
            $first.ControllerType     | Should -Be 'SCSI'
            $first.ControllerNumber   | Should -Be 0
            $first.ControllerLocation | Should -Be 0

            # ControllerLocation must round-trip as a JSON number (not a
            # quoted string) -- a regression that emitted `"0"` would
            # break the Go-side types.Int64 decode AND would fail the
            # `Should -Be 0` integer comparison above (Pester's -Be is
            # strict-typed; "0" -ne 0).
            $second = $parsed.HardDiskDrives[1]
            $second.ControllerLocation | Should -Be 1
        }

        It 'returns SecureBootEnabled=true when firmware reports SecureBoot=On' {
            Mock Get-VM { New-HypervVMSample -Generation 2 }
            Mock Get-VMFirmware { New-HypervVMFirmwareSample -SecureBoot 'On' }

            $parsed = Get-HypervVM -Name 'sample-vm' | ConvertFrom-Json
            $parsed.SecureBootEnabled | Should -BeTrue
        }

        It 'returns SecureBootEnabled=false when firmware reports SecureBoot=Off' {
            Mock Get-VM { New-HypervVMSample -Generation 2 }
            Mock Get-VMFirmware { New-HypervVMFirmwareSample -SecureBoot 'Off' }

            $parsed = Get-HypervVM -Name 'sample-vm' | ConvertFrom-Json
            $parsed.SecureBootEnabled | Should -BeFalse
        }

        It 'returns SecureBootEnabled=null on gen 1 (Get-VMFirmware not called)' {
            # Get-VMFirmware errors on gen 1 ("not supported for the current
            # configuration"); the script must not call it. Asserting on
            # invocation count is the load-bearing part -- a regression that
            # accidentally calls it would surface as ErrPSExecution.
            Mock Get-VM { New-HypervVMSample -Generation 1 }
            Mock Get-VMFirmware { New-HypervVMFirmwareSample }

            $parsed = Get-HypervVM -Name 'gen1-vm' | ConvertFrom-Json
            $parsed.SecureBootEnabled | Should -BeNullOrEmpty
            Should -Invoke Get-VMFirmware -Times 0 -Exactly
        }

        It 'preserves int64 memory values (multi-GiB exceeds int32)' {
            Mock Get-VM { New-HypervVMSample -MemoryStartup 17179869184 -MemoryAssigned 17179869184 }   # 16 GiB
            Mock Get-VMFirmware { New-HypervVMFirmwareSample }

            $parsed = Get-HypervVM -Name 'big-vm' | ConvertFrom-Json
            $parsed.MemoryStartupBytes  | Should -Be 17179869184
            $parsed.MemoryAssignedBytes | Should -Be 17179869184
        }

        It 'stringifies the State enum' {
            Mock Get-VM { New-HypervVMSample -State 'Running' }
            Mock Get-VMFirmware { New-HypervVMFirmwareSample }

            $parsed = Get-HypervVM -Name 'sample-vm' | ConvertFrom-Json
            $parsed.State | Should -BeOfType [string]
            $parsed.State | Should -Be 'Running'
        }

        It 'stringifies the Id Guid' {
            Mock Get-VM { New-HypervVMSample -Id 'aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee' }
            Mock Get-VMFirmware { New-HypervVMFirmwareSample }

            $parsed = Get-HypervVM -Name 'sample-vm' | ConvertFrom-Json
            $parsed.Id | Should -Be 'aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee'
        }

        It 'forwards -Name verbatim to Get-VM' {
            Mock Get-VM { New-HypervVMSample -Name $Name }
            Mock Get-VMFirmware { New-HypervVMFirmwareSample }

            Get-HypervVM -Name 'lookup-target' | Out-Null

            Should -Invoke Get-VM -Times 1 -Exactly -ParameterFilter {
                $Name -eq 'lookup-target'
            }
        }

        It 'emits an empty BootOrder array on gen 1 (Get-VMFirmware not called)' {
            # Same gen-1 guard as SecureBootEnabled -- BootOrder also
            # comes from Get-VMFirmware so it's gen-2-only.
            Mock Get-VM { New-HypervVMSample -Generation 1 }
            Mock Get-VMFirmware { New-HypervVMFirmwareSample }

            $raw = Get-HypervVM -Name 'gen1-vm'
            $raw | Should -Match '"BootOrder":\[\]'
            Should -Invoke Get-VMFirmware -Times 0 -Exactly
        }

        It 'emits BootOrder discriminated by Type (hard_disk_drive / dvd_drive / network_adapter)' {
            # The Go-side decode uses Type as the discriminator -- HDD
            # and DVD entries carry ControllerType / Number / Location;
            # NIC entries carry Name. Unused fields are zero values.
            Mock Get-VM { New-HypervVMSample -Generation 2 }
            Mock Get-VMFirmware {
                New-HypervVMFirmwareSample -BootOrder @(
                    New-HypervVMBootOrderEntrySample -DeviceType 'DvdDrive' `
                        -ControllerType 'SCSI' -ControllerNumber 0 -ControllerLocation 1
                    New-HypervVMBootOrderEntrySample -DeviceType 'HardDiskDrive' `
                        -ControllerType 'SCSI' -ControllerNumber 0 -ControllerLocation 0
                    New-HypervVMBootOrderEntrySample -DeviceType 'VMNetworkAdapter' -Name 'primary'
                )
            }

            $parsed = Get-HypervVM -Name 'sample-vm' | ConvertFrom-Json
            $parsed.BootOrder.Count | Should -Be 3

            $parsed.BootOrder[0].Type               | Should -Be 'dvd_drive'
            $parsed.BootOrder[0].ControllerType     | Should -Be 'SCSI'
            $parsed.BootOrder[0].ControllerNumber   | Should -Be 0
            $parsed.BootOrder[0].ControllerLocation | Should -Be 1
            $parsed.BootOrder[0].Name               | Should -Be ''

            $parsed.BootOrder[1].Type               | Should -Be 'hard_disk_drive'
            $parsed.BootOrder[1].ControllerLocation | Should -Be 0

            $parsed.BootOrder[2].Type               | Should -Be 'network_adapter'
            $parsed.BootOrder[2].Name               | Should -Be 'primary'
            $parsed.BootOrder[2].ControllerType     | Should -Be ''
        }

        It 'emits per-NIC MacAddress and VlanID with the documented null/zero conventions' {
            # Two NICs to pin both branches of the read script's
            # per-NIC enrichment:
            #
            #   1. Dynamic MAC + untagged: MacAddress = '' (resource
            #      layer surfaces null), VlanID = 0 (surfaces null).
            #   2. Static MAC + access VLAN 100: MacAddress is the
            #      cmdlet's hyphenated form, VlanID = 100.
            Mock Get-VM { New-HypervVMSample -Generation 2 }
            Mock Get-VMFirmware { New-HypervVMFirmwareSample }
            Mock Get-VMHardDiskDrive { @() }
            Mock Get-VMNetworkAdapter {
                @(
                    New-HypervVMNetworkAdapterSample `
                        -Name 'primary' -SwitchName 'lab' `
                        -DynamicMacAddressEnabled $true -MacAddress '00155DAABBCC'
                    New-HypervVMNetworkAdapterSample `
                        -Name 'pinned' -SwitchName 'mgmt' `
                        -DynamicMacAddressEnabled $false -MacAddress '00-15-5D-AA-BB-CC'
                )
            }
            Mock Get-VMNetworkAdapterVlan -ParameterFilter { $VMNetworkAdapter.Name -eq 'primary' } `
                -MockWith { New-HypervVMNetworkAdapterVlanSample -OperationMode 'Untagged' -AccessVlanId 0 }
            Mock Get-VMNetworkAdapterVlan -ParameterFilter { $VMNetworkAdapter.Name -eq 'pinned' } `
                -MockWith { New-HypervVMNetworkAdapterVlanSample -OperationMode 'Access'   -AccessVlanId 100 }

            $parsed = Get-HypervVM -Name 'sample-vm' | ConvertFrom-Json

            $parsed.NetworkAdapters.Count | Should -Be 2

            # Dynamic MAC pool: emit empty string -- resource layer
            # translates to null state value to keep config/state in
            # round-trip lockstep.
            $parsed.NetworkAdapters[0].Name       | Should -Be 'primary'
            $parsed.NetworkAdapters[0].MacAddress | Should -Be ''
            $parsed.NetworkAdapters[0].VlanID     | Should -Be 0

            # Static MAC + access VLAN: emit the cmdlet's value verbatim.
            $parsed.NetworkAdapters[1].Name       | Should -Be 'pinned'
            $parsed.NetworkAdapters[1].MacAddress | Should -Be '00-15-5D-AA-BB-CC'
            $parsed.NetworkAdapters[1].VlanID     | Should -Be 100
        }

        It 'compresses output to a single line (Write-HypervResult contract)' {
            Mock Get-VM { New-HypervVMSample }
            Mock Get-VMFirmware { New-HypervVMFirmwareSample }

            $output = Get-HypervVM -Name 'sample-vm'
            $output | Should -BeOfType [string]
            ($output -split "`n" | Measure-Object).Count | Should -Be 1
        }
    }

    Context 'error propagation' {

        It 'translates the cmdlet''s actual "VM not found" error (InvalidArgument + FQId) to the typed envelope' {
            # Get-VM on Server 2022 + PS 5.1 reports a missing VM with
            # category=InvalidArgument and FullyQualifiedErrorId
            # 'InvalidParameter,Microsoft.HyperV.PowerShell.Commands.GetVM' --
            # NOT the documented ObjectNotFound. Verified against the
            # bench 2026-04 by an acceptance-test CheckDestroy failure;
            # same Hyper-V quirk as Get-VMSwitch (see vswitch/get.Tests.ps1
            # for the full discussion).
            Mock Get-VM {
                $exception = [System.ArgumentException]::new(
                    "Hyper-V was unable to find a virtual machine with name `"$Name`".")
                $errorRecord = [System.Management.Automation.ErrorRecord]::new(
                    $exception,
                    'InvalidParameter,Microsoft.HyperV.PowerShell.Commands.GetVM',
                    [System.Management.Automation.ErrorCategory]::InvalidArgument,
                    $Name)
                throw $errorRecord
            }

            $captured = $null
            try { Get-HypervVM -Name 'missing' } catch { $captured = $_ }

            $captured | Should -Not -BeNullOrEmpty
            $captured.CategoryInfo.Category.ToString() | Should -Be 'ObjectNotFound'
            $captured.FullyQualifiedErrorId | Should -Match 'VMNotFound'
        }

        It 'still handles the documented ObjectNotFound shape (defensive)' {
            # Belt-and-suspenders: a future Hyper-V version or other
            # cmdlet path that emits the documented category should
            # still translate.
            Mock Get-VM {
                $exception = [System.Management.Automation.ItemNotFoundException]::new(
                    "Hyper-V was unable to find a VM with name 'missing'.")
                $errorRecord = [System.Management.Automation.ErrorRecord]::new(
                    $exception, 'GetVM,Microsoft.HyperV.PowerShell.Commands.GetVM',
                    [System.Management.Automation.ErrorCategory]::ObjectNotFound, $Name)
                throw $errorRecord
            }

            $captured = $null
            try { Get-HypervVM -Name 'missing' } catch { $captured = $_ }

            $captured.CategoryInfo.Category.ToString() | Should -Be 'ObjectNotFound'
            $captured.FullyQualifiedErrorId | Should -Match 'VMNotFound'
        }

        It 'propagates non-ObjectNotFound errors from Get-VM (e.g. permission denied)' {
            # Locks the SilentlyContinue lesson: a permission error or
            # transient vmms outage must NOT collapse into ObjectNotFound,
            # because the Go side maps that to RemoveResource and would
            # silently drop a still-present VM from state.
            Mock Get-VM {
                $exception = [System.UnauthorizedAccessException]::new('access denied')
                $errorRecord = [System.Management.Automation.ErrorRecord]::new(
                    $exception, 'AccessDenied',
                    [System.Management.Automation.ErrorCategory]::PermissionDenied, $Name)
                throw $errorRecord
            }

            $captured = $null
            try { Get-HypervVM -Name 'restricted-vm' } catch { $captured = $_ }

            $captured | Should -Not -BeNullOrEmpty
            $captured.CategoryInfo.Category.ToString() | Should -Be 'PermissionDenied'
        }

        It 'propagates Get-VMFirmware errors on gen 2 (e.g. firmware unreadable)' {
            Mock Get-VM { New-HypervVMSample -Generation 2 }
            Mock Get-VMFirmware { throw 'simulated firmware read failure' }

            { Get-HypervVM -Name 'sample-vm' } |
                Should -Throw -ExpectedMessage '*firmware read failure*'
        }
    }
}
