# Locks the Remove-HypervImageFile contract: -Force is always passed, the
# function emits no stdout (caller passes dst=nil), missing-file errors
# propagate so the entry block can convert them to the PLAN.md S5 envelope,
# and non-ObjectNotFound errors from the underlying cmdlets propagate
# rather than being remapped to "missing".

BeforeAll {
    . $PSScriptRoot/_test_helpers.ps1
    . $PSScriptRoot/../common/preamble.ps1
    . $PSScriptRoot/remove.ps1
}

Describe 'Remove-HypervImageFile' {

    It 'invokes Remove-Item with -Force when the file exists' {
        Mock Test-Path { $true }
        Mock Remove-Item { }

        Remove-HypervImageFile -Path 'C:\images\foo.vhdx'

        Should -Invoke Remove-Item -Times 1 -Exactly -ParameterFilter {
            $LiteralPath -eq 'C:\images\foo.vhdx' -and $Force -eq $true
        }
    }

    It 'emits no stdout on success (caller relies on dst=nil + exit 0)' {
        Mock Test-Path { $true }
        Mock Remove-Item { }

        $output = Remove-HypervImageFile -Path 'C:\images\foo.vhdx'
        $output | Should -BeNullOrEmpty
    }

    It 'throws ObjectNotFound when the file is missing (skips Remove-Item)' {
        # Asserts on CategoryInfo.Category because that's what the Go side
        # maps to ErrNotFound. ErrorId drift wouldn't change behavior; a
        # category drift would silently mis-route the typed error.
        Mock Test-Path { $false }
        Mock Remove-Item { }

        $captured = $null
        try { Remove-HypervImageFile -Path 'C:\nope.vhdx' } catch { $captured = $_ }

        $captured | Should -Not -BeNullOrEmpty
        $captured.CategoryInfo.Category.ToString() | Should -Be 'ObjectNotFound'
        $captured.FullyQualifiedErrorId | Should -Match 'ImageFileNotFound'
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
        try { Remove-HypervImageFile -Path 'C:\restricted.vhdx' } catch { $captured = $_ }

        $captured | Should -Not -BeNullOrEmpty
        $captured.CategoryInfo.Category.ToString() | Should -Be 'PermissionDenied'
        Should -Invoke Remove-Item -Times 0 -Exactly
    }

    It 'propagates Remove-Item errors instead of swallowing them' {
        # Symmetric with the vswitch fix: a transient IO error from
        # Remove-Item must surface so Delete fails -- otherwise the resource
        # would be dropped from state while still present on the host.
        Mock Test-Path { $true }
        Mock Remove-Item { throw 'simulated IO fault' }

        { Remove-HypervImageFile -Path 'C:\images\foo.vhdx' } |
            Should -Throw -ExpectedMessage '*IO fault*'
    }
}
