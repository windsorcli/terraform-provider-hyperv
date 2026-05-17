# Locks the Remove-HypervVHD contract: -Force is always passed, the
# function emits no stdout (caller passes dst=nil), missing-file errors
# propagate so the entry block can convert them to the structured error
# envelope, and non-ObjectNotFound errors from the underlying cmdlets
# propagate rather than being remapped to "missing".

BeforeAll {
    . $PSScriptRoot/_test_helpers.ps1
    . $PSScriptRoot/../common/preamble.ps1
    . $PSScriptRoot/remove.ps1
}

Describe 'Remove-HypervVHD' {

    It 'invokes Remove-Item with -Force when the file exists' {
        Mock Test-Path { $true }
        Mock Remove-Item { }

        Remove-HypervVHD -Path 'C:\vhds\foo.vhdx'

        Should -Invoke Remove-Item -Times 1 -Exactly -ParameterFilter {
            $LiteralPath -eq 'C:\vhds\foo.vhdx' -and $Force -eq $true
        }
    }

    It 'emits no stdout on success (caller relies on dst=nil + exit 0)' {
        Mock Test-Path { $true }
        Mock Remove-Item { }

        $output = Remove-HypervVHD -Path 'C:\vhds\foo.vhdx'
        $output | Should -BeNullOrEmpty
    }

    It 'throws ObjectNotFound when the file is missing (skips Remove-Item)' {
        Mock Test-Path { $false }
        Mock Remove-Item { }

        $captured = $null
        try { Remove-HypervVHD -Path 'C:\nope.vhdx' } catch { $captured = $_ }

        $captured | Should -Not -BeNullOrEmpty
        $captured.CategoryInfo.Category.ToString() | Should -Be 'ObjectNotFound'
        $captured.FullyQualifiedErrorId | Should -Match 'VHDNotFound'
        Should -Invoke Remove-Item -Times 0 -Exactly
    }

    It 'propagates non-ObjectNotFound errors from Test-Path (e.g. permission denied)' {
        # Locks the SilentlyContinue lesson: a permission error must NOT
        # collapse into "missing" because Delete on the Go side treats
        # ObjectNotFound as idempotent success and would drop a still-present
        # file from state.
        Mock Test-Path {
            $exception = [System.UnauthorizedAccessException]::new('access denied')
            $errorRecord = [System.Management.Automation.ErrorRecord]::new(
                $exception, 'AccessDenied',
                [System.Management.Automation.ErrorCategory]::PermissionDenied, $LiteralPath)
            throw $errorRecord
        }
        Mock Remove-Item { }

        $captured = $null
        try { Remove-HypervVHD -Path 'C:\restricted.vhdx' } catch { $captured = $_ }

        $captured | Should -Not -BeNullOrEmpty
        $captured.CategoryInfo.Category.ToString() | Should -Be 'PermissionDenied'
        Should -Invoke Remove-Item -Times 0 -Exactly
    }

    It 'propagates Remove-Item errors instead of swallowing them (e.g. file in use by VM)' {
        # The cmdlet errors loudly when the VHD is attached to a running VM
        # (open file handle). Surfacing the error lets the user see the
        # cause and detach manually.
        Mock Test-Path { $true }
        Mock Remove-Item { throw 'The file is being used by another process' }

        { Remove-HypervVHD -Path 'C:\vhds\inuse.vhdx' } |
            Should -Throw -ExpectedMessage '*used by another process*'
    }
}
