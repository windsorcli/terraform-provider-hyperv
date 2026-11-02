# Locks Remove-HypervIcsNetwork's already-gone handling.

BeforeAll {
    . $PSScriptRoot/_test_helpers.ps1
    . $PSScriptRoot/../common/preamble.ps1
    . $PSScriptRoot/_hcn.ps1
    . $PSScriptRoot/remove.ps1
}

Describe 'Remove-HypervIcsNetwork' {

    It 'deletes the network' {
        Mock Invoke-HypervHcnDelete { New-HypervHcnCallResult }

        { Remove-HypervIcsNetwork -Id '753900d5-83fd-4c4d-83b6-7c438c6ff4fe' } | Should -Not -Throw
        Should -Invoke Invoke-HypervHcnDelete -Times 1 -ParameterFilter { $Id -eq [guid]'753900d5-83fd-4c4d-83b6-7c438c6ff4fe' }
    }

    It 'succeeds when the network is already gone' {
        Mock Invoke-HypervHcnDelete { New-HypervHcnCallResult -HResult $script:HypervHrElementNotFound }

        { Remove-HypervIcsNetwork -Id '753900d5-83fd-4c4d-83b6-7c438c6ff4fe' } | Should -Not -Throw
    }

    It 'throws on any other failure' {
        Mock Invoke-HypervHcnDelete { New-HypervHcnCallResult -HResult $script:HypervHrServiceMissing }

        $err = $null
        try { Remove-HypervIcsNetwork -Id '753900d5-83fd-4c4d-83b6-7c438c6ff4fe' } catch { $err = $_ }

        $err.CategoryInfo.Category | Should -Be 'NotInstalled'
    }
}
