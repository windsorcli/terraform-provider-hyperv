# Locks the Get-HypervVMByPrefix contract: filters Get-VM output by name
# prefix, emits a JSON array (even on zero/one match) with only Name, not
# the full read format (state, generation, etc.) get.ps1 emits. The
# Go-side []VMName decoder depends on the array format and field set.

BeforeAll {
    . $PSScriptRoot/_test_helpers.ps1
    . $PSScriptRoot/../common/preamble.ps1
    . $PSScriptRoot/list.ps1
}

Describe 'Get-HypervVMByPrefix' {

    Context 'happy paths' {

        It 'returns only VMs whose name starts with the prefix' {
            # Mock Get-VM to return a mixed bag; expect the prefix filter
            # to drop the non-matching one. The filter is done inside the
            # script via Where-Object, not by passing the wildcard into
            # Get-VM's -Name, because the latter errors on no-match in
            # some PS versions.
            Mock Get-VM {
                @(
                    (New-HypervVMSample -Name 'tfacc-vm-basic-abc')
                    (New-HypervVMSample -Name 'tfacc-vm-boot-xyz')
                    (New-HypervVMSample -Name 'production-app-server')
                )
            }

            $output = Get-HypervVMByPrefix -NamePrefix 'tfacc-'
            $parsed = $output | ConvertFrom-Json

            @($parsed).Count | Should -Be 2
            @($parsed).Name | Should -Contain 'tfacc-vm-basic-abc'
            @($parsed).Name | Should -Contain 'tfacc-vm-boot-xyz'
            @($parsed).Name | Should -Not -Contain 'production-app-server'
        }

        It 'emits a JSON array even when there are zero matches' {
            # -InputObject keeps the output array-typed; the Go decoder is []VMName and errors on a bare object.
            Mock Get-VM { @() }

            $output = Get-HypervVMByPrefix -NamePrefix 'tfacc-'

            $output | Should -Be '[]'
        }

        It 'emits a JSON array (not a bare object) when there is exactly one match' {
            # Same array-typed invariant as the empty case, single-match version.
            Mock Get-VM { @(New-HypervVMSample -Name 'tfacc-vm-only-one') }

            $output = Get-HypervVMByPrefix -NamePrefix 'tfacc-'
            $parsed = $output | ConvertFrom-Json

            @($parsed).Count | Should -Be 1
            @($parsed).Name | Should -Contain 'tfacc-vm-only-one'
            $output | Should -Match '^\['
        }

        It 'emits only the Name field (sweeper does not need the full read shape)' {
            # Locks the minimal-format decision: a wider format means slower enumeration and a wider blast radius for contract drift.
            Mock Get-VM { @(New-HypervVMSample -Name 'tfacc-vm-shape') }

            $output = Get-HypervVMByPrefix -NamePrefix 'tfacc-'
            $parsed = $output | ConvertFrom-Json

            # One field only; no State, MemoryAssigned, Generation, etc.
            @($parsed[0].PSObject.Properties).Count | Should -Be 1
            $parsed[0].Name | Should -Be 'tfacc-vm-shape'
        }
    }

    Context 'error propagation' {

        It 'lets Get-VM errors bubble (caller -- the sweeper -- treats failure as best-effort)' {
            # Get-VM with -ErrorAction Stop turns transient WMI/vmms
            # faults into terminating errors. The script-level
            # Write-HypervError envelope is the entry-block's job;
            # Get-HypervVMByPrefix itself just lets them propagate.
            Mock Get-VM { throw 'simulated WMI failure' }

            { Get-HypervVMByPrefix -NamePrefix 'tfacc-' } |
                Should -Throw -ExpectedMessage '*simulated WMI failure*'
        }
    }
}
