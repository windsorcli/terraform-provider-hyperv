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

Describe 'New-HypervImageFileFromLocalPath' {

    Context 'happy path (local_path mode)' {

        It 'verifies the staging file SHA against the expected hash before renaming' {
            # The bytes were streamed by the Go side; the only check this
            # script performs is that what landed on disk matches what the
            # runner thinks it sent. Same pattern as url-mode's hash check,
            # different source.
            Mock Test-Path { $true }
            Mock Get-FileHash { New-HypervImageFileHashSample -Hash 'EXPECTED' }
            Mock Move-Item { }
            Mock Remove-Item { }
            Mock Get-Item { New-HypervImageFileSample }

            New-HypervImageFileFromLocalPath `
                -DestinationPath 'C:\images\ubuntu.vhdx' `
                -StagingPath     'C:\images\ubuntu.vhdx.part-abc123' `
                -ExpectedSha256  'expected' | Out-Null

            Should -Invoke Get-FileHash -Times 1 -Exactly -ParameterFilter {
                $LiteralPath -eq 'C:\images\ubuntu.vhdx.part-abc123' -and
                $Algorithm -eq 'SHA256'
            }
        }

        It 'atomic-renames the staging file to the destination on hash match' {
            Mock Test-Path { $true }
            Mock Get-FileHash { New-HypervImageFileHashSample -Hash 'EXPECTED' }
            Mock Move-Item { }
            Mock Remove-Item { }
            Mock Get-Item { New-HypervImageFileSample }

            New-HypervImageFileFromLocalPath `
                -DestinationPath 'C:\images\ubuntu.vhdx' `
                -StagingPath     'C:\images\ubuntu.vhdx.part-abc123' `
                -ExpectedSha256  'expected' | Out-Null

            Should -Invoke Move-Item -Times 1 -Exactly -ParameterFilter {
                $LiteralPath -eq 'C:\images\ubuntu.vhdx.part-abc123' -and
                $Destination -eq 'C:\images\ubuntu.vhdx' -and
                $Force -eq $true
            }
        }

        It 'compares hashes case-insensitively (lowercases both sides)' {
            Mock Test-Path { $true }
            Mock Get-FileHash { New-HypervImageFileHashSample -Hash 'ABCDEF' }
            Mock Move-Item { }
            Mock Remove-Item { }
            Mock Get-Item { New-HypervImageFileSample }

            { New-HypervImageFileFromLocalPath `
                -DestinationPath 'C:\images\ubuntu.vhdx' `
                -StagingPath     'C:\images\ubuntu.vhdx.part-abc123' `
                -ExpectedSha256  'abcdef' } | Should -Not -Throw

            Should -Invoke Move-Item -Times 1 -Exactly
        }

        It 'never invokes Save-HypervHttpFile (no fetch happens in local_path mode)' {
            # Locks the load-bearing distinction between url and local_path
            # modes: url fetches over HTTP, local_path verifies bytes the
            # Go-side stream already deposited.
            Mock Test-Path { $true }
            Mock Get-FileHash { New-HypervImageFileHashSample -Hash 'EXPECTED' }
            Mock Move-Item { }
            Mock Remove-Item { }
            Mock Get-Item { New-HypervImageFileSample }
            Mock Save-HypervHttpFile { }

            New-HypervImageFileFromLocalPath `
                -DestinationPath 'C:\images\ubuntu.vhdx' `
                -StagingPath     'C:\images\ubuntu.vhdx.part-abc123' `
                -ExpectedSha256  'expected' | Out-Null

            Should -Invoke Save-HypervHttpFile -Times 0 -Exactly
        }

        It 'emits the canonical three-field shape after rename (matches get.ps1)' {
            Mock Test-Path { $true }
            Mock Get-FileHash { New-HypervImageFileHashSample -Hash 'EXPECTED' }
            Mock Move-Item { }
            Mock Remove-Item { }
            Mock Get-Item { New-HypervImageFileSample -FullName 'C:\images\ubuntu.vhdx' -Length 5678 }

            $parsed = New-HypervImageFileFromLocalPath `
                -DestinationPath 'C:\images\ubuntu.vhdx' `
                -StagingPath     'C:\images\ubuntu.vhdx.part-abc123' `
                -ExpectedSha256  'expected' | ConvertFrom-Json

            $parsed.PSObject.Properties.Name | Sort-Object | Should -Be @(
                'Path', 'Sha256', 'SizeBytes'
            )
            $parsed.Path      | Should -Be 'C:\images\ubuntu.vhdx'
            $parsed.SizeBytes | Should -Be 5678
            $parsed.Sha256    | Should -Be 'expected'
        }
    }

    Context 'error propagation (local_path mode)' {

        It 'throws InvalidData with ImageFileChecksumMismatch on hash mismatch (skips Move-Item, cleans up staging)' {
            # Transport corruption between runner and host -- the bytes
            # that landed don't match what the runner thinks it sent.
            # Same diagnostic shape as url-mode so the Go side can map
            # both paths to ErrChecksumMismatch through one rule.
            Mock Test-Path {
                # First call (mode-entry presence check) returns true; the
                # finally-block cleanup check also returns true so we see
                # Remove-Item invoked.
                $true
            }
            Mock Get-FileHash { New-HypervImageFileHashSample -Hash 'WRONGHASH' }
            Mock Move-Item { }
            Mock Remove-Item { }
            Mock Get-Item { New-HypervImageFileSample }

            $captured = $null
            try {
                New-HypervImageFileFromLocalPath `
                    -DestinationPath 'C:\images\ubuntu.vhdx' `
                    -StagingPath     'C:\images\ubuntu.vhdx.part-abc123' `
                    -ExpectedSha256  'expected'
            } catch { $captured = $_ }

            $captured | Should -Not -BeNullOrEmpty
            $captured.CategoryInfo.Category.ToString() | Should -Be 'InvalidData'
            $captured.FullyQualifiedErrorId | Should -Match 'ImageFileChecksumMismatch'
            Should -Invoke Move-Item   -Times 0 -Exactly
            Should -Invoke Remove-Item -Times 1 -Exactly
        }

        It 'throws ObjectNotFound when the staging file is missing (no hash check, no rename)' {
            # The Go side promised to stream a file before invoking this
            # script; if it's not there, that's a transport bug worth
            # surfacing loudly rather than silently no-op'ing.
            Mock Test-Path { $false }
            Mock Get-FileHash { }
            Mock Move-Item { }
            Mock Remove-Item { }

            $captured = $null
            try {
                New-HypervImageFileFromLocalPath `
                    -DestinationPath 'C:\images\ubuntu.vhdx' `
                    -StagingPath     'C:\images\ubuntu.vhdx.part-abc123' `
                    -ExpectedSha256  'expected'
            } catch { $captured = $_ }

            $captured | Should -Not -BeNullOrEmpty
            $captured.CategoryInfo.Category.ToString() | Should -Be 'ObjectNotFound'
            $captured.FullyQualifiedErrorId | Should -Match 'ImageFileStagingNotFound'
            Should -Invoke Get-FileHash -Times 0 -Exactly
            Should -Invoke Move-Item    -Times 0 -Exactly
        }

        It 'skips staging cleanup when Test-Path reports no file (already moved or never created)' {
            # Test-Path returns true for the entry-block presence check
            # and false for the finally-block cleanup probe -- means the
            # successful Move-Item consumed the staging file.
            $script:testPathCalls = 0
            Mock Test-Path {
                $script:testPathCalls++
                if ($script:testPathCalls -eq 1) { $true } else { $false }
            }
            Mock Get-FileHash { New-HypervImageFileHashSample -Hash 'EXPECTED' }
            Mock Move-Item { }
            Mock Remove-Item { }
            Mock Get-Item { New-HypervImageFileSample }

            New-HypervImageFileFromLocalPath `
                -DestinationPath 'C:\images\ubuntu.vhdx' `
                -StagingPath     'C:\images\ubuntu.vhdx.part-abc123' `
                -ExpectedSha256  'expected' | Out-Null

            Should -Invoke Remove-Item -Times 0 -Exactly
        }
    }
}
