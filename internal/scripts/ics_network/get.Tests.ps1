# Locks the JSON contract for Get-HypervIcsNetwork; the Go DTO decodes these keys.

BeforeAll {
    . $PSScriptRoot/_test_helpers.ps1
    . $PSScriptRoot/../common/preamble.ps1
    . $PSScriptRoot/_hcn.ps1
    . $PSScriptRoot/get.ps1
}

Describe 'Get-HypervIcsNetwork' {

    It 'emits the six-field read result as JSON' {
        Mock Invoke-HypervHcnQuery { New-HypervHcnQueryResult }
        Mock Get-VMSwitch { [pscustomobject]@{ Name = 'tfdev-ics' } }

        $parsed = Get-HypervIcsNetwork -Id '753900d5-83fd-4c4d-83b6-7c438c6ff4fe' | ConvertFrom-Json

        @($parsed.PSObject.Properties.Name) | Should -Be @('Id', 'Name', 'AddressPrefix', 'HostAddress', 'IsolationId', 'SwitchName')
        $parsed.IsolationId | Should -Be 1
    }

    It 'throws ObjectNotFound for a missing network' {
        Mock Invoke-HypervHcnQuery {
            New-HypervHcnQueryResult -HResult $script:HypervHrNetworkNotFound -Operation 'open' -Result ''
        }

        $err = $null
        try { Get-HypervIcsNetwork -Id '753900d5-83fd-4c4d-83b6-7c438c6ff4fe' } catch { $err = $_ }

        $err.CategoryInfo.Category | Should -Be 'ObjectNotFound'
    }

    It 'rejects an ID that is not a GUID' {
        { Get-HypervIcsNetwork -Id 'not-a-guid' } | Should -Throw
    }
}
