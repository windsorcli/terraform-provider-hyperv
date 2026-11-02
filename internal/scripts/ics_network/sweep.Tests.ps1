# Locks Invoke-HypervIcsNetworkSweep's prefix filter and its always-an-array output.

BeforeAll {
    . $PSScriptRoot/_test_helpers.ps1
    . $PSScriptRoot/../common/preamble.ps1
    . $PSScriptRoot/_hcn.ps1
    . $PSScriptRoot/sweep.ps1
}

Describe 'Invoke-HypervIcsNetworkSweep' {

    BeforeEach {
        Mock Get-VMSwitch { }
        Mock Invoke-HypervHcnDelete { New-HypervHcnCallResult }
        Mock Invoke-HypervHcnEnumerate {
            [pscustomobject]@{
                HResult     = 0
                Result      = '["11111111-1111-1111-1111-111111111111","22222222-2222-2222-2222-222222222222","33333333-3333-3333-3333-333333333333"]'
                ErrorRecord = $null
            }
        }
        Mock Invoke-HypervHcnQuery {
            New-HypervHcnQueryResult -Result ($script:HypervIcsPropertiesJson -replace '"Name":"tfdev-ics"', '"Name":"tfacc-ics-a"')
        } -ParameterFilter { $Id -eq [guid]'11111111-1111-1111-1111-111111111111' }
        Mock Invoke-HypervHcnQuery {
            New-HypervHcnQueryResult -Result ($script:HypervIcsPropertiesJson -replace '"Name":"tfdev-ics"', '"Name":"keep-me"')
        } -ParameterFilter { $Id -eq [guid]'22222222-2222-2222-2222-222222222222' }
        Mock Invoke-HypervHcnQuery {
            New-HypervHcnQueryResult -Result '{"ID":"33333333-3333-3333-3333-333333333333","Name":"tfacc-lan","Type":"Transparent"}'
        } -ParameterFilter { $Id -eq [guid]'33333333-3333-3333-3333-333333333333' }
    }

    It 'deletes only ICS networks under the prefix' {
        $parsed = Invoke-HypervIcsNetworkSweep -NamePrefix 'tfacc-' | ConvertFrom-Json

        @($parsed.removed) | Should -Be @('tfacc-ics-a')
        Should -Invoke Invoke-HypervHcnDelete -Times 1 -Exactly
    }

    It 'emits an empty removed array when nothing matches' {
        $json = Invoke-HypervIcsNetworkSweep -NamePrefix 'nomatch-'

        $json | Should -Be '{"removed":[]}'
    }

    It 'skips a network whose delete fails' {
        Mock Invoke-HypervHcnDelete { New-HypervHcnCallResult -HResult $script:HypervHrDuplicateName }

        (Invoke-HypervIcsNetworkSweep -NamePrefix 'tfacc-' | ConvertFrom-Json).removed.Count | Should -Be 0
    }

    It 'rejects an empty prefix' {
        { Invoke-HypervIcsNetworkSweep -NamePrefix '' } | Should -Throw
    }
}
