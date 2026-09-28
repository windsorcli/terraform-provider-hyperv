# Locks the Invoke-HypervNetNatSweep contract: filters Get-NetNat by name
# prefix, removes each match, and emits a JSON object with a `removed`
# array (even on zero / one match). Combined list+remove is a deliberate
# departure from vswitch/list.ps1, saving the round-trip the sweeper
# would otherwise chain anyway.

BeforeAll {
    . $PSScriptRoot/_test_helpers.ps1
    . $PSScriptRoot/../common/preamble.ps1
    . $PSScriptRoot/sweep.ps1
}

Describe 'Invoke-HypervNetNatSweep' {

    Context 'happy paths' {

        It 'removes only NetNats whose name starts with the prefix' {
            Mock Get-NetNat {
                @(
                    (New-HypervNetNatSample -Name 'tfacc-nat-data-abc')
                )
            }
            Mock Remove-NetNat {}

            $output = Invoke-HypervNetNatSweep -NamePrefix 'tfacc-'
            $parsed = $output | ConvertFrom-Json

            @($parsed.removed).Count | Should -Be 1
            @($parsed.removed) | Should -Contain 'tfacc-nat-data-abc'
            # Raw-JSON guard: @($parsed.removed) coerces a bare string into a
            # one-element array, so the Count / Contain assertions alone pass
            # on PS 5.1 even when ConvertTo-Json collapses the single-element
            # array to a scalar. The regex below catches that regression.
            $output | Should -Match '"removed":\["tfacc-nat-data-abc"\]'
            Should -Invoke Remove-NetNat -Times 1 -ParameterFilter {
                $Name -eq 'tfacc-nat-data-abc'
            }
        }

        It 'skips NetNats that do not match the prefix' {
            # A NetNat the operator created out-of-band must not be
            # touched -- the prefix filter is what bounds the sweep.
            Mock Get-NetNat {
                @(
                    (New-HypervNetNatSample -Name 'production-nat-keep')
                )
            }
            Mock Remove-NetNat {}

            $output = Invoke-HypervNetNatSweep -NamePrefix 'tfacc-'
            $parsed = $output | ConvertFrom-Json

            @($parsed.removed).Count | Should -Be 0
            Should -Invoke Remove-NetNat -Times 0
        }

        It 'emits an object with an empty array when there are zero matches' {
            # -InputObject keeps the output array-typed so the Go decoder gets []string{}, not null.
            Mock Get-NetNat { @() }
            Mock Remove-NetNat {}

            $output = Invoke-HypervNetNatSweep -NamePrefix 'tfacc-'
            $parsed = $output | ConvertFrom-Json

            @($parsed.removed).Count | Should -Be 0
            $output | Should -Match '"removed":\[\]'
        }

        It 'tolerates Get-NetNat returning $null (NetNat module absent or no instance)' {
            # Get-NetNat with no instance yields $null, not @(). The
            # @(...) wrapper in sweep.ps1 must coerce that to an empty
            # array so the foreach is a no-op rather than iterating
            # one null pseudo-result.
            Mock Get-NetNat { $null }
            Mock Remove-NetNat {}

            $output = Invoke-HypervNetNatSweep -NamePrefix 'tfacc-'
            $parsed = $output | ConvertFrom-Json

            @($parsed.removed).Count | Should -Be 0
            Should -Invoke Remove-NetNat -Times 0
        }
    }

    Context 'parameter validation' {

        It 'rejects an empty name prefix' {
            # [Parameter(Mandatory)] [string] accepts "" -- only $null is blocked --
            # which would expand $pattern to "*" and sweep every NetNat on the host.
            # ValidateNotNullOrEmpty is the boundary guard; the entry-block catch
            # translates the validation throw into a Write-HypervError envelope.
            { Invoke-HypervNetNatSweep -NamePrefix '' } |
                Should -Throw -ExpectedMessage '*null or empty*'
        }
    }

    Context 'error handling' {

        It 'logs and continues when Remove-NetNat fails on one instance' {
            # Best-effort sweep: a failed remove on one instance must
            # not abort the sweep on the next. Test pins that the
            # failure does not throw out of the function.
            Mock Get-NetNat {
                @(
                    (New-HypervNetNatSample -Name 'tfacc-nat-fails'),
                    (New-HypervNetNatSample -Name 'tfacc-nat-ok')
                )
            }
            Mock Remove-NetNat -ParameterFilter { $Name -eq 'tfacc-nat-fails' } {
                throw 'simulated remove failure'
            }
            Mock Remove-NetNat -ParameterFilter { $Name -eq 'tfacc-nat-ok' } {}

            $output = Invoke-HypervNetNatSweep -NamePrefix 'tfacc-' -WarningAction SilentlyContinue
            $parsed = $output | ConvertFrom-Json

            @($parsed.removed).Count | Should -Be 1
            @($parsed.removed) | Should -Contain 'tfacc-nat-ok'
            # Same raw-JSON format guard as the happy-path single-match test; pins [string[]]$removed typing here too.
            $output | Should -Match '"removed":\["tfacc-nat-ok"\]'
            @($parsed.removed) | Should -Not -Contain 'tfacc-nat-fails'
        }
    }
}
