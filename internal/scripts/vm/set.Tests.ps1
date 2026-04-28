# Locks the partial-update semantics of Set-HypervVM. Only the keys
# present in the input get forwarded to the corresponding Set-VM* cmdlet,
# and the post-update read shape matches Get-HypervVM exactly.

BeforeAll {
    . $PSScriptRoot/_test_helpers.ps1
    . $PSScriptRoot/../common/preamble.ps1
    . $PSScriptRoot/set.ps1
}

Describe 'Set-HypervVM' {

    Context 'partial updates' {

        It 'forwards only -StartupBytes when only memory_bytes is supplied' {
            Mock Get-VM { New-HypervVMSample }
            Mock Set-VMMemory { }
            Mock Set-VMProcessor { }
            Mock Set-VMFirmware { }
            Mock Set-VM { }
            Mock Get-VMFirmware { New-HypervVMFirmwareSample }

            Set-HypervVM -Name 'vm01' -Generation 2 -MemoryBytes 8589934592 | Out-Null

            Should -Invoke Set-VMMemory -Times 1 -Exactly -ParameterFilter {
                $StartupBytes -eq 8589934592 -and $DynamicMemoryEnabled -eq $false
            }
            Should -Invoke Set-VMProcessor -Times 0 -Exactly
            Should -Invoke Set-VMFirmware  -Times 0 -Exactly
            Should -Invoke Set-VM          -Times 0 -Exactly
        }

        It 'forwards only -Count when only vcpu is supplied' {
            Mock Get-VM { New-HypervVMSample }
            Mock Set-VMMemory { }
            Mock Set-VMProcessor { }
            Mock Set-VMFirmware { }
            Mock Set-VM { }
            Mock Get-VMFirmware { New-HypervVMFirmwareSample }

            Set-HypervVM -Name 'vm01' -Generation 2 -Vcpu 8 | Out-Null

            Should -Invoke Set-VMProcessor -Times 1 -Exactly -ParameterFilter {
                $Count -eq 8
            }
            Should -Invoke Set-VMMemory -Times 0 -Exactly
        }

        It 'forwards Set-VMFirmware only on gen 2 + secure_boot present' {
            Mock Get-VM { New-HypervVMSample -Generation 2 }
            Mock Set-VMMemory { }
            Mock Set-VMProcessor { }
            Mock Set-VMFirmware { }
            Mock Set-VM { }
            Mock Get-VMFirmware { New-HypervVMFirmwareSample -SecureBoot 'Off' }

            Set-HypervVM -Name 'vm01' -Generation 2 -SecureBoot $false | Out-Null

            Should -Invoke Set-VMFirmware -Times 1 -Exactly -ParameterFilter {
                $EnableSecureBoot -eq 'Off'
            }
        }

        It 'never forwards Set-VMFirmware on gen 1, even with secure_boot supplied' {
            # Defense in depth -- the Go-side ConfigValidator should reject
            # this at plan time. Script-level guard ensures direct invocation
            # doesn't error on Set-VMFirmware (which is gen 2 only).
            Mock Get-VM { New-HypervVMSample -Generation 1 }
            Mock Set-VMFirmware { }
            Mock Set-VM { }

            Set-HypervVM -Name 'vm01' -Generation 1 -SecureBoot $true | Out-Null

            Should -Invoke Set-VMFirmware -Times 0 -Exactly
        }

        It 'forwards Set-VM -Notes including empty string (clear semantics)' {
            Mock Get-VM { New-HypervVMSample }
            Mock Set-VMMemory { }
            Mock Set-VMProcessor { }
            Mock Set-VMFirmware { }
            Mock Set-VM { }
            Mock Get-VMFirmware { New-HypervVMFirmwareSample }

            Set-HypervVM -Name 'vm01' -Generation 2 -Notes '' | Out-Null

            Should -Invoke Set-VM -Times 1 -Exactly -ParameterFilter {
                $null -ne $Notes -and $Notes -eq ''
            }
        }

        It 'forwards all four when all four are supplied' {
            Mock Get-VM { New-HypervVMSample -Generation 2 }
            Mock Set-VMMemory { }
            Mock Set-VMProcessor { }
            Mock Set-VMFirmware { }
            Mock Set-VM { }
            Mock Get-VMFirmware { New-HypervVMFirmwareSample }

            Set-HypervVM -Name 'vm01' -Generation 2 -Vcpu 4 -MemoryBytes 8589934592 `
                -SecureBoot $true -Notes 'updated' | Out-Null

            Should -Invoke Set-VMMemory    -Times 1 -Exactly
            Should -Invoke Set-VMProcessor -Times 1 -Exactly
            Should -Invoke Set-VMFirmware  -Times 1 -Exactly
            Should -Invoke Set-VM          -Times 1 -Exactly
        }
    }

    Context 'read-back' {

        It 'follows the Set-* sequence with a Get-VM read-back (twice: once for the existence pre-check, once for the post-mutation shape)' {
            Mock Get-VM { New-HypervVMSample }
            Mock Set-VMMemory { }
            Mock Get-VMFirmware { New-HypervVMFirmwareSample }

            Set-HypervVM -Name 'vm01' -Generation 2 -MemoryBytes 4294967296 | Out-Null

            Should -Invoke Get-VM -Times 2 -Exactly -ParameterFilter { $Name -eq 'vm01' }
        }

        It 'emits the same 10-field shape as get.ps1' {
            Mock Get-VM { New-HypervVMSample -Generation 2 -MemoryStartup 4294967296 }
            Mock Set-VMMemory { }
            Mock Get-VMFirmware { New-HypervVMFirmwareSample }

            Mock Get-VMHardDiskDrive { @() }

            $parsed = Set-HypervVM -Name 'vm01' -Generation 2 -MemoryBytes 4294967296 |
                ConvertFrom-Json

            $parsed.PSObject.Properties.Name | Sort-Object | Should -Be @(
                'Generation', 'HardDiskDrives', 'Id', 'MemoryAssignedBytes',
                'MemoryStartupBytes', 'Name', 'Notes', 'Path',
                'ProcessorCount', 'SecureBootEnabled', 'State'
            )
        }
    }

    Context 'error propagation' {

        It 'throws ObjectNotFound when the VM is missing (skips all Set-* cmdlets)' {
            Mock Get-VM {
                $exception = [System.Management.Automation.ItemNotFoundException]::new(
                    "Hyper-V was unable to find a VM with name 'missing'.")
                $errorRecord = [System.Management.Automation.ErrorRecord]::new(
                    $exception, 'GetVM,Microsoft.HyperV.PowerShell.Commands.GetVM',
                    [System.Management.Automation.ErrorCategory]::ObjectNotFound, $Name)
                throw $errorRecord
            }
            Mock Set-VMMemory { }
            Mock Set-VMProcessor { }
            Mock Set-VMFirmware { }
            Mock Set-VM { }

            $captured = $null
            try {
                Set-HypervVM -Name 'missing' -Generation 2 -Vcpu 4
            } catch { $captured = $_ }

            $captured | Should -Not -BeNullOrEmpty
            $captured.CategoryInfo.Category.ToString() | Should -Be 'ObjectNotFound'
            $captured.FullyQualifiedErrorId | Should -Match 'VMNotFound'
            Should -Invoke Set-VMMemory    -Times 0 -Exactly
            Should -Invoke Set-VMProcessor -Times 0 -Exactly
        }

        It 'propagates non-ObjectNotFound errors from Get-VM (e.g. permission denied)' {
            Mock Get-VM {
                $exception = [System.UnauthorizedAccessException]::new('access denied')
                $errorRecord = [System.Management.Automation.ErrorRecord]::new(
                    $exception, 'AccessDenied',
                    [System.Management.Automation.ErrorCategory]::PermissionDenied, $Name)
                throw $errorRecord
            }
            Mock Set-VMMemory { }

            $captured = $null
            try {
                Set-HypervVM -Name 'restricted' -Generation 2 -Vcpu 4
            } catch { $captured = $_ }

            $captured | Should -Not -BeNullOrEmpty
            $captured.CategoryInfo.Category.ToString() | Should -Be 'PermissionDenied'
        }

        It 'lets Set-VMMemory terminating errors propagate (e.g. VM-must-be-Off)' {
            # Memory/CPU changes generally require the VM to be powered off.
            # The script doesn't auto-stop -- the cmdlet's clear error reaches
            # the operator who chooses whether to use hyperv_vm_state to
            # power down first.
            Mock Get-VM { New-HypervVMSample -State 'Running' }
            Mock Set-VMMemory {
                $exception = [System.InvalidOperationException]::new(
                    'VM must be in Off state to change StartupBytes')
                $errorRecord = [System.Management.Automation.ErrorRecord]::new(
                    $exception, 'VMNotOff',
                    [System.Management.Automation.ErrorCategory]::InvalidOperation, $VMName)
                throw $errorRecord
            }

            { Set-HypervVM -Name 'vm01' -Generation 2 -MemoryBytes 8589934592 } |
                Should -Throw -ErrorId 'VMNotOff'
        }
    }
}
