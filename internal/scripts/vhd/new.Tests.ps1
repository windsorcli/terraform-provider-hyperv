# Locks the JSON contract for New-HypervVHD{Fixed,Dynamic,Differencing}.
# Three creation modes share an output shape (matches get.ps1) but each
# forwards a distinct -Fixed/-Dynamic/-Differencing switch with mode-
# appropriate other params.

BeforeAll {
    . $PSScriptRoot/_test_helpers.ps1
    . $PSScriptRoot/../common/preamble.ps1
    . $PSScriptRoot/new.ps1
}

Describe 'New-HypervVHDFixed' {

    It 'forwards -Fixed with -Path and -SizeBytes' {
        Mock New-VHD { }
        Mock Get-VHD { New-HypervVHDSample -VhdType 'Fixed' -Size 1073741824 }

        New-HypervVHDFixed -Path 'C:\vhds\fixed.vhdx' -SizeBytes 1073741824 | Out-Null

        Should -Invoke New-VHD -Times 1 -Exactly -ParameterFilter {
            $Path -eq 'C:\vhds\fixed.vhdx' -and
            $SizeBytes -eq 1073741824 -and
            $Fixed -eq $true
        }
    }

    It 'forwards -BlockSizeBytes when supplied' {
        Mock New-VHD { }
        Mock Get-VHD { New-HypervVHDSample }

        New-HypervVHDFixed -Path 'C:\vhds\fixed.vhdx' -SizeBytes 1073741824 -BlockSizeBytes 33554432 | Out-Null

        Should -Invoke New-VHD -Times 1 -Exactly -ParameterFilter {
            $BlockSizeBytes -eq 33554432
        }
    }

    It 'omits -BlockSizeBytes when not supplied (lets Hyper-V default apply)' {
        # Asserting via $PSBoundParameters.ContainsKey because an unbound
        # [int64] under StrictMode 3.0 references an undefined variable in
        # the filter context, which silently fails the match.
        Mock New-VHD { }
        Mock Get-VHD { New-HypervVHDSample }

        New-HypervVHDFixed -Path 'C:\vhds\fixed.vhdx' -SizeBytes 1073741824 | Out-Null

        Should -Invoke New-VHD -Times 1 -Exactly -ParameterFilter {
            -not $PSBoundParameters.ContainsKey('BlockSizeBytes')
        }
    }

    It 'emits the canonical eight-field shape after creation (matches get.ps1)' {
        Mock New-VHD { }
        Mock Get-VHD { New-HypervVHDSample -VhdType 'Fixed' }

        $parsed = New-HypervVHDFixed -Path 'C:\vhds\fixed.vhdx' -SizeBytes 1073741824 | ConvertFrom-Json

        $parsed.PSObject.Properties.Name | Sort-Object | Should -Be @(
            'Attached', 'BlockSizeBytes', 'FileSizeBytes', 'Format',
            'ParentPath', 'Path', 'SizeBytes', 'VhdType'
        )
        $parsed.VhdType | Should -Be 'Fixed'
    }
}

