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

    BeforeEach { Mock New-Item { } }

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

        It 'skips checksum verification and renames when ExpectedSha256 is empty' {
            # Empty ExpectedSha256 is the TLS-only-trust path: no compare against
            # the .part file's hash. Get-FileHash is still called once by
            # Read-HypervImageFileResult to surface the on-disk SHA for drift
            # detection -- but not a *second* time for verification, which is
            # the path being skipped.
            Mock Save-HypervHttpFile { }
            Mock Get-FileHash { New-HypervImageFileHashSample -Hash 'whatever' }
            Mock Move-Item { }
            Mock Test-Path { $false }
            Mock Remove-Item { }
            Mock Get-Item { New-HypervImageFileSample }

            { New-HypervImageFileFromUrl `
                -DestinationPath 'C:\images\ubuntu.vhdx' `
                -Url 'https://example.com/ubuntu.vhdx' `
                -ExpectedSha256 '' } | Should -Not -Throw

            Should -Invoke Get-FileHash -Times 1 -Exactly
            Should -Invoke Move-Item    -Times 1 -Exactly
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

    Context 'parent directory creation (url mode)' {

        It 'creates the parent directory of the destination path before writing the .part file' {
            # New-Item -Force is a no-op if the directory already exists, so this
            # call is safe on first-apply (missing dir) and subsequent applies
            # (dir present). The test uses Split-Path on the same literal path so
            # the expected-dir computation is platform-consistent across Mac (where
            # backslash paths have no directory component) and Windows (where they do).
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

            $expectedDir = Split-Path -LiteralPath 'C:\images\ubuntu.vhdx'
            Should -Invoke New-Item -Times 1 -Exactly -ParameterFilter {
                $ItemType -eq 'Directory' -and $Force -eq $true -and $Path -eq $expectedDir
            }
        }

        It 'does not throw when the parent directory already exists' {
            Mock Save-HypervHttpFile { }
            Mock Get-FileHash { New-HypervImageFileHashSample -Hash 'EXPECTED' }
            Mock Move-Item { }
            Mock Test-Path { $false }
            Mock Remove-Item { }
            Mock Get-Item { New-HypervImageFileSample }

            { New-HypervImageFileFromUrl `
                -DestinationPath 'C:\images\ubuntu.vhdx' `
                -Url 'https://example.com/ubuntu.vhdx' `
                -ExpectedSha256 'expected' } | Should -Not -Throw
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

        It 'never creates the parent directory (verify-only: path must already be complete)' {
            # host_path mode attests that the file exists at the declared path;
            # silently creating the parent directory would be misleading -- if
            # the directory is missing the file can't exist there either, and the
            # ObjectNotFound error from Test-Path is the correct signal.
            Mock Test-Path { $true }
            Mock Get-Item { New-HypervImageFileSample }
            Mock Get-FileHash { New-HypervImageFileHashSample }
            Mock New-Item { }

            New-HypervImageFileFromHostPath -DestinationPath 'C:\share\foo.vhdx' | Out-Null

            Should -Invoke New-Item -Times 0 -Exactly
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

    BeforeEach { Mock New-Item { } }

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

    Context 'replace-while-mounted mode (ReplaceWhileMounted switch)' {
        # iso_volume sets this flag because cidata seeds may be mounted as
        # a DVD on a running VM. Move-Item -Force against a destination
        # Hyper-V holds an exclusive open handle on surfaces "Cannot
        # create a file when that file already exists." The fix is the
        # swap-via-pivot dance in Invoke-HypervDvdSafeReplace: rename
        # staging to a sibling pivot, point each matching DVD slot at the
        # pivot (releasing the lock on the destination), Copy-Item the
        # pivot bytes to the destination, point each slot back, remove
        # the pivot. Every Set-VMDvdDrive call has a real existing path,
        # which avoids the bench-observed "object not found" failure
        # mode of Set-VMDvdDrive -Path $null.
        #
        # All cases stub Get-FileHash to match expected_sha256 so the
        # dance under test runs; the hash-mismatch path stays in the
        # previous context and is not duplicated here.

        It 'when no VM mounts the destination: Move-Item runs once, no DVD calls' {
            # No-attachment branch must be a uniform fall-through to the
            # plain Move-Item path -- this is the case for an iso_volume
            # whose seed has not yet been wired into any VM (a Create on
            # an unattached path). Both no-VMs and VMs-with-no-DVDs land
            # here; the empty Get-VMDvdDrive return covers both.
            Mock Test-Path { $true }
            Mock Get-FileHash { New-HypervImageFileHashSample -Hash 'EXPECTED' }
            Mock Move-Item { }
            Mock Copy-Item { }
            Mock Remove-Item { }
            Mock Get-Item { New-HypervImageFileSample }
            Mock Get-VM { @() }
            Mock Get-VMDvdDrive { @() }
            Mock Set-VMDvdDrive { }

            New-HypervImageFileFromLocalPath `
                -DestinationPath                 'C:\hyperv\seeds\cidata.iso' `
                -StagingPath                     'C:\hyperv\seeds\cidata.iso.part-abc' `
                -ExpectedSha256                  'expected' `
                -ReplaceWhileMounted | Out-Null

            Should -Invoke Get-VM         -Times 1 -Exactly
            Should -Invoke Set-VMDvdDrive -Times 0 -Exactly
            Should -Invoke Copy-Item      -Times 0 -Exactly
            Should -Invoke Move-Item      -Times 1 -Exactly -ParameterFilter {
                $LiteralPath -eq 'C:\hyperv\seeds\cidata.iso.part-abc' -and
                $Destination -eq 'C:\hyperv\seeds\cidata.iso' -and
                $Force -eq $true
            }
        }

        It 'swaps DVD media via pivot in the documented order' {
            # The load-bearing assertion: each Set-VMDvdDrive points at a
            # real existing path (never $null), the Copy-Item lands on
            # $DestinationPath after the slots are pointed at the pivot,
            # and the slots are pointed back at $DestinationPath after
            # the copy. A regression that broke the ordering would
            # either (a) try to copy onto a still-locked destination
            # (collision returns), or (b) leave VMs mounting the pivot
            # after the script returns (cleanup deletes the pivot, VMs
            # see a missing-file error on next read).
            $script:order = @()
            Mock Test-Path { $true }
            Mock Get-FileHash { New-HypervImageFileHashSample -Hash 'EXPECTED' }
            Mock Move-Item {
                $script:order += "move:$LiteralPath->$Destination"
            }
            Mock Copy-Item {
                $script:order += "copy:$LiteralPath->$Destination"
            }
            Mock Remove-Item { $script:order += "remove:$LiteralPath" }
            Mock Get-Item { New-HypervImageFileSample }
            Mock Get-VM {
                @(
                    New-HypervImageFileVMSample -Name 'vm-a'
                    New-HypervImageFileVMSample -Name 'vm-b'
                )
            }
            Mock Get-VMDvdDrive {
                if ($VMName -eq 'vm-a') {
                    @( New-HypervImageFileVMDvdDriveSample `
                        -VMName 'vm-a' -ControllerNumber 0 -ControllerLocation 1 `
                        -Path   'C:\hyperv\seeds\cidata.iso' )
                } elseif ($VMName -eq 'vm-b') {
                    @( New-HypervImageFileVMDvdDriveSample `
                        -VMName 'vm-b' -ControllerNumber 1 -ControllerLocation 0 `
                        -Path   'C:\hyperv\seeds\cidata.iso' )
                }
            }
            Mock Set-VMDvdDrive {
                # The mock records the slot identifier and the path's
                # role: 'pivot' if the path matches the .swap- pattern,
                # 'dest' if it matches $DestinationPath. A regression
                # that called Set-VMDvdDrive with $null (or no path)
                # would record 'unknown' and fail the order match.
                $role = if ($Path -like '*.swap-*') { 'pivot' }
                        elseif ($Path -eq 'C:\hyperv\seeds\cidata.iso') { 'dest' }
                        else { "unknown:$Path" }
                $script:order += "set:${VMName}:${ControllerNumber}:${ControllerLocation}:$role"
            }

            New-HypervImageFileFromLocalPath `
                -DestinationPath                 'C:\hyperv\seeds\cidata.iso' `
                -StagingPath                     'C:\hyperv\seeds\cidata.iso.part-abc' `
                -ExpectedSha256                  'expected' `
                -ReplaceWhileMounted | Out-Null

            # Strip the random pivot guid (and the trailing .iso the
            # cmdlet validator requires) from move/copy entries before
            # comparing so the assertion is stable across runs. The
            # ordering check is what the test is for; the pivot's exact
            # name is an implementation detail.
            $cleaned = $script:order | ForEach-Object { $_ -replace '\.swap-[0-9a-f]+\.iso', '.swap-XXX' }

            $cleaned | Should -Be @(
                'move:C:\hyperv\seeds\cidata.iso.part-abc->C:\hyperv\seeds\cidata.iso.swap-XXX'
                'set:vm-a:0:1:pivot'
                'set:vm-b:1:0:pivot'
                'copy:C:\hyperv\seeds\cidata.iso.swap-XXX->C:\hyperv\seeds\cidata.iso'
                'set:vm-a:0:1:dest'
                'set:vm-b:1:0:dest'
                'remove:C:\hyperv\seeds\cidata.iso.swap-XXX'
                # Trailing remove is the outer New-HypervImageFileFromLocalPath
                # finally block sweeping the staging file. Test-Path is mocked
                # to always return $true, so Remove-Item runs even though in
                # production the successful Move-Item already consumed the
                # staging file. Keeping this entry in the assertion makes the
                # test honest about what calls land.
                'remove:C:\hyperv\seeds\cidata.iso.part-abc'
            )
        }

        It 'Set-VMDvdDrive restore uses backslash-normalized destination form' {
            # Hyper-V canonicalizes -Path into its storage layer; passing
            # the user's forward-slash form (e.g. C:/hyperv/...) has been
            # observed to land as an empty Path on the slot. Lock the
            # backslash form here so a regression that drops the
            # normalization surfaces in unit tests, not on the bench.
            Mock Test-Path { $true }
            Mock Get-FileHash { New-HypervImageFileHashSample -Hash 'EXPECTED' }
            Mock Move-Item { }
            Mock Copy-Item { }
            Mock Remove-Item { }
            Mock Get-Item { New-HypervImageFileSample }
            Mock Get-VM { @( New-HypervImageFileVMSample -Name 'vm-a' ) }
            Mock Get-VMDvdDrive {
                @( New-HypervImageFileVMDvdDriveSample `
                    -VMName 'vm-a' -ControllerNumber 0 -ControllerLocation 1 `
                    -Path   'C:\hyperv\seeds\cidata.iso' )
            }
            Mock Set-VMDvdDrive { }

            New-HypervImageFileFromLocalPath `
                -DestinationPath                 'C:/hyperv/seeds/cidata.iso' `
                -StagingPath                     'C:/hyperv/seeds/cidata.iso.part-abc' `
                -ExpectedSha256                  'expected' `
                -ReplaceWhileMounted | Out-Null

            # Final restore must use backslash form (C:\...), not the
            # user-supplied forward-slash form.
            Should -Invoke Set-VMDvdDrive -Times 1 -Exactly -ParameterFilter {
                $Path -eq 'C:\hyperv\seeds\cidata.iso'
            }
        }

        It 'restores attachments to destination even when Copy-Item throws' {
            # If Copy-Item fails after the slots are pointed at the pivot
            # (disk full, transient AV scan, antivirus quarantine),
            # the finally block must still re-target the slots back to
            # $DestinationPath -- otherwise the post-failure VM has its
            # DVD pointed at a soon-deleted pivot. The restore is
            # SilentlyContinue so a missing destination doesn't shadow
            # the original Copy-Item error.
            $script:restoreToDest = 0
            Mock Test-Path { $true }
            Mock Get-FileHash { New-HypervImageFileHashSample -Hash 'EXPECTED' }
            Mock Move-Item { }
            Mock Copy-Item { throw 'simulated copy failure' }
            Mock Remove-Item { }
            Mock Get-Item { New-HypervImageFileSample }
            Mock Get-VM { @( New-HypervImageFileVMSample -Name 'vm-a' ) }
            Mock Get-VMDvdDrive {
                @( New-HypervImageFileVMDvdDriveSample `
                    -VMName 'vm-a' -ControllerNumber 0 -ControllerLocation 1 `
                    -Path   'C:\hyperv\seeds\cidata.iso' )
            }
            Mock Set-VMDvdDrive {
                if ($Path -eq 'C:\hyperv\seeds\cidata.iso') {
                    $script:restoreToDest++
                }
            }

            { New-HypervImageFileFromLocalPath `
                -DestinationPath                 'C:\hyperv\seeds\cidata.iso' `
                -StagingPath                     'C:\hyperv\seeds\cidata.iso.part-abc' `
                -ExpectedSha256                  'expected' `
                -ReplaceWhileMounted } | Should -Throw -ExpectedMessage '*simulated copy failure*'

            # The single slot got pointed back at the destination once
            # via the finally block. Without that, the post-fail VM
            # would mount the about-to-be-removed pivot.
            $script:restoreToDest | Should -Be 1
        }

        It 'matches paths case-insensitively and normalizes forward slashes' {
            # Hyper-V returns canonical backslash form, but a forward-
            # slash destination_path (HCL ergonomics on the user side)
            # plus a mixed-case path comparison must still find the
            # attachment. Match failure here would silently skip the
            # swap and the apply would hit the original lock failure.
            Mock Test-Path { $true }
            Mock Get-FileHash { New-HypervImageFileHashSample -Hash 'EXPECTED' }
            Mock Move-Item { }
            Mock Copy-Item { }
            Mock Remove-Item { }
            Mock Get-Item { New-HypervImageFileSample }
            Mock Get-VM { @( New-HypervImageFileVMSample -Name 'vm-a' ) }
            Mock Get-VMDvdDrive {
                @( New-HypervImageFileVMDvdDriveSample `
                    -VMName 'vm-a' -ControllerNumber 0 -ControllerLocation 1 `
                    -Path   'C:\HyperV\Seeds\CIDATA.iso' )
            }
            Mock Set-VMDvdDrive { }

            New-HypervImageFileFromLocalPath `
                -DestinationPath                 'C:/hyperv/seeds/cidata.iso' `
                -StagingPath                     'C:/hyperv/seeds/cidata.iso.part-abc' `
                -ExpectedSha256                  'expected' `
                -ReplaceWhileMounted | Out-Null

            # Two Set-VMDvdDrive calls: one to pivot, one back to dest.
            Should -Invoke Set-VMDvdDrive -Times 2 -Exactly
        }

        It 'skips DVDs whose Path is null or empty (unmounted slot)' {
            # A VM with a DvdDrive controller location that has no media
            # mounted has Path=$null. Treating that as a match would call
            # Set-VMDvdDrive on a slot the user did not reference, which
            # would then attach our destination after the swap -- a
            # silent footgun.
            Mock Test-Path { $true }
            Mock Get-FileHash { New-HypervImageFileHashSample -Hash 'EXPECTED' }
            Mock Move-Item { }
            Mock Copy-Item { }
            Mock Remove-Item { }
            Mock Get-Item { New-HypervImageFileSample }
            Mock Get-VM {
                @(
                    New-HypervImageFileVMSample -Name 'vm-empty-slot'
                    New-HypervImageFileVMSample -Name 'vm-other-iso'
                )
            }
            Mock Get-VMDvdDrive {
                if ($VMName -eq 'vm-empty-slot') {
                    @( New-HypervImageFileVMDvdDriveSample `
                        -VMName 'vm-empty-slot' -ControllerNumber 0 -ControllerLocation 1 `
                        -Path   $null )
                } elseif ($VMName -eq 'vm-other-iso') {
                    @( New-HypervImageFileVMDvdDriveSample `
                        -VMName 'vm-other-iso' -ControllerNumber 0 -ControllerLocation 1 `
                        -Path   'C:\hyperv\seeds\unrelated.iso' )
                }
            }
            Mock Set-VMDvdDrive { }

            New-HypervImageFileFromLocalPath `
                -DestinationPath                 'C:\hyperv\seeds\cidata.iso' `
                -StagingPath                     'C:\hyperv\seeds\cidata.iso.part-abc' `
                -ExpectedSha256                  'expected' `
                -ReplaceWhileMounted | Out-Null

            Should -Invoke Set-VMDvdDrive -Times 0 -Exactly
        }

        It 'when the switch is absent (default): Move-Item runs directly, no DVD enumeration' {
            # The flag must be opt-in. image_file's url and local_path
            # paths do not set it; their callers should see exactly the
            # legacy behavior (no Get-VM, no Get-VMDvdDrive, no
            # Set-VMDvdDrive, no Copy-Item -- just Move-Item).
            Mock Test-Path { $true }
            Mock Get-FileHash { New-HypervImageFileHashSample -Hash 'EXPECTED' }
            Mock Move-Item { }
            Mock Copy-Item { }
            Mock Remove-Item { }
            Mock Get-Item { New-HypervImageFileSample }
            Mock Get-VM { }
            Mock Get-VMDvdDrive { }
            Mock Set-VMDvdDrive { }

            New-HypervImageFileFromLocalPath `
                -DestinationPath 'C:\images\ubuntu.vhdx' `
                -StagingPath     'C:\images\ubuntu.vhdx.part-abc' `
                -ExpectedSha256  'expected' | Out-Null

            Should -Invoke Get-VM         -Times 0 -Exactly
            Should -Invoke Get-VMDvdDrive -Times 0 -Exactly
            Should -Invoke Set-VMDvdDrive -Times 0 -Exactly
            Should -Invoke Copy-Item      -Times 0 -Exactly
            Should -Invoke Move-Item      -Times 1 -Exactly
        }
    }

    Context 'parent directory creation (local_path mode)' {

        It 'creates the parent directory of the destination path before renaming the staging file' {
            # New-Item -Force is a no-op if the directory already exists, so this
            # call is safe on both first-apply (missing dir) and subsequent applies
            # (dir present). The test uses Split-Path on the same literal path so
            # the expected-dir computation is platform-consistent across Mac (where
            # backslash paths have no directory component) and Windows.
            Mock Test-Path { $true }
            Mock Get-FileHash { New-HypervImageFileHashSample -Hash 'EXPECTED' }
            Mock Move-Item { }
            Mock Remove-Item { }
            Mock Get-Item { New-HypervImageFileSample }

            New-HypervImageFileFromLocalPath `
                -DestinationPath 'C:\images\ubuntu.vhdx' `
                -StagingPath     'C:\images\ubuntu.vhdx.part-abc123' `
                -ExpectedSha256  'expected' | Out-Null

            $expectedDir = Split-Path -LiteralPath 'C:\images\ubuntu.vhdx'
            Should -Invoke New-Item -Times 1 -Exactly -ParameterFilter {
                $ItemType -eq 'Directory' -and $Force -eq $true -and $Path -eq $expectedDir
            }
        }

        It 'does not throw when the parent directory already exists' {
            Mock Test-Path { $true }
            Mock Get-FileHash { New-HypervImageFileHashSample -Hash 'EXPECTED' }
            Mock Move-Item { }
            Mock Remove-Item { }
            Mock Get-Item { New-HypervImageFileSample }

            { New-HypervImageFileFromLocalPath `
                -DestinationPath 'C:\images\ubuntu.vhdx' `
                -StagingPath     'C:\images\ubuntu.vhdx.part-abc123' `
                -ExpectedSha256  'expected' } | Should -Not -Throw
        }
    }
}
