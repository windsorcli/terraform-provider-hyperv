# Locks the JSON contract for Set-HypervVMState (vm/set-state.ps1).
# The Go-side resource layer dispatches state.desired transitions
# through this script; any change to the wire shape or the dispatch
# mapping is a breaking change.

BeforeAll {
    . $PSScriptRoot/_test_helpers.ps1
    . $PSScriptRoot/../common/preamble.ps1
    . $PSScriptRoot/read-result.ps1
    . $PSScriptRoot/set-state.ps1
}

Describe 'Set-HypervVMState' {

    # Read-HypervVMResult always calls Get-VMMemory (added in the
    # dynamic-memory slice). Default mock returns a static-only shape;
    # this script doesn't directly mutate memory, so all tests share
    # the default.
    BeforeEach {
        Mock Get-VMMemory { New-HypervVMMemorySample -DynamicMemoryEnabled $false }
    }

    Context 'transition: Off -> Running' {

        It 'calls Start-VM with the resolved VM and emits the post-transition read shape' {
            Mock Get-VM { New-HypervVMSample -Name 'vm01' -State 'Running' }
            Mock Get-VMFirmware { New-HypervVMFirmwareSample }
            Mock Get-VMHardDiskDrive { @() }
            Mock Start-VM { }
            Mock Stop-VM  { }

            Set-HypervVMState -Name 'vm01' -Desired 'Running' | Out-Null

            Should -Invoke Start-VM -Times 1 -Exactly
            Should -Invoke Stop-VM  -Times 0 -Exactly
        }
    }

    Context 'transition: Running -> Off (default shutdown_mode = turn_off)' {

        It 'calls Stop-VM -TurnOff -Force when ShutdownMode is omitted' {
            Mock Get-VM { New-HypervVMSample -Name 'vm01' -State 'Off' }
            Mock Get-VMFirmware { New-HypervVMFirmwareSample }
            Mock Get-VMHardDiskDrive { @() }
            Mock Start-VM { }
            Mock Stop-VM  { }

            Set-HypervVMState -Name 'vm01' -Desired 'Off' | Out-Null

            Should -Invoke Stop-VM  -Times 1 -Exactly -ParameterFilter {
                $TurnOff.IsPresent -and $Force.IsPresent
            }
            Should -Invoke Start-VM -Times 0 -Exactly
        }

        It 'calls Stop-VM -TurnOff -Force when ShutdownMode = "turn_off" explicitly' {
            Mock Get-VM { New-HypervVMSample -Name 'vm01' -State 'Off' }
            Mock Get-VMFirmware { New-HypervVMFirmwareSample }
            Mock Get-VMHardDiskDrive { @() }
            Mock Start-VM { }
            Mock Stop-VM  { }

            Set-HypervVMState -Name 'vm01' -Desired 'Off' -ShutdownMode 'turn_off' | Out-Null

            Should -Invoke Stop-VM -Times 1 -Exactly -ParameterFilter {
                $TurnOff.IsPresent -and $Force.IsPresent
            }
        }
    }

    Context 'transition: Running -> Off (shutdown_mode = graceful)' {

        # Stop-VM -Force without -TurnOff sends the ACPI shutdown signal
        # via Hyper-V integration services. The cmdlet returns once the
        # guest acknowledges OR after Hyper-V's internal timeout (no
        # PS-level timeout knob in PS 5.1). Guests without integration
        # services hang -- documented at the schema layer.

        It 'calls Stop-VM -Force without -TurnOff (graceful ACPI shutdown)' {
            Mock Get-VM { New-HypervVMSample -Name 'vm01' -State 'Off' }
            Mock Get-VMFirmware { New-HypervVMFirmwareSample }
            Mock Get-VMHardDiskDrive { @() }
            Mock Start-VM { }
            Mock Stop-VM  { }

            Set-HypervVMState -Name 'vm01' -Desired 'Off' -ShutdownMode 'graceful' | Out-Null

            Should -Invoke Stop-VM -Times 1 -Exactly -ParameterFilter {
                -not $TurnOff.IsPresent -and $Force.IsPresent
            }
            Should -Invoke Start-VM -Times 0 -Exactly
        }
    }

    Context 'transition: Off -> Running ignores ShutdownMode' {

        # ShutdownMode only governs Stop dispatch -- Start-VM has no
        # analog. Asserted to lock the design: a future graceful-start
        # would need a separate attribute, not a re-purposed shutdown_mode.

        It 'calls Start-VM regardless of ShutdownMode value' {
            Mock Get-VM { New-HypervVMSample -Name 'vm01' -State 'Running' }
            Mock Get-VMFirmware { New-HypervVMFirmwareSample }
            Mock Get-VMHardDiskDrive { @() }
            Mock Start-VM { }
            Mock Stop-VM  { }

            Set-HypervVMState -Name 'vm01' -Desired 'Running' -ShutdownMode 'graceful' | Out-Null

            Should -Invoke Start-VM -Times 1 -Exactly
            Should -Invoke Stop-VM  -Times 0 -Exactly
        }
    }

    Context 'parameter validation' {

        It 'rejects Desired values outside {Off, Running}' {
            { Set-HypervVMState -Name 'vm01' -Desired 'Saved' } |
                Should -Throw -ExpectedMessage '*does not belong to the set*'
        }

        It 'rejects ShutdownMode values outside {turn_off, graceful}' {
            { Set-HypervVMState -Name 'vm01' -Desired 'Off' -ShutdownMode 'kill' } |
                Should -Throw -ExpectedMessage '*does not belong to the set*'
        }
    }

    Context 'error propagation' {

        It 'maps a missing VM to ObjectNotFound regardless of cmdlet category' {
            # Mirrors get.ps1's two-shape catch -- on Server 2022 + PS 5.1
            # the cmdlet emits InvalidArgument + the GetVM FQId for a
            # missing VM, NOT ObjectNotFound. The script normalizes both
            # to ObjectNotFound so the Go side maps to ErrNotFound and
            # Update can recover via destroy + recreate.
            Mock Get-VM {
                $exception = [System.ArgumentException]::new(
                    "Hyper-V was unable to find a virtual machine with name 'missing'.")
                $errorRecord = [System.Management.Automation.ErrorRecord]::new(
                    $exception,
                    'InvalidParameter,Microsoft.HyperV.PowerShell.Commands.GetVM',
                    [System.Management.Automation.ErrorCategory]::InvalidArgument,
                    'missing')
                throw $errorRecord
            }

            $captured = $null
            try {
                Set-HypervVMState -Name 'missing' -Desired 'Running'
            } catch { $captured = $_ }

            $captured | Should -Not -BeNullOrEmpty
            $captured.CategoryInfo.Category.ToString() | Should -Be 'ObjectNotFound'
        }
    }

    Context 'output shape' {

        It 'emits a single-line JSON object with the canonical read keys' {
            Mock Get-VM { New-HypervVMSample -State 'Running' }
            Mock Get-VMFirmware { New-HypervVMFirmwareSample }
            Mock Get-VMHardDiskDrive { @() }
            Mock Start-VM { }

            $output = Set-HypervVMState -Name 'vm01' -Desired 'Running'
            $output | Should -BeOfType [string]
            ($output -split "`n" | Measure-Object).Count | Should -Be 1

            $parsed = $output | ConvertFrom-Json
            $parsed.PSObject.Properties.Name | Sort-Object | Should -Be @(
                'AutomaticStartAction', 'AutomaticStartDelay', 'AutomaticStopAction',
                'BootOrder', 'CheckpointType', 'DvdDrives', 'Generation', 'HardDiskDrives', 'Id',
                'MemoryAssignedBytes', 'MemoryDynamicEnabled', 'MemoryMaximumBytes',
                'MemoryMinimumBytes', 'MemoryStartupBytes', 'Name', 'NetworkAdapters',
                'Notes', 'Path', 'ProcessorCount', 'SecureBootEnabled',
                'SecureBootTemplate', 'SmartPagingFilePath', 'SnapshotFileLocation', 'State'
            )
        }
    }
}
