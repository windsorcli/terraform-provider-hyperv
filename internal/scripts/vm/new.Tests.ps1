# Locks the JSON contract for New-HypervVM. The script's create sequence
# is New-VM -> Set-VMMemory (with DynamicMemoryEnabled=false) ->
# Set-VMProcessor -> Set-VMFirmware (gen 2 + secure_boot only) -> Set-VM
# (notes only) -> Get-VM read-back. Each cmdlet's parameter forwarding is
# pinned here.

BeforeAll {
    . $PSScriptRoot/_test_helpers.ps1
    . $PSScriptRoot/../common/preamble.ps1
    . $PSScriptRoot/new.ps1
}

Describe 'New-HypervVM' {

    Context 'minimal create (gen 2, no optionals)' {

        It 'forwards -Name -Generation -MemoryStartupBytes -BootDevice None -NoVHD to New-VM' {
            Mock New-VM { }
            Mock Set-VMMemory { }
            Mock Set-VMProcessor { }
            Mock Set-VM { }
            Mock Set-VMFirmware { }
            Mock Get-VM { New-HypervVMSample -Generation 2 }
            Mock Get-VMFirmware { New-HypervVMFirmwareSample }

            New-HypervVM -Name 'vm01' -Generation 2 -Vcpu 2 -MemoryBytes 4294967296 | Out-Null

            Should -Invoke New-VM -Times 1 -Exactly -ParameterFilter {
                $Name -eq 'vm01' -and
                $Generation -eq 2 -and
                $MemoryStartupBytes -eq 4294967296 -and
                $BootDevice -eq 'None' -and
                $NoVHD -eq $true
            }
        }

        It 'sets static memory (DynamicMemoryEnabled=$false in the same call as StartupBytes)' {
            # The dynamic flag MUST land in the same Set-VMMemory call as
            # StartupBytes, otherwise the cmdlet rejects StartupBytes as
            # out-of-range against the still-default dynamic min/max.
            Mock New-VM { }
            Mock Set-VMMemory { }
            Mock Set-VMProcessor { }
            Mock Set-VMFirmware { }
            Mock Set-VM { }
            Mock Get-VM { New-HypervVMSample }
            Mock Get-VMFirmware { New-HypervVMFirmwareSample }

            New-HypervVM -Name 'vm01' -Generation 2 -Vcpu 2 -MemoryBytes 4294967296 | Out-Null

            Should -Invoke Set-VMMemory -Times 1 -Exactly -ParameterFilter {
                $VMName -eq 'vm01' -and
                $DynamicMemoryEnabled -eq $false -and
                $StartupBytes -eq 4294967296
            }
        }

        It 'sets vcpu via Set-VMProcessor -Count' {
            Mock New-VM { }
            Mock Set-VMMemory { }
            Mock Set-VMProcessor { }
            Mock Set-VMFirmware { }
            Mock Set-VM { }
            Mock Get-VM { New-HypervVMSample }
            Mock Get-VMFirmware { New-HypervVMFirmwareSample }

            New-HypervVM -Name 'vm01' -Generation 2 -Vcpu 4 -MemoryBytes 4294967296 | Out-Null

            Should -Invoke Set-VMProcessor -Times 1 -Exactly -ParameterFilter {
                $VMName -eq 'vm01' -and $Count -eq 4
            }
        }

        It 'does NOT call Set-VMFirmware when secure_boot is omitted' {
            Mock New-VM { }
            Mock Set-VMMemory { }
            Mock Set-VMProcessor { }
            Mock Set-VMFirmware { }
            Mock Set-VM { }
            Mock Get-VM { New-HypervVMSample }
            Mock Get-VMFirmware { New-HypervVMFirmwareSample }

            New-HypervVM -Name 'vm01' -Generation 2 -Vcpu 2 -MemoryBytes 4294967296 | Out-Null

            Should -Invoke Set-VMFirmware -Times 0 -Exactly
        }

        It 'does NOT call Set-VM when notes is omitted' {
            Mock New-VM { }
            Mock Set-VMMemory { }
            Mock Set-VMProcessor { }
            Mock Set-VMFirmware { }
            Mock Set-VM { }
            Mock Get-VM { New-HypervVMSample }
            Mock Get-VMFirmware { New-HypervVMFirmwareSample }

            New-HypervVM -Name 'vm01' -Generation 2 -Vcpu 2 -MemoryBytes 4294967296 | Out-Null

            Should -Invoke Set-VM -Times 0 -Exactly
        }

        It 'emits the canonical 10-field shape after create (matches get.ps1)' {
            Mock New-VM { }
            Mock Set-VMMemory { }
            Mock Set-VMProcessor { }
            Mock Set-VMFirmware { }
            Mock Set-VM { }
            Mock Get-VM { New-HypervVMSample -Generation 2 }
            Mock Get-VMFirmware { New-HypervVMFirmwareSample -SecureBoot 'On' }

            $parsed = New-HypervVM -Name 'vm01' -Generation 2 -Vcpu 2 -MemoryBytes 4294967296 |
                ConvertFrom-Json

            $parsed.PSObject.Properties.Name | Sort-Object | Should -Be @(
                'Generation', 'Id', 'MemoryAssignedBytes', 'MemoryStartupBytes',
                'Name', 'Notes', 'Path', 'ProcessorCount', 'SecureBootEnabled',
                'State'
            )
        }
    }

    Context 'gen 2 with secure_boot' {

        It 'forwards Set-VMFirmware -EnableSecureBoot On when secure_boot=$true' {
            Mock New-VM { }
            Mock Set-VMMemory { }
            Mock Set-VMProcessor { }
            Mock Set-VMFirmware { }
            Mock Set-VM { }
            Mock Get-VM { New-HypervVMSample -Generation 2 }
            Mock Get-VMFirmware { New-HypervVMFirmwareSample -SecureBoot 'On' }

            New-HypervVM -Name 'vm01' -Generation 2 -Vcpu 2 -MemoryBytes 4294967296 -SecureBoot $true |
                Out-Null

            Should -Invoke Set-VMFirmware -Times 1 -Exactly -ParameterFilter {
                $VMName -eq 'vm01' -and $EnableSecureBoot -eq 'On'
            }
        }

        It 'forwards Set-VMFirmware -EnableSecureBoot Off when secure_boot=$false' {
            Mock New-VM { }
            Mock Set-VMMemory { }
            Mock Set-VMProcessor { }
            Mock Set-VMFirmware { }
            Mock Set-VM { }
            Mock Get-VM { New-HypervVMSample -Generation 2 }
            Mock Get-VMFirmware { New-HypervVMFirmwareSample -SecureBoot 'Off' }

            New-HypervVM -Name 'vm01' -Generation 2 -Vcpu 2 -MemoryBytes 4294967296 -SecureBoot $false |
                Out-Null

            Should -Invoke Set-VMFirmware -Times 1 -Exactly -ParameterFilter {
                $VMName -eq 'vm01' -and $EnableSecureBoot -eq 'Off'
            }
        }
    }

    Context 'gen 1 (BIOS)' {

        It 'never calls Set-VMFirmware on gen 1, even if secure_boot is supplied' {
            # Defense in depth -- the Go-side ConfigValidator should reject
            # secure_boot on gen 1 at plan time, but the script must not
            # error if the validator is bypassed (direct invocation, future
            # caller bug).
            Mock New-VM { }
            Mock Set-VMMemory { }
            Mock Set-VMProcessor { }
            Mock Set-VMFirmware { }
            Mock Set-VM { }
            Mock Get-VM { New-HypervVMSample -Generation 1 }
            Mock Get-VMFirmware { New-HypervVMFirmwareSample }

            New-HypervVM -Name 'vm01' -Generation 1 -Vcpu 2 -MemoryBytes 4294967296 -SecureBoot $true |
                Out-Null

            Should -Invoke Set-VMFirmware -Times 0 -Exactly
        }

        It 'returns SecureBootEnabled=null on gen 1 (Get-VMFirmware not called)' {
            Mock New-VM { }
            Mock Set-VMMemory { }
            Mock Set-VMProcessor { }
            Mock Set-VM { }
            Mock Get-VM { New-HypervVMSample -Generation 1 }
            Mock Get-VMFirmware { New-HypervVMFirmwareSample }

            $parsed = New-HypervVM -Name 'vm01' -Generation 1 -Vcpu 2 -MemoryBytes 4294967296 |
                ConvertFrom-Json

            $parsed.SecureBootEnabled | Should -BeNullOrEmpty
            Should -Invoke Get-VMFirmware -Times 0 -Exactly
        }
    }

    Context 'optional notes' {

        It 'forwards Set-VM -Notes when notes is supplied' {
            Mock New-VM { }
            Mock Set-VMMemory { }
            Mock Set-VMProcessor { }
            Mock Set-VMFirmware { }
            Mock Set-VM { }
            Mock Get-VM { New-HypervVMSample -Notes 'production' }
            Mock Get-VMFirmware { New-HypervVMFirmwareSample }

            New-HypervVM -Name 'vm01' -Generation 2 -Vcpu 2 -MemoryBytes 4294967296 -Notes 'production' |
                Out-Null

            Should -Invoke Set-VM -Times 1 -Exactly -ParameterFilter {
                $Name -eq 'vm01' -and $Notes -eq 'production'
            }
        }

        It 'forwards an empty string Notes (clear semantics)' {
            Mock New-VM { }
            Mock Set-VMMemory { }
            Mock Set-VMProcessor { }
            Mock Set-VMFirmware { }
            Mock Set-VM { }
            Mock Get-VM { New-HypervVMSample }
            Mock Get-VMFirmware { New-HypervVMFirmwareSample }

            New-HypervVM -Name 'vm01' -Generation 2 -Vcpu 2 -MemoryBytes 4294967296 -Notes '' | Out-Null

            Should -Invoke Set-VM -Times 1 -Exactly -ParameterFilter {
                $null -ne $Notes -and $Notes -eq ''
            }
        }
    }

    Context 'error propagation' {

        It 'lets New-VM terminating errors propagate (e.g. duplicate name)' {
            Mock New-VM {
                $exception = [System.InvalidOperationException]::new("VM 'vm01' already exists")
                $errorRecord = [System.Management.Automation.ErrorRecord]::new(
                    $exception, 'VMAlreadyExists',
                    [System.Management.Automation.ErrorCategory]::ResourceExists, 'vm01')
                throw $errorRecord
            }
            Mock Set-VMMemory { }
            Mock Set-VMProcessor { }
            Mock Get-VM { New-HypervVMSample }
            Mock Get-VMFirmware { New-HypervVMFirmwareSample }

            { New-HypervVM -Name 'vm01' -Generation 2 -Vcpu 2 -MemoryBytes 4294967296 } |
                Should -Throw -ErrorId 'VMAlreadyExists'

            Should -Invoke Set-VMMemory   -Times 0 -Exactly
            Should -Invoke Set-VMProcessor -Times 0 -Exactly
        }
    }
}
