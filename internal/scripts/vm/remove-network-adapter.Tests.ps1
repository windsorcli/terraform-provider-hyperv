# Locks the JSON contract for Remove-HypervVMNetworkAdapter.

BeforeAll {
    . $PSScriptRoot/_test_helpers.ps1
    . $PSScriptRoot/../common/preamble.ps1
    . $PSScriptRoot/remove-network-adapter.ps1
}

Describe 'Remove-HypervVMNetworkAdapter' {

    Context 'happy path' {

        It 'forwards every argument to Remove-VMNetworkAdapter verbatim' {
            Mock Remove-VMNetworkAdapter { }

            Remove-HypervVMNetworkAdapter `
                -Name 'primary' -VMName 'vm01' | Out-Null

            Should -Invoke Remove-VMNetworkAdapter -Times 1 -Exactly -ParameterFilter {
                $VMName -eq 'vm01' -and
                $Name -eq 'primary'
            }
        }

        It 'emits an empty JSON object on success' {
            Mock Remove-VMNetworkAdapter { }

            $output = Remove-HypervVMNetworkAdapter -Name 'primary' -VMName 'vm01'

            $output | Should -BeOfType [string]
            ($output -split "`n" | Measure-Object).Count | Should -Be 1
            $output.Trim() | Should -Be '{}'
        }
    }

    Context 'error propagation' {

        It 'propagates ObjectNotFound when the VM is missing' {
            Mock Remove-VMNetworkAdapter {
                $exception = [System.Management.Automation.ItemNotFoundException]::new(
                    "Hyper-V was unable to find a VM with name 'missing'.")
                $errorRecord = [System.Management.Automation.ErrorRecord]::new(
                    $exception, 'RemoveVMNetworkAdapter,Microsoft.HyperV.PowerShell.Commands.RemoveVMNetworkAdapter',
                    [System.Management.Automation.ErrorCategory]::ObjectNotFound, $VMName)
                throw $errorRecord
            }

            $captured = $null
            try {
                Remove-HypervVMNetworkAdapter -Name 'primary' -VMName 'missing'
            } catch { $captured = $_ }

            $captured | Should -Not -BeNullOrEmpty
            $captured.CategoryInfo.Category.ToString() | Should -Be 'ObjectNotFound'
        }

        It 'propagates ObjectNotFound when the NIC name is not on the VM' {
            # The Go-side reconciliation in Update treats this as a
            # no-op since the desired state (NIC removed) is already
            # met. Same pattern as Remove-VMHardDiskDrive on an empty
            # slot.
            Mock Remove-VMNetworkAdapter {
                $exception = [System.Management.Automation.ItemNotFoundException]::new(
                    "no NIC named 'gone' on VM")
                $errorRecord = [System.Management.Automation.ErrorRecord]::new(
                    $exception, 'RemoveVMNetworkAdapter,NicNotFound',
                    [System.Management.Automation.ErrorCategory]::ObjectNotFound, $VMName)
                throw $errorRecord
            }

            $captured = $null
            try {
                Remove-HypervVMNetworkAdapter -Name 'gone' -VMName 'vm01'
            } catch { $captured = $_ }

            $captured.CategoryInfo.Category.ToString() | Should -Be 'ObjectNotFound'
        }
    }
}
