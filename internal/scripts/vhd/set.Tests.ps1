# Locks the resize-only mutation contract for Set-HypervVHD. All other
# attribute changes RequiresReplace at the schema layer and never reach
# this script -- see resource.go.

BeforeAll {
    . $PSScriptRoot/_test_helpers.ps1
    . $PSScriptRoot/../common/preamble.ps1
    . $PSScriptRoot/set.ps1
}

Describe 'Set-HypervVHD' {

    Context 'happy path' {

        It 'invokes Resize-VHD with -Path and -SizeBytes' {
            Mock Test-Path { $true }
            Mock Resize-VHD { }
            Mock Get-VHD { New-HypervVHDSample -Size 2147483648 }

            Set-HypervVHD -Path 'C:\vhds\foo.vhdx' -SizeBytes 2147483648 | Out-Null

            Should -Invoke Resize-VHD -Times 1 -Exactly -ParameterFilter {
                $Path -eq 'C:\vhds\foo.vhdx' -and
                $SizeBytes -eq 2147483648
            }
        }

        It 'follows Resize-VHD with a Get-VHD read-back' {
            Mock Test-Path { $true }
            Mock Resize-VHD { }
            Mock Get-VHD { New-HypervVHDSample -Size 2147483648 }

            Set-HypervVHD -Path 'C:\vhds\foo.vhdx' -SizeBytes 2147483648 | Out-Null

            Should -Invoke Get-VHD -Times 1 -Exactly -ParameterFilter {
                $Path -eq 'C:\vhds\foo.vhdx'
            }
        }

        It 'emits the post-resize shape (matches get.ps1)' {
            Mock Test-Path { $true }
            Mock Resize-VHD { }
            Mock Get-VHD { New-HypervVHDSample -Size 2147483648 }

            $parsed = Set-HypervVHD -Path 'C:\vhds\foo.vhdx' -SizeBytes 2147483648 | ConvertFrom-Json

            $parsed.PSObject.Properties.Name | Sort-Object | Should -Be @(
                'Attached', 'BlockSizeBytes', 'FileSizeBytes', 'Format',
                'ParentPath', 'Path', 'SizeBytes', 'VhdType'
            )
            $parsed.SizeBytes | Should -Be 2147483648
        }
    }

    Context 'error propagation' {

        It 'throws ObjectNotFound when the file is missing (skips Resize-VHD)' {
            Mock Test-Path { $false }
            Mock Resize-VHD { }
            Mock Get-VHD { New-HypervVHDSample }

            $captured = $null
            try { Set-HypervVHD -Path 'C:\nope.vhdx' -SizeBytes 1073741824 } catch { $captured = $_ }

            $captured | Should -Not -BeNullOrEmpty
            $captured.CategoryInfo.Category.ToString() | Should -Be 'ObjectNotFound'
            $captured.FullyQualifiedErrorId | Should -Match 'VHDNotFound'
            Should -Invoke Resize-VHD -Times 0 -Exactly
        }

        It 'propagates Resize-VHD errors (e.g. shrink without compaction)' {
            # Hyper-V refuses to shrink a disk whose trailing blocks aren't
            # empty -- documented behavior; the cmdlet's error message tells
            # the user to run Optimize-VHD first. We let it bubble up.
            Mock Test-Path { $true }
            Mock Resize-VHD { throw 'The size cannot be less than the minimum file size' }
            Mock Get-VHD { New-HypervVHDSample }

            { Set-HypervVHD -Path 'C:\vhds\foo.vhdx' -SizeBytes 1024 } |
                Should -Throw -ExpectedMessage '*minimum file size*'
        }

        It 'propagates non-ObjectNotFound errors from Test-Path (e.g. permission denied)' {
            Mock Test-Path {
                $exception = [System.UnauthorizedAccessException]::new('access denied')
                $errorRecord = [System.Management.Automation.ErrorRecord]::new(
                    $exception, 'AccessDenied',
                    [System.Management.Automation.ErrorCategory]::PermissionDenied, $LiteralPath)
                throw $errorRecord
            }
            Mock Resize-VHD { }

            $captured = $null
            try { Set-HypervVHD -Path 'C:\restricted.vhdx' -SizeBytes 1073741824 } catch { $captured = $_ }

            $captured | Should -Not -BeNullOrEmpty
            $captured.CategoryInfo.Category.ToString() | Should -Be 'PermissionDenied'
            Should -Invoke Resize-VHD -Times 0 -Exactly
        }
    }
}
