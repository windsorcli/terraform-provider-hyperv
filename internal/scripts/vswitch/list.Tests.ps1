# Locks the Get-HypervVMSwitchByPrefix contract: filters Get-VMSwitch by
# name prefix, emits a JSON array (even on zero / one match) with only
# Name, and does NOT carry the full read shape. Mirrors vm/list.Tests.ps1
# -- the two are intentionally symmetric so the sweeper logic on the Go
# side can read both via the same minimal shape.

BeforeAll {
    . $PSScriptRoot/_test_helpers.ps1
    . $PSScriptRoot/../common/preamble.ps1
    . $PSScriptRoot/list.ps1
}

Describe 'Get-HypervVMSwitchByPrefix' {

    Context 'happy paths' {

        It 'returns only switches whose name starts with the prefix' {
            Mock Get-VMSwitch {
                @(
                    (New-HypervSwitchSample -Name 'tfacc-vswitch-priv-abc')
                    (New-HypervSwitchSample -Name 'tfacc-nic-sw-vlan-xyz')
                    (New-HypervSwitchSample -Name 'production-external-switch')
                )
            }

            $output = Get-HypervVMSwitchByPrefix -NamePrefix 'tfacc-'
            $parsed = $output | ConvertFrom-Json

            @($parsed).Count | Should -Be 2
            @($parsed).Name | Should -Contain 'tfacc-vswitch-priv-abc'
            @($parsed).Name | Should -Contain 'tfacc-nic-sw-vlan-xyz'
            @($parsed).Name | Should -Not -Contain 'production-external-switch'
        }

        It 'emits a JSON array even when there are zero matches' {
            # The Go decoder is []VMSwitchName -- a JSON object instead
            # of an empty array would unmarshal-error. -InputObject in
            # the script keeps the shape array-typed.
            Mock Get-VMSwitch { @() }

            $output = Get-HypervVMSwitchByPrefix -NamePrefix 'tfacc-'

            $output | Should -Be '[]'
        }

        It 'emits a JSON array (not a bare object) when there is exactly one match' {
            Mock Get-VMSwitch { @(New-HypervSwitchSample -Name 'tfacc-only-one') }

            $output = Get-HypervVMSwitchByPrefix -NamePrefix 'tfacc-'
            $parsed = $output | ConvertFrom-Json

            @($parsed).Count | Should -Be 1
            @($parsed).Name | Should -Contain 'tfacc-only-one'
            $output | Should -Match '^\['
        }

        It 'emits only the Name field (sweeper does not need the full read shape)' {
            # Minimal shape. NAT-switch sweep support (which would need
            # the NAT name carried alongside) is deliberately deferred
            # until NAT acctests exist; current acctest bar uses
            # Private + Internal only.
            Mock Get-VMSwitch { @(New-HypervSwitchSample -Name 'tfacc-sw-shape') }

            $output = Get-HypervVMSwitchByPrefix -NamePrefix 'tfacc-'
            $parsed = $output | ConvertFrom-Json

            @($parsed[0].PSObject.Properties).Count | Should -Be 1
            $parsed[0].Name | Should -Be 'tfacc-sw-shape'
        }
    }

    Context 'error propagation' {

        It 'lets Get-VMSwitch errors bubble (sweeper treats failure as best-effort)' {
            Mock Get-VMSwitch { throw 'simulated WMI failure' }

            { Get-HypervVMSwitchByPrefix -NamePrefix 'tfacc-' } |
                Should -Throw -ExpectedMessage '*simulated WMI failure*'
        }
    }
}