Describe 'New-HypervVHDDynamic' {

    It 'forwards -Dynamic with -Path and -SizeBytes (not -Fixed)' {
        Mock New-VHD { }
        Mock Get-VHD { New-HypervVHDSample -VhdType 'Dynamic' }

        New-HypervVHDDynamic -Path 'C:\vhds\dyn.vhdx' -SizeBytes 34359738368 | Out-Null

        Should -Invoke New-VHD -Times 1 -Exactly -ParameterFilter {
            $Path -eq 'C:\vhds\dyn.vhdx' -and
            $SizeBytes -eq 34359738368 -and
            $Dynamic -eq $true
        }
    }

    It 'forwards -BlockSizeBytes when supplied' {
        # Mirrors the equivalent New-HypervVHDFixed test: dynamic shares
        # the same BlockSizeBytes splat logic, and a regression in the
        # dynamic path would otherwise go undetected.
        Mock New-VHD { }
        Mock Get-VHD { New-HypervVHDSample -VhdType 'Dynamic' }

        New-HypervVHDDynamic -Path 'C:\vhds\dyn.vhdx' -SizeBytes 34359738368 -BlockSizeBytes 33554432 | Out-Null

        Should -Invoke New-VHD -Times 1 -Exactly -ParameterFilter {
            $BlockSizeBytes -eq 33554432
        }
    }

    It 'omits -BlockSizeBytes when not supplied (lets Hyper-V default apply)' {
        # Asserting via $PSBoundParameters.ContainsKey because an unbound
        # [int64] under StrictMode 3.0 references an undefined variable in
        # the filter context, which silently fails the match. (Same gotcha
        # as the New-HypervVHDFixed counterpart -- documented there.)
        Mock New-VHD { }
        Mock Get-VHD { New-HypervVHDSample -VhdType 'Dynamic' }

        New-HypervVHDDynamic -Path 'C:\vhds\dyn.vhdx' -SizeBytes 34359738368 | Out-Null

        Should -Invoke New-VHD -Times 1 -Exactly -ParameterFilter {
            -not $PSBoundParameters.ContainsKey('BlockSizeBytes')
        }
    }
}

Describe 'New-HypervVHDDifferencing' {

    It 'forwards -Differencing with -Path and -ParentPath, no -SizeBytes' {
        # Differencing inherits size from the parent -- supplying -SizeBytes
        # to New-VHD with -Differencing is a Hyper-V error.
        Mock New-VHD { }
        Mock Get-VHD { New-HypervVHDSample -VhdType 'Differencing' -ParentPath 'C:\vhds\parent.vhdx' }

        New-HypervVHDDifferencing -Path 'C:\vhds\child.vhdx' -ParentPath 'C:\vhds\parent.vhdx' | Out-Null

        Should -Invoke New-VHD -Times 1 -Exactly -ParameterFilter {
            $Path -eq 'C:\vhds\child.vhdx' -and
            $ParentPath -eq 'C:\vhds\parent.vhdx' -and
            $Differencing -eq $true -and
            -not $PSBoundParameters.ContainsKey('SizeBytes')
        }
    }

    It 'reports the parent path back through the read shape' {
        Mock New-VHD { }
        Mock Get-VHD { New-HypervVHDSample -VhdType 'Differencing' -ParentPath 'C:\vhds\parent.vhdx' }

        $parsed = New-HypervVHDDifferencing -Path 'C:\vhds\child.vhdx' -ParentPath 'C:\vhds\parent.vhdx' | ConvertFrom-Json

        $parsed.VhdType    | Should -Be 'Differencing'
        $parsed.ParentPath | Should -Be 'C:\vhds\parent.vhdx'
    }

    It 'surfaces the Microsoft.Vhd InvalidParameter error when the parent is missing/invalid' {
        # Locks the wire contract for ErrInvalidParentPath: New-VHD raises
        # InvalidArgument with FullyQualifiedErrorId starting
        # "InvalidParameter,Microsoft.Vhd.*". The Go-side errors.go
        # mapCategory function pattern-matches that prefix to route the
        # typed error -- see internal/hyperv/errors.go and spike #3.
        Mock New-VHD {
            $exception = [System.ArgumentException]::new('parent path missing')
            $errorRecord = [System.Management.Automation.ErrorRecord]::new(
                $exception,
                'InvalidParameter,Microsoft.Vhd.PowerShell.Cmdlets.NewVhd',
                [System.Management.Automation.ErrorCategory]::InvalidArgument,
                'C:\vhds\does-not-exist.vhdx')
            throw $errorRecord
        }
        Mock Get-VHD { New-HypervVHDSample }

        $captured = $null
        try {
            New-HypervVHDDifferencing -Path 'C:\vhds\child.vhdx' -ParentPath 'C:\vhds\does-not-exist.vhdx'
        } catch { $captured = $_ }

        $captured | Should -Not -BeNullOrEmpty
        $captured.CategoryInfo.Category.ToString() | Should -Be 'InvalidArgument'
        $captured.FullyQualifiedErrorId | Should -Match '^InvalidParameter,Microsoft\.Vhd\.'
    }
}
