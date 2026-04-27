# Locks the JSON contract for New-HypervImageFile{FromUrl,FromHostPath}.
# URL mode: Save-HypervHttpFile to .part, hash check, atomic Move-Item;
# .part is cleaned up on every failure path. host_path mode: verify-only,
# no copy.

BeforeAll {
    . $PSScriptRoot/_test_helpers.ps1
    . $PSScriptRoot/../common/preamble.ps1
    . $PSScriptRoot/new.ps1
}

Describe 'New-HypervImageFileFromUrl' {

    Context 'happy path (url mode)' {

        It 'downloads to a sibling .part file in the destination directory' {
            # The .part lives next to the destination on purpose: NTFS Move-Item
            # is atomic only within a volume, so staging in the destination
            # directory keeps the rename atomic regardless of TEMP location.
            Mock Save-HypervHttpFile { }
            Mock Get-FileHash { New-HypervImageFileHashSample -Hash 'EXPECTED' }
            Mock Move-Item { }
            Mock Test-Path { $false }
            Mock Remove-Item { }
            Mock Get-Item { New-HypervImageFileSample }

            New-HypervImageFileFromUrl `
                -DestinationPath 'C:\images\ubuntu.vhdx' `
                -Url 'https://example.com/ubuntu.vhdx' `
                -ExpectedSha256 'expected' | Out-Null

            Should -Invoke Save-HypervHttpFile -Times 1 -Exactly -ParameterFilter {
                $Url -eq 'https://example.com/ubuntu.vhdx' -and
                $OutFile -like 'C:\images\ubuntu.vhdx.part-*'
            }
        }

        It 'atomic-renames the .part to the destination on hash match' {
            Mock Save-HypervHttpFile { }
            Mock Get-FileHash { New-HypervImageFileHashSample -Hash 'EXPECTED' }
            Mock Move-Item { }
            Mock Test-Path { $false }
            Mock Remove-Item { }
            Mock Get-Item { New-HypervImageFileSample }

            New-HypervImageFileFromUrl `
                -DestinationPath 'C:\images\ubuntu.vhdx' `
                -Url 'https://example.com/ubuntu.vhdx' `
                -ExpectedSha256 'expected' | Out-Null

            Should -Invoke Move-Item -Times 1 -Exactly -ParameterFilter {
                $LiteralPath -like 'C:\images\ubuntu.vhdx.part-*' -and
                $Destination -eq 'C:\images\ubuntu.vhdx' -and
                $Force -eq $true
            }
        }

        It 'compares hashes case-insensitively (lowercases both sides)' {
            # User-supplied checksums in the wild come in mixed case; canonical
            # form is lowercase. Both sides must lowercase before compare.
            Mock Save-HypervHttpFile { }
            Mock Get-FileHash { New-HypervImageFileHashSample -Hash 'ABCDEF' }
            Mock Move-Item { }
            Mock Test-Path { $false }
            Mock Remove-Item { }
            Mock Get-Item { New-HypervImageFileSample }

            { New-HypervImageFileFromUrl `
                -DestinationPath 'C:\images\ubuntu.vhdx' `
                -Url 'https://example.com/ubuntu.vhdx' `
                -ExpectedSha256 'abcdef' } | Should -Not -Throw

            Should -Invoke Move-Item -Times 1 -Exactly
        }

        It 'emits the canonical three-field shape after rename (matches get.ps1)' {
            Mock Save-HypervHttpFile { }
            Mock Get-FileHash { New-HypervImageFileHashSample -Hash 'EXPECTED' }
            Mock Move-Item { }
            Mock Test-Path { $false }
            Mock Remove-Item { }
            Mock Get-Item { New-HypervImageFileSample -FullName 'C:\images\ubuntu.vhdx' -Length 1234 }

            $parsed = New-HypervImageFileFromUrl `
                -DestinationPath 'C:\images\ubuntu.vhdx' `
                -Url 'https://example.com/ubuntu.vhdx' `
                -ExpectedSha256 'expected' | ConvertFrom-Json

            $parsed.PSObject.Properties.Name | Sort-Object | Should -Be @(
                'Path', 'Sha256', 'SizeBytes'
            )
            $parsed.Path      | Should -Be 'C:\images\ubuntu.vhdx'
            $parsed.SizeBytes | Should -Be 1234
            $parsed.Sha256    | Should -Be 'expected'
        }
    }

    Context 'error propagation (url mode)' {

        It 'throws InvalidData with ImageFileChecksumMismatch on hash mismatch (skips Move-Item, cleans up .part)' {
            # Asserts on CategoryInfo.Category because that's what the Go side
            # will key on for the typed ErrChecksumMismatch sentinel.
            Mock Save-HypervHttpFile { }
            Mock Get-FileHash { New-HypervImageFileHashSample -Hash 'WRONGHASH' }
            Mock Move-Item { }
            Mock Test-Path { $true }
            Mock Remove-Item { }
            Mock Get-Item { New-HypervImageFileSample }

            $captured = $null
            try {
                New-HypervImageFileFromUrl `
                    -DestinationPath 'C:\images\ubuntu.vhdx' `
                    -Url 'https://example.com/ubuntu.vhdx' `
                    -ExpectedSha256 'expected'
            } catch { $captured = $_ }

            $captured | Should -Not -BeNullOrEmpty
            $captured.CategoryInfo.Category.ToString() | Should -Be 'InvalidData'
            $captured.FullyQualifiedErrorId | Should -Match 'ImageFileChecksumMismatch'
            Should -Invoke Move-Item   -Times 0 -Exactly
            Should -Invoke Remove-Item -Times 1 -Exactly
        }

        It 'cleans up the .part file when the transport itself fails' {
            # The finally block must run on transport failure so a partial
            # download never lingers as a stale .part file.
            Mock Save-HypervHttpFile { throw 'simulated transport failure' }
            Mock Get-FileHash { }
            Mock Move-Item { }
            Mock Test-Path { $true }
            Mock Remove-Item { }
            Mock Get-Item { New-HypervImageFileSample }

            { New-HypervImageFileFromUrl `
                -DestinationPath 'C:\images\ubuntu.vhdx' `
                -Url 'https://example.com/ubuntu.vhdx' `
                -ExpectedSha256 'expected' } | Should -Throw -ExpectedMessage '*transport failure*'

            Should -Invoke Remove-Item -Times 1 -Exactly
            Should -Invoke Move-Item   -Times 0 -Exactly
        }

        It 'skips .part cleanup when Test-Path reports no file (already moved or never created)' {
            # Avoids a spurious Remove-Item invocation when the .part is gone --
            # both the success path (Move-Item consumed it) and the IWR-failed-
            # before-creating-the-file path land here.
            Mock Save-HypervHttpFile { }
            Mock Get-FileHash { New-HypervImageFileHashSample -Hash 'EXPECTED' }
            Mock Move-Item { }
            Mock Test-Path { $false }
            Mock Remove-Item { }
            Mock Get-Item { New-HypervImageFileSample }

            New-HypervImageFileFromUrl `
                -DestinationPath 'C:\images\ubuntu.vhdx' `
                -Url 'https://example.com/ubuntu.vhdx' `
                -ExpectedSha256 'expected' | Out-Null

            Should -Invoke Remove-Item -Times 0 -Exactly
        }
    }
}

Describe 'New-HypervImageFileFromHostPath' {

    Context 'happy path (host_path mode)' {

        It 'verifies the file exists and emits the canonical shape' {
            Mock Test-Path { $true }
            Mock Get-Item { New-HypervImageFileSample -FullName 'C:\share\foo.vhdx' -Length 5000 }
            Mock Get-FileHash { New-HypervImageFileHashSample -Hash 'EXPECTED' }

            $parsed = New-HypervImageFileFromHostPath -DestinationPath 'C:\share\foo.vhdx' | ConvertFrom-Json

            $parsed.Path      | Should -Be 'C:\share\foo.vhdx'
            $parsed.SizeBytes | Should -Be 5000
            $parsed.Sha256    | Should -Be 'expected'
        }

        It 'never invokes Save-HypervHttpFile or Move-Item (verify-only contract)' {
            # Locks the load-bearing semantic distinction between url and
            # host_path modes: host_path attests, it never copies or fetches.
            Mock Test-Path { $true }
            Mock Get-Item { New-HypervImageFileSample }
            Mock Get-FileHash { New-HypervImageFileHashSample }
            Mock Save-HypervHttpFile { }
            Mock Move-Item { }

            New-HypervImageFileFromHostPath -DestinationPath 'C:\share\foo.vhdx' | Out-Null

            Should -Invoke Save-HypervHttpFile -Times 0 -Exactly
            Should -Invoke Move-Item           -Times 0 -Exactly
        }
    }

    Context 'error propagation (host_path mode)' {

        It 'throws ObjectNotFound when the file is missing (no copy attempted)' {
            Mock Test-Path { $false }
            Mock Get-Item { }
            Mock Get-FileHash { }
            Mock Save-HypervHttpFile { }

            $captured = $null
            try { New-HypervImageFileFromHostPath -DestinationPath 'C:\nope.vhdx' } catch { $captured = $_ }

            $captured | Should -Not -BeNullOrEmpty
            $captured.CategoryInfo.Category.ToString() | Should -Be 'ObjectNotFound'
            $captured.FullyQualifiedErrorId | Should -Match 'ImageFileNotFound'
            Should -Invoke Save-HypervHttpFile -Times 0 -Exactly
        }

        It 'propagates non-ObjectNotFound errors from Test-Path (e.g. permission denied)' {
            # Same SilentlyContinue lesson as get/remove: a permission error
            # must NOT collapse into "missing" because the Go side would map
            # ObjectNotFound to RemoveResource on the next Read.
            Mock Test-Path {
                $exception = [System.UnauthorizedAccessException]::new('access denied')
                $errorRecord = [System.Management.Automation.ErrorRecord]::new(
                    $exception, 'AccessDenied',
                    [System.Management.Automation.ErrorCategory]::PermissionDenied, $LiteralPath)
                throw $errorRecord
            }

            $captured = $null
            try { New-HypervImageFileFromHostPath -DestinationPath 'C:\restricted.vhdx' } catch { $captured = $_ }

            $captured | Should -Not -BeNullOrEmpty
            $captured.CategoryInfo.Category.ToString() | Should -Be 'PermissionDenied'
        }
    }
}
