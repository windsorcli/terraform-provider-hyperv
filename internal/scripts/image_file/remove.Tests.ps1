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

    Context 'sharing-violation diagnostic' {
        # ERROR_SHARING_VIOLATION = Win32 0x20, surfaces as IOException
        # with HResult -2147024864 (0x80070020 as a signed int32).
        It 'names the holding VM and slot when Hyper-V holds a lock on the path' {
            Mock Test-Path { $true }
            Mock Remove-Item {
                $exception = [System.IO.IOException]::new(
                    "The process cannot access the file because it is being used by another process.",
                    -2147024864)
                $errorRecord = [System.Management.Automation.ErrorRecord]::new(
                    $exception, 'RemoveItemIOException',
                    [System.Management.Automation.ErrorCategory]::WriteError, $LiteralPath)
                throw $errorRecord
            }
            Mock Get-VM { New-HypervImageFileVMSample -Name 'controlplane' }
            Mock Get-VMDvdDrive {
                New-HypervImageFileVMDvdDriveSample `
                    -Path 'C:\images\seed.iso' `
                    -ControllerNumber 0 `
                    -ControllerLocation 1
            } -ParameterFilter { $VMName -eq 'controlplane' }

            $captured = $null
            try { Remove-HypervImageFile -Path 'C:\images\seed.iso' } catch { $captured = $_ }

            $captured | Should -Not -BeNullOrEmpty
            $captured.CategoryInfo.Category.ToString() | Should -Be 'ResourceBusy'
            $captured.FullyQualifiedErrorId | Should -Match 'ImageFileLocked'
            $captured.Exception.Message | Should -Match 'controlplane'
            $captured.Exception.Message | Should -Match 'controller 0/1'
        }

        It 'reports non-Hyper-V holder when no DVD attachment matches' {
            # Sharing violation can come from AV scan, Explorer preview,
            # etc. Surface that explicitly so the operator doesn't chase
            # phantom VM detachments.
            Mock Test-Path { $true }
            Mock Remove-Item {
                $exception = [System.IO.IOException]::new(
                    "The process cannot access the file because it is being used by another process.",
                    -2147024864)
                throw $exception
            }
            Mock Get-VM { @() }
            Mock Get-VMDvdDrive { @() }

            $captured = $null
            try { Remove-HypervImageFile -Path 'C:\images\seed.iso' } catch { $captured = $_ }

            $captured | Should -Not -BeNullOrEmpty
            $captured.FullyQualifiedErrorId | Should -Match 'ImageFileLocked'
            $captured.Exception.Message | Should -Match 'No Hyper-V DVD attachment matches'
        }

        It 'still surfaces ImageFileLocked when the holder lookup itself fails (degraded VMMS)' {
            # Regression: Get-HypervImageFileDvdHolder uses
            # Get-VM -ErrorAction Stop, so a VMMS-unavailable / WMI-flap /
            # permissions failure during the lookup would propagate out of
            # the sharing-violation catch and replace the actionable
            # diagnostic with an unrelated Hyper-V management error. The
            # call site wraps the lookup in its own try/catch so that
            # path falls back to the non-Hyper-V message and the operator
            # still sees the "another process is holding it" they need.
            Mock Test-Path { $true }
            Mock Remove-Item {
                $exception = [System.IO.IOException]::new(
                    "The process cannot access the file because it is being used by another process.",
                    -2147024864)
                throw $exception
            }
            Mock Get-VM { throw "The Virtual Machine Management Service is not available." }
            Mock Get-VMDvdDrive { @() }

            $captured = $null
            try { Remove-HypervImageFile -Path 'C:\images\seed.iso' } catch { $captured = $_ }

            $captured | Should -Not -BeNullOrEmpty
            $captured.FullyQualifiedErrorId | Should -Match 'ImageFileLocked'
            $captured.Exception.Message | Should -Match 'No Hyper-V DVD attachment matches'
            $captured.Exception.Message | Should -Not -Match 'Virtual Machine Management Service'
        }

        It 'does NOT call Get-VMDvdDrive for non-sharing-violation IO errors (cheap path)' {
            # Disk full, permission denied, etc. shouldn't trigger the VM
            # enumeration -- those holders are irrelevant and the
            # enumeration is N+1 cmdlet calls.
            Mock Test-Path { $true }
            Mock Remove-Item {
                $exception = [System.IO.IOException]::new("disk full", -2147024784)
                throw $exception
            }
            Mock Get-VM { @() }
            Mock Get-VMDvdDrive { @() }

            try { Remove-HypervImageFile -Path 'C:\images\foo.iso' } catch { }

            Should -Invoke Get-VM -Times 0 -Exactly
            Should -Invoke Get-VMDvdDrive -Times 0 -Exactly
        }
    }
}
