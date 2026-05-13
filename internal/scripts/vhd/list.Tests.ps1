# Locks the Get-HypervVHDByPrefix contract: filters Get-ChildItem by name
# prefix AND VHD extension family, emits a JSON array (even on zero / one
# match) with only Path, treats a missing parent dir as empty (not error).

BeforeAll {
    . $PSScriptRoot/_test_helpers.ps1
    . $PSScriptRoot/../common/preamble.ps1
    . $PSScriptRoot/list.ps1
}

Describe 'Get-HypervVHDByPrefix' {

    Context 'happy paths' {

        It 'returns VHDs and VHDXs matching the prefix, excludes non-VHD extensions' {
            # The extension filter is what keeps the VHD sweeper from
            # stomping on the image_file sweeper's territory. A
            # tfacc-*.iso or tfacc-*.txt file in the same dir gets
            # claimed by image_file, not by vhd.
            Mock Test-Path { $true }
            Mock Get-ChildItem {
                @(
                    (New-HypervChildItemSample -Name 'tfacc-vm-root-abc.vhdx')
                    (New-HypervChildItemSample -Name 'tfacc-vm-data-def.vhd')
                    (New-HypervChildItemSample -Name 'tfacc-vm-snap-ghi.avhdx')
                    (New-HypervChildItemSample -Name 'tfacc-fixture.iso')
                    (New-HypervChildItemSample -Name 'tfacc-cidata.txt')
                )
            }

            $output = Get-HypervVHDByPrefix -ParentDir 'C:\hyperv\tfacc' -NamePrefix 'tfacc-'
            $parsed = $output | ConvertFrom-Json

            @($parsed).Count | Should -Be 3
            @($parsed).Path | Should -Contain 'C:\hyperv\tfacc\tfacc-vm-root-abc.vhdx'
            @($parsed).Path | Should -Contain 'C:\hyperv\tfacc\tfacc-vm-data-def.vhd'
            @($parsed).Path | Should -Contain 'C:\hyperv\tfacc\tfacc-vm-snap-ghi.avhdx'
            @($parsed).Path | Should -Not -Contain 'C:\hyperv\tfacc\tfacc-fixture.iso'
            @($parsed).Path | Should -Not -Contain 'C:\hyperv\tfacc\tfacc-cidata.txt'
        }

        It 'returns an empty JSON array when the parent directory does not exist' {
            # A fresh bench legitimately has no fixture dir. The
            # sweeper treats this as "no orphans," not as an error.
            Mock Test-Path { $false }
            Mock Get-ChildItem { throw 'should not be called when parent_dir is missing' }

            $output = Get-HypervVHDByPrefix -ParentDir 'C:\does\not\exist' -NamePrefix 'tfacc-'

            $output | Should -Be '[]'
            Should -Invoke Get-ChildItem -Times 0 -Exactly
        }

        It 'emits a JSON array even when there are zero matches in the directory' {
            Mock Test-Path { $true }
            Mock Get-ChildItem { @() }

            $output = Get-HypervVHDByPrefix -ParentDir 'C:\hyperv\tfacc' -NamePrefix 'tfacc-'

            $output | Should -Be '[]'
        }

        It 'emits a JSON array (not a bare object) when there is exactly one match' {
            Mock Test-Path { $true }
            Mock Get-ChildItem { @(New-HypervChildItemSample -Name 'tfacc-only-one.vhdx') }

            $output = Get-HypervVHDByPrefix -ParentDir 'C:\hyperv\tfacc' -NamePrefix 'tfacc-'
            $parsed = $output | ConvertFrom-Json

            @($parsed).Count | Should -Be 1
            @($parsed).Path | Should -Contain 'C:\hyperv\tfacc\tfacc-only-one.vhdx'
            $output | Should -Match '^\['
        }

        It 'emits only the Path field (sweeper does not need Size, CreationTime, etc.)' {
            Mock Test-Path { $true }
            Mock Get-ChildItem { @(New-HypervChildItemSample -Name 'tfacc-vm-shape.vhdx') }

            $output = Get-HypervVHDByPrefix -ParentDir 'C:\hyperv\tfacc' -NamePrefix 'tfacc-'
            $parsed = $output | ConvertFrom-Json

            @($parsed[0].PSObject.Properties).Count | Should -Be 1
            $parsed[0].Path | Should -Be 'C:\hyperv\tfacc\tfacc-vm-shape.vhdx'
        }

        It 'is case-insensitive on the extension match (.VHDX = .vhdx)' {
            # NTFS is case-insensitive; the script's extension filter
            # uses ToLowerInvariant() to match either form.
            Mock Test-Path { $true }
            Mock Get-ChildItem {
                @(
                    (New-HypervChildItemSample -Name 'tfacc-upper.VHDX')
                    (New-HypervChildItemSample -Name 'tfacc-lower.vhdx')
                )
            }

            $output = Get-HypervVHDByPrefix -ParentDir 'C:\hyperv\tfacc' -NamePrefix 'tfacc-'
            $parsed = $output | ConvertFrom-Json

            @($parsed).Count | Should -Be 2
        }
    }

    Context 'error propagation' {

        It 'lets Get-ChildItem errors bubble when the dir exists but is unreadable' {
            # Permissions error on the test dir is a real fault, not a
            # missing-dir case -- bubble it so the operator notices.
            Mock Test-Path { $true }
            Mock Get-ChildItem { throw 'access denied to C:\hyperv\tfacc' }

            { Get-HypervVHDByPrefix -ParentDir 'C:\hyperv\tfacc' -NamePrefix 'tfacc-' } |
                Should -Throw -ExpectedMessage '*access denied*'
        }
    }
}
