# Locks the JSON contract for Get-HypervImageFile. The Go-side typed wrapper
# decodes the output with field tags that match the keys asserted here; any
# change to those keys or types is a wire-level break.

BeforeAll {
    . $PSScriptRoot/_test_helpers.ps1
    . $PSScriptRoot/../common/preamble.ps1
    . $PSScriptRoot/get.ps1
}

Describe 'Get-HypervImageFile' {

    Context 'happy path' {

        It 'emits the canonical three-field shape' {
            Mock Test-Path { $true }
            Mock Get-Item { New-HypervImageFileSample }
            Mock Get-FileHash { New-HypervImageFileHashSample }

            $parsed = Get-HypervImageFile -Path 'C:\images\foo.vhdx' | ConvertFrom-Json

            $parsed.PSObject.Properties.Name | Sort-Object | Should -Be @(
                'Path', 'Sha256', 'SizeBytes'
            )
        }

        It 'lowercases the SHA-256 hex (canonical comparison form)' {
            # Get-FileHash returns Hash uppercased; the wire contract is lowercase
            # so Go-side string comparison against an expected hash is trivial.
            Mock Test-Path { $true }
            Mock Get-Item { New-HypervImageFileSample }
            Mock Get-FileHash {
                New-HypervImageFileHashSample -Hash 'ABCDEF0123456789ABCDEF0123456789ABCDEF0123456789ABCDEF0123456789'
            }

            $parsed = Get-HypervImageFile -Path 'C:\images\foo.vhdx' | ConvertFrom-Json
            $parsed.Sha256 | Should -Be 'abcdef0123456789abcdef0123456789abcdef0123456789abcdef0123456789'
        }

        It 'preserves SizeBytes as int64 (large files exceed int32)' {
            Mock Test-Path { $true }
            Mock Get-Item { New-HypervImageFileSample -Length 5368709120 } # 5 GiB
            Mock Get-FileHash { New-HypervImageFileHashSample }

            $parsed = Get-HypervImageFile -Path 'C:\images\big.vhdx' | ConvertFrom-Json
            $parsed.SizeBytes | Should -Be 5368709120
        }

        It 'forwards the path verbatim to Test-Path / Get-Item / Get-FileHash' {
            Mock Test-Path { $true }
            Mock Get-Item { New-HypervImageFileSample }
            Mock Get-FileHash { New-HypervImageFileHashSample }

            Get-HypervImageFile -Path 'C:\custom\path.iso' | Out-Null

            Should -Invoke Test-Path  -Times 1 -Exactly -ParameterFilter {
                $LiteralPath -eq 'C:\custom\path.iso' -and $PathType -eq 'Leaf'
            }
            Should -Invoke Get-Item   -Times 1 -Exactly -ParameterFilter { $LiteralPath -eq 'C:\custom\path.iso' }
            Should -Invoke Get-FileHash -Times 1 -Exactly -ParameterFilter {
                $LiteralPath -eq 'C:\custom\path.iso' -and $Algorithm -eq 'SHA256'
            }
        }

        It 'compresses output to a single line (Write-HypervResult contract)' {
            Mock Test-Path { $true }
            Mock Get-Item { New-HypervImageFileSample }
            Mock Get-FileHash { New-HypervImageFileHashSample }

            $output = Get-HypervImageFile -Path 'C:\images\foo.vhdx'
            $output | Should -BeOfType [string]
            ($output -split "`n" | Measure-Object).Count | Should -Be 1
        }
    }

    Context 'error propagation' {

        It 'throws ObjectNotFound when the file is missing (skips Get-FileHash)' {
            # Asserts on CategoryInfo.Category because that's what the Go side
            # maps to ErrNotFound. ErrorId drift wouldn't change behavior; a
            # category drift would silently mis-route the typed error.
            Mock Test-Path { $false }
            Mock Get-Item { New-HypervImageFileSample }
            Mock Get-FileHash { New-HypervImageFileHashSample }

            $captured = $null
            try { Get-HypervImageFile -Path 'C:\nope.vhdx' } catch { $captured = $_ }

            $captured | Should -Not -BeNullOrEmpty
            $captured.CategoryInfo.Category.ToString() | Should -Be 'ObjectNotFound'
            $captured.FullyQualifiedErrorId | Should -Match 'ImageFileNotFound'
            Should -Invoke Get-FileHash -Times 0 -Exactly
        }

        It 'propagates non-ObjectNotFound errors from Test-Path (e.g. permission denied)' {
            # Locks the SilentlyContinue lesson from vswitch: a permission
            # error or transient IO fault must NOT collapse into the missing-file
            # branch because the Go side maps ObjectNotFound to RemoveResource
            # and would silently drop the resource from state.
            Mock Test-Path {
                $exception = [System.UnauthorizedAccessException]::new('access denied')
                $errorRecord = [System.Management.Automation.ErrorRecord]::new(
                    $exception, 'AccessDenied',
                    [System.Management.Automation.ErrorCategory]::PermissionDenied, $LiteralPath)
                throw $errorRecord
            }

            $captured = $null
            try { Get-HypervImageFile -Path 'C:\restricted.vhdx' } catch { $captured = $_ }

            $captured | Should -Not -BeNullOrEmpty
            $captured.CategoryInfo.Category.ToString() | Should -Be 'PermissionDenied'
        }

        It 'propagates Get-FileHash errors (e.g. file disappeared mid-read)' {
            Mock Test-Path { $true }
            Mock Get-Item { New-HypervImageFileSample }
            Mock Get-FileHash { throw 'simulated IO fault' }

            { Get-HypervImageFile -Path 'C:\images\foo.vhdx' } |
                Should -Throw -ExpectedMessage '*IO fault*'
        }
    }
}
