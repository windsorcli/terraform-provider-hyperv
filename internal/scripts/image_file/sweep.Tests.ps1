# Locks the Invoke-HypervImageFileSweep contract: enumerates a dir,
# filters by name prefix AND excludes VHD-family extensions (those are
# the vhd sweeper's territory), removes each match, emits a JSON object
# with a `removed` array (even on zero / one match).

BeforeAll {
    . $PSScriptRoot/_test_helpers.ps1
    . $PSScriptRoot/../common/preamble.ps1
    . $PSScriptRoot/sweep.ps1
}

Describe 'Invoke-HypervImageFileSweep' {

    Context 'happy paths' {

        It 'removes non-VHD files matching the prefix, skips VHDs and unprefixed files' {
            Mock Test-Path { $true }
            Mock Get-ChildItem {
                @(
                    (New-HypervChildItemSample -Name 'tfacc-img-url-abc.bin')
                    (New-HypervChildItemSample -Name 'tfacc-img-iso-def.iso')
                    (New-HypervChildItemSample -Name 'tfacc-vm-root-ghi.vhdx')
                    (New-HypervChildItemSample -Name 'tfacc-vm-data-jkl.vhd')
                )
            }
            Mock Remove-Item {}

            $output = Invoke-HypervImageFileSweep -ParentDir 'C:\hyperv\tfacc' -NamePrefix 'tfacc-'
            $parsed = $output | ConvertFrom-Json

            @($parsed.removed).Count | Should -Be 2
            @($parsed.removed) | Should -Contain 'C:\hyperv\tfacc\tfacc-img-url-abc.bin'
            @($parsed.removed) | Should -Contain 'C:\hyperv\tfacc\tfacc-img-iso-def.iso'
            @($parsed.removed) | Should -Not -Contain 'C:\hyperv\tfacc\tfacc-vm-root-ghi.vhdx'
            @($parsed.removed) | Should -Not -Contain 'C:\hyperv\tfacc\tfacc-vm-data-jkl.vhd'
            Should -Invoke Remove-Item -Times 2
        }

        It 'emits an object with an empty array when there are zero matches' {
            Mock Test-Path { $true }
            Mock Get-ChildItem { @() }
            Mock Remove-Item {}

            $output = Invoke-HypervImageFileSweep -ParentDir 'C:\hyperv\tfacc' -NamePrefix 'tfacc-'
            $parsed = $output | ConvertFrom-Json

            @($parsed.removed).Count | Should -Be 0
            # Raw-JSON guard: ConvertTo-Json on an empty [string[]] could
            # serialize as null without the -InputObject + [pscustomobject]
            # wrap. The literal "[]" assertion catches the regression.
            $output | Should -Match '"removed":\[\]'
            Should -Invoke Remove-Item -Times 0
        }

        It 'returns an empty object when the parent directory is missing' {
            # Fresh bench: parent dir not yet created. Treat as no orphans.
            Mock Test-Path { $false }
            Mock Get-ChildItem { throw 'should not be called when parent_dir is missing' }
            Mock Remove-Item {}

            $output = Invoke-HypervImageFileSweep -ParentDir 'C:\does\not\exist' -NamePrefix 'tfacc-'
            $parsed = $output | ConvertFrom-Json

            @($parsed.removed).Count | Should -Be 0
            $output | Should -Match '"removed":\[\]'
            Should -Invoke Remove-Item -Times 0
        }

        It 'raw-JSON shape: single removed entry stays array-typed' {
            # Without -InputObject, ConvertTo-Json collapses a one-element
            # array property to a scalar on PS 5.1, breaking the Go
            # decoder. The regex below would fail if that regressed.
            Mock Test-Path { $true }
            Mock Get-ChildItem {
                @(
                    (New-HypervChildItemSample -Name 'tfacc-img-only.bin')
                )
            }
            Mock Remove-Item {}

            $output = Invoke-HypervImageFileSweep -ParentDir 'C:\hyperv\tfacc' -NamePrefix 'tfacc-'

            $output | Should -Match '"removed":\["[^"]*tfacc-img-only\.bin"\]'
        }

        It 'continues sweeping after a Remove-Item failure on one file' {
            # Best-effort: a permission error on file A must not prevent
            # the sweeper from clearing file B.
            Mock Test-Path { $true }
            Mock Get-ChildItem {
                @(
                    (New-HypervChildItemSample -Name 'tfacc-img-locked.bin')
                    (New-HypervChildItemSample -Name 'tfacc-img-clean.bin')
                )
            }
            Mock Remove-Item {
                if ($LiteralPath -like '*locked*') { throw 'access denied' }
            }

            $output = Invoke-HypervImageFileSweep -ParentDir 'C:\hyperv\tfacc' -NamePrefix 'tfacc-' -WarningAction SilentlyContinue
            $parsed = $output | ConvertFrom-Json

            @($parsed.removed).Count | Should -Be 1
            @($parsed.removed) | Should -Contain 'C:\hyperv\tfacc\tfacc-img-clean.bin'
            @($parsed.removed) | Should -Not -Contain 'C:\hyperv\tfacc\tfacc-img-locked.bin'
        }
    }

    Context 'parameter validation' {

        It 'rejects an empty NamePrefix (guards against pattern="*" sweep-everything)' {
            { Invoke-HypervImageFileSweep -ParentDir 'C:\hyperv\tfacc' -NamePrefix '' } |
                Should -Throw -ExpectedMessage '*null or empty*'
        }

        It 'rejects an empty ParentDir' {
            { Invoke-HypervImageFileSweep -ParentDir '' -NamePrefix 'tfacc-' } |
                Should -Throw -ExpectedMessage '*null or empty*'
        }
    }
}
