# Locks the JSON contract for Get-HypervVHD. The Go-side typed wrapper
# decodes the output with field tags that match the keys asserted here;
# any change to those keys or types is a wire-level break.

BeforeAll {
    . $PSScriptRoot/_test_helpers.ps1
    . $PSScriptRoot/../common/preamble.ps1
    . $PSScriptRoot/get.ps1
}

Describe 'Get-HypervVHD' {

    Context 'happy path' {

        It 'emits the canonical eight-field shape' {
            Mock Test-Path { $true }
            Mock Get-VHD { New-HypervVHDSample }

            $parsed = Get-HypervVHD -Path 'C:\vhds\foo.vhdx' | ConvertFrom-Json

            $parsed.PSObject.Properties.Name | Sort-Object | Should -Be @(
                'Attached', 'BlockSizeBytes', 'FileSizeBytes', 'Format',
                'ParentPath', 'Path', 'SizeBytes', 'VhdType'
            )
        }

        It 'stringifies the VhdType enum' {
            Mock Test-Path { $true }
            Mock Get-VHD { New-HypervVHDSample -VhdType 'Fixed' }

            $parsed = Get-HypervVHD -Path 'C:\vhds\foo.vhdx' | ConvertFrom-Json
            $parsed.VhdType | Should -BeOfType [string]
            $parsed.VhdType | Should -Be 'Fixed'
        }

        It 'stringifies the VhdFormat enum (VHD vs VHDX, uppercase per Get-VHD)' {
            Mock Test-Path { $true }
            Mock Get-VHD { New-HypervVHDSample -VhdFormat 'VHD' }

            $parsed = Get-HypervVHD -Path 'C:\vhds\legacy.vhd' | ConvertFrom-Json
            $parsed.Format | Should -Be 'VHD'
        }

        It 'preserves SizeBytes as int64 (multi-GiB disks exceed int32)' {
            Mock Test-Path { $true }
            Mock Get-VHD { New-HypervVHDSample -Size 5368709120 -FileSize 1073741824 }

            $parsed = Get-HypervVHD -Path 'C:\vhds\big.vhdx' | ConvertFrom-Json
            $parsed.SizeBytes     | Should -Be 5368709120
            $parsed.FileSizeBytes | Should -Be 1073741824
        }

        It 'preserves the parent path on differencing disks' {
            Mock Test-Path { $true }
            Mock Get-VHD { New-HypervVHDSample -VhdType 'Differencing' -ParentPath 'C:\vhds\parent.vhdx' }

            $parsed = Get-HypervVHD -Path 'C:\vhds\child.vhdx' | ConvertFrom-Json
            $parsed.ParentPath | Should -Be 'C:\vhds\parent.vhdx'
            $parsed.VhdType    | Should -Be 'Differencing'
        }

        It 'reports Attached=true when the disk is in use by a VM' {
            Mock Test-Path { $true }
            Mock Get-VHD { New-HypervVHDSample -Attached $true }

            $parsed = Get-HypervVHD -Path 'C:\vhds\inuse.vhdx' | ConvertFrom-Json
            $parsed.Attached | Should -BeTrue
        }

        It 'forwards the path verbatim to Test-Path / Get-VHD' {
            Mock Test-Path { $true }
            Mock Get-VHD { New-HypervVHDSample }

            Get-HypervVHD -Path 'C:\custom\path.vhdx' | Out-Null

            Should -Invoke Test-Path -Times 1 -Exactly -ParameterFilter {
                $LiteralPath -eq 'C:\custom\path.vhdx' -and $PathType -eq 'Leaf'
            }
            Should -Invoke Get-VHD -Times 1 -Exactly -ParameterFilter {
                $Path -eq 'C:\custom\path.vhdx'
            }
        }

        It 'compresses output to a single line (Write-HypervResult contract)' {
            Mock Test-Path { $true }
            Mock Get-VHD { New-HypervVHDSample }

            $output = Get-HypervVHD -Path 'C:\vhds\foo.vhdx'
            $output | Should -BeOfType [string]
            ($output -split "`n" | Measure-Object).Count | Should -Be 1
        }
    }

    Context 'error propagation' {

        It 'throws ObjectNotFound when the file is missing (skips Get-VHD)' {
            # Asserts on CategoryInfo.Category because that's what the Go side
            # maps to ErrNotFound. ErrorId drift wouldn't change behavior; a
            # category drift would silently mis-route the typed error.
            Mock Test-Path { $false }
            Mock Get-VHD { New-HypervVHDSample }

            $captured = $null
            try { Get-HypervVHD -Path 'C:\nope.vhdx' } catch { $captured = $_ }

            $captured | Should -Not -BeNullOrEmpty
            $captured.CategoryInfo.Category.ToString() | Should -Be 'ObjectNotFound'
            $captured.FullyQualifiedErrorId | Should -Match 'VHDNotFound'
            Should -Invoke Get-VHD -Times 0 -Exactly
        }

        It 'propagates non-ObjectNotFound errors from Test-Path (e.g. permission denied)' {
            # Locks the SilentlyContinue lesson: a permission error or
            # transient IO fault must NOT collapse into "missing" because the
            # Go side maps ObjectNotFound to RemoveResource and would silently
            # drop the resource from state.
            Mock Test-Path {
                $exception = [System.UnauthorizedAccessException]::new('access denied')
                $errorRecord = [System.Management.Automation.ErrorRecord]::new(
                    $exception, 'AccessDenied',
                    [System.Management.Automation.ErrorCategory]::PermissionDenied, $LiteralPath)
                throw $errorRecord
            }

            $captured = $null
            try { Get-HypervVHD -Path 'C:\restricted.vhdx' } catch { $captured = $_ }

            $captured | Should -Not -BeNullOrEmpty
            $captured.CategoryInfo.Category.ToString() | Should -Be 'PermissionDenied'
        }

        It 'propagates Get-VHD errors (e.g. file is not a valid VHD)' {
            # Test-Path passes (file exists) but Get-VHD rejects it -- could
            # be a non-VHD with the right extension, corrupt header, etc.
            # Surfaces as ErrPSExecution on the Go side.
            Mock Test-Path { $true }
            Mock Get-VHD { throw 'Not a recognized VHD format' }

            { Get-HypervVHD -Path 'C:\vhds\corrupt.vhdx' } |
                Should -Throw -ExpectedMessage '*Not a recognized VHD format*'
        }
    }
}
