# Locks the JSON contract for Get-HypervVM. The Go-side typed wrapper
# decodes the output with field tags that match the keys asserted here;
# any change to those keys or types is a wire-level break.

BeforeAll {
    . $PSScriptRoot/_test_helpers.ps1
    . $PSScriptRoot/../common/preamble.ps1
    . $PSScriptRoot/get.ps1
}

Describe 'Get-HypervVM' {

    Context 'happy path' {

        It 'emits the canonical read shape (gen 2, no HDDs attached)' {
            Mock Get-VM { New-HypervVMSample -Generation 2 }
            Mock Get-VMFirmware { New-HypervVMFirmwareSample -SecureBoot 'On' }
            Mock Get-VMHardDiskDrive { @() }

            $parsed = Get-HypervVM -Name 'sample-vm' | ConvertFrom-Json

            $parsed.PSObject.Properties.Name | Sort-Object | Should -Be @(
                'Generation', 'HardDiskDrives', 'Id', 'MemoryAssignedBytes',
                'MemoryStartupBytes', 'Name', 'Notes', 'Path',
                'ProcessorCount', 'SecureBootEnabled', 'State'
            )
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

        It 'compresses output to a single line (Write-HypervResult contract)' {
            Mock Get-VM { New-HypervVMSample }
            Mock Get-VMFirmware { New-HypervVMFirmwareSample }

            $output = Get-HypervVM -Name 'sample-vm'
            $output | Should -BeOfType [string]
            ($output -split "`n" | Measure-Object).Count | Should -Be 1
        }
    }

    Context 'error propagation' {

        It 'throws ObjectNotFound when the VM is missing' {
            # The Go side keys on CategoryInfo.Category for ErrNotFound
            # routing. ErrorId drift is fine; category drift would silently
            # mis-route the typed error.
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

            $captured | Should -Not -BeNullOrEmpty
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
