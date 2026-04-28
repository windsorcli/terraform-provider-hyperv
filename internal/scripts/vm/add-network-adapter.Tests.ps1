# Locks the JSON contract for Add-HypervVMNetworkAdapter.

BeforeAll {
    . $PSScriptRoot/_test_helpers.ps1
    . $PSScriptRoot/../common/preamble.ps1
    . $PSScriptRoot/add-network-adapter.ps1
}

Describe 'Add-HypervVMNetworkAdapter' {

    Context 'happy path' {

        It 'forwards every argument to Add-VMNetworkAdapter verbatim' {
            Mock Add-VMNetworkAdapter { }

            Add-HypervVMNetworkAdapter `
                -Name 'primary' `
                -VMName 'vm01' `
                -SwitchName 'lab-internal' | Out-Null

            Should -Invoke Add-VMNetworkAdapter -Times 1 -Exactly -ParameterFilter {
                $VMName -eq 'vm01' -and
                $Name -eq 'primary' -and
                $SwitchName -eq 'lab-internal'
            }
        }

        It 'emits an empty JSON object on success' {
            Mock Add-VMNetworkAdapter { }

            $output = Add-HypervVMNetworkAdapter `
                -Name 'primary' -VMName 'vm01' -SwitchName 'lab-internal'

            $output | Should -BeOfType [string]
            ($output -split "`n" | Measure-Object).Count | Should -Be 1
            $output.Trim() | Should -Be '{}'
        }
    }

    Context 'error propagation' {

        It 'propagates ObjectNotFound when the VM is missing' {
            Mock Add-VMNetworkAdapter {
                $exception = [System.Management.Automation.ItemNotFoundException]::new(
                    "Hyper-V was unable to find a VM with name 'missing'.")
                $errorRecord = [System.Management.Automation.ErrorRecord]::new(
                    $exception, 'AddVMNetworkAdapter,Microsoft.HyperV.PowerShell.Commands.AddVMNetworkAdapter',
                    [System.Management.Automation.ErrorCategory]::ObjectNotFound, $VMName)
                throw $errorRecord
            }

            $captured = $null
            try {
                Add-HypervVMNetworkAdapter `
                    -Name 'primary' -VMName 'missing' -SwitchName 'lab-internal'
            } catch { $captured = $_ }

            $captured | Should -Not -BeNullOrEmpty
            $captured.CategoryInfo.Category.ToString() | Should -Be 'ObjectNotFound'
        }

        It 'propagates InvalidArgument when the switch is missing' {
            # Hyper-V's "switch not found" surfaces as InvalidArgument
            # from Add-VMNetworkAdapter (the cmdlet validates the
            # switch name against Get-VMSwitch before binding). The
            # Go side maps InvalidArgument to ErrPSExecution unless
            # the FQId matches a specific Hyper-V quirk -- this one
            # gets the generic catch.
            Mock Add-VMNetworkAdapter {
                $exception = [System.ArgumentException]::new('switch not found')
                $errorRecord = [System.Management.Automation.ErrorRecord]::new(
                    $exception, 'InvalidParameter,Microsoft.HyperV.PowerShell.Commands.AddVMNetworkAdapter',
                    [System.Management.Automation.ErrorCategory]::InvalidArgument, $VMName)
                throw $errorRecord
            }

            { Add-HypervVMNetworkAdapter `
                -Name 'primary' -VMName 'vm01' -SwitchName 'no-such-switch' } |
                Should -Throw -ExpectedMessage '*switch not found*'
        }
    }
}
