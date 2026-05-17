# Locks the Remove-HypervImageFile contract: -Force is always passed, the
# function emits no stdout (caller passes dst=nil), missing-file errors
# propagate so the entry block can convert them to the structured error
# envelope, and non-ObjectNotFound errors from the underlying cmdlets
# propagate rather than being remapped to "missing".

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

    Context 'force_destroy detach-then-retry' {
        # The -Force escape hatch handles the cross-module-destroy case:
        # the cidata image_file lives in one terraform state, the VM
        # holding it lives in another, and the cidata module's destroy
        # plan can't model the dependency. With -Force the script
        # detaches the DVD slots and retries the delete once, letting
        # the cidata destroy succeed even though the VM-side destroy
        # hasn't run yet.

        It 'detaches DVD slots and retries Remove-Item when -Force is set and Hyper-V holds the lock' {
            Mock Test-Path { $true }
            $script:removeAttempt = 0
            Mock Remove-Item {
                $script:removeAttempt++
                if ($script:removeAttempt -eq 1) {
                    $exception = [System.IO.IOException]::new(
                        "The process cannot access the file because it is being used by another process.",
                        -2147024864)
                    throw $exception
                }
                # Second attempt succeeds (DVD detached -- lock released).
            }
            Mock Get-VM { New-HypervImageFileVMSample -Name 'worker-2' }
            Mock Get-VMDvdDrive {
                New-HypervImageFileVMDvdDriveSample `
                    -VMName 'worker-2' `
                    -Path 'C:\images\seed.iso' `
                    -ControllerNumber 0 `
                    -ControllerLocation 2
            } -ParameterFilter { $VMName -eq 'worker-2' }
            Mock Set-VMDvdDrive { }

            { Remove-HypervImageFile -Path 'C:\images\seed.iso' -Force } |
                Should -Not -Throw

            # The detach call passes -Path $null to release the slot. The
            # _test_helpers Set-VMDvdDrive stub binds Path as [string], so
            # $null folds to '' on PS 5.1; assert on the slot tuple and
            # the empty-string Path so the filter doesn't depend on the
            # stub's parameter-binding choice.
            Should -Invoke Set-VMDvdDrive -Times 1 -Exactly -ParameterFilter {
                $VMName -eq 'worker-2' -and
                $ControllerNumber -eq 0 -and
                $ControllerLocation -eq 2 -and
                [string]::IsNullOrEmpty($Path)
            }
            Should -Invoke Remove-Item -Times 2 -Exactly
        }

        It 'detaches every Hyper-V holder when -Force is set (multiple VMs)' {
            # Multiple VMs holding the same cidata path is rare but legal
            # (e.g., a snapshot/clone that shares the seed). The detach
            # loop must cover every slot or the retry hits the same
            # sharing violation.
            Mock Test-Path { $true }
            $script:removeAttempt = 0
            Mock Remove-Item {
                $script:removeAttempt++
                if ($script:removeAttempt -eq 1) {
                    $exception = [System.IO.IOException]::new(
                        "The process cannot access the file because it is being used by another process.",
                        -2147024864)
                    throw $exception
                }
            }
            Mock Get-VM {
                @(
                    (New-HypervImageFileVMSample -Name 'worker-2'),
                    (New-HypervImageFileVMSample -Name 'worker-3')
                )
            }
            Mock Get-VMDvdDrive {
                if ($VMName -eq 'worker-2') {
                    New-HypervImageFileVMDvdDriveSample `
                        -VMName 'worker-2' `
                        -Path 'C:\images\shared.iso' `
                        -ControllerNumber 0 -ControllerLocation 2
                }
                elseif ($VMName -eq 'worker-3') {
                    New-HypervImageFileVMDvdDriveSample `
                        -VMName 'worker-3' `
                        -Path 'C:\images\shared.iso' `
                        -ControllerNumber 0 -ControllerLocation 2
                }
            }
            Mock Set-VMDvdDrive { }

            { Remove-HypervImageFile -Path 'C:\images\shared.iso' -Force } |
                Should -Not -Throw

            Should -Invoke Set-VMDvdDrive -Times 2 -Exactly
            Should -Invoke Set-VMDvdDrive -Times 1 -Exactly -ParameterFilter { $VMName -eq 'worker-2' }
            Should -Invoke Set-VMDvdDrive -Times 1 -Exactly -ParameterFilter { $VMName -eq 'worker-3' }
        }

        It 'does NOT call Set-VMDvdDrive when -Force is omitted (no-detach is the default)' {
            # Backstop for the safety property: force_destroy=false on the
            # resource means the script never mutates VM state. The
            # original ImageFileLocked diagnostic must still surface.
            Mock Test-Path { $true }
            Mock Remove-Item {
                $exception = [System.IO.IOException]::new(
                    "The process cannot access the file because it is being used by another process.",
                    -2147024864)
                throw $exception
            }
            Mock Get-VM { New-HypervImageFileVMSample -Name 'worker-2' }
            Mock Get-VMDvdDrive {
                New-HypervImageFileVMDvdDriveSample `
                    -VMName 'worker-2' `
                    -Path 'C:\images\seed.iso' `
                    -ControllerNumber 0 -ControllerLocation 2
            }
            Mock Set-VMDvdDrive { }

            $captured = $null
            try { Remove-HypervImageFile -Path 'C:\images\seed.iso' } catch { $captured = $_ }

            $captured | Should -Not -BeNullOrEmpty
            $captured.FullyQualifiedErrorId | Should -Match 'ImageFileLocked'
            Should -Invoke Set-VMDvdDrive -Times 0 -Exactly
        }

        It 'does NOT call Set-VMDvdDrive when -Force is set but the holder is non-Hyper-V' {
            # AV scan / Explorer preview / etc. produce a sharing
            # violation with no matching DVD attachment. -Force has
            # nothing to act on; surface the original "another process"
            # message so the operator resolves it out-of-band.
            Mock Test-Path { $true }
            Mock Remove-Item {
                $exception = [System.IO.IOException]::new(
                    "The process cannot access the file because it is being used by another process.",
                    -2147024864)
                throw $exception
            }
            Mock Get-VM { @() }
            Mock Get-VMDvdDrive { @() }
            Mock Set-VMDvdDrive { }

            $captured = $null
            try { Remove-HypervImageFile -Path 'C:\images\seed.iso' -Force } catch { $captured = $_ }

            $captured | Should -Not -BeNullOrEmpty
            $captured.FullyQualifiedErrorId | Should -Match 'ImageFileLocked'
            $captured.Exception.Message | Should -Match 'No Hyper-V DVD attachment matches'
            Should -Invoke Set-VMDvdDrive -Times 0 -Exactly
        }

        It 'surfaces ImageFileLocked with the no-holders message when the retry still fails after detach' {
            # Detach succeeded but Remove-Item still fails -- a new
            # holder appeared between our detach and our retry. The
            # message must NOT name the VM whose slot we just cleared,
            # because that VM is no longer holding the file and the
            # operator should not be told to detach what's already
            # detached. The no-holders branch ("another process is
            # holding the file") points at the right culprit (AV /
            # Explorer / out-of-band Set-VMDvdDrive).
            Mock Test-Path { $true }
            Mock Remove-Item {
                $exception = [System.IO.IOException]::new(
                    "The process cannot access the file because it is being used by another process.",
                    -2147024864)
                throw $exception
            }
            Mock Get-VM { New-HypervImageFileVMSample -Name 'worker-2' }
            Mock Get-VMDvdDrive {
                New-HypervImageFileVMDvdDriveSample `
                    -VMName 'worker-2' `
                    -Path 'C:\images\seed.iso' `
                    -ControllerNumber 0 -ControllerLocation 2
            }
            Mock Set-VMDvdDrive { }

            $captured = $null
            try { Remove-HypervImageFile -Path 'C:\images\seed.iso' -Force } catch { $captured = $_ }

            $captured | Should -Not -BeNullOrEmpty
            $captured.FullyQualifiedErrorId | Should -Match 'ImageFileLocked'
            $captured.Exception.Message | Should -Match 'another process'
            $captured.Exception.Message | Should -Not -Match 'worker-2'
            $captured.Exception.Message | Should -Not -Match 'Detach the dvd_drive'
            Should -Invoke Set-VMDvdDrive -Times 1 -Exactly
            Should -Invoke Remove-Item -Times 2 -Exactly
        }

        It 'surfaces the raw Set-VMDvdDrive error (not ImageFileLocked) when the detach itself is refused' {
            # The detach-refused case -- VM in Saved/Paused, runner
            # missing Hyper-V Admins on the VM, live-migration in
            # flight -- demands different remediation than the
            # detach-succeeded-but-file-still-locked case. If we
            # wrapped this as ImageFileLocked, the operator would be
            # told to "resolve the lock and re-run apply" -- but the
            # next apply hits the same refusal. The raw error names
            # the VM and the cmdlet so they go fix VM state instead.
            Mock Test-Path { $true }
            Mock Remove-Item {
                $exception = [System.IO.IOException]::new(
                    "The process cannot access the file because it is being used by another process.",
                    -2147024864)
                throw $exception
            }
            Mock Get-VM { New-HypervImageFileVMSample -Name 'paused-vm' }
            Mock Get-VMDvdDrive {
                New-HypervImageFileVMDvdDriveSample `
                    -VMName 'paused-vm' `
                    -Path 'C:\images\seed.iso' `
                    -ControllerNumber 0 -ControllerLocation 1
            }
            Mock Set-VMDvdDrive {
                throw "Set-VMDvdDrive : The operation cannot be performed while the virtual machine is in its current state."
            }

            $captured = $null
            try { Remove-HypervImageFile -Path 'C:\images\seed.iso' -Force } catch { $captured = $_ }

            $captured | Should -Not -BeNullOrEmpty
            $captured.FullyQualifiedErrorId | Should -Not -Match 'ImageFileLocked'
            $captured.Exception.Message | Should -Match 'current state'
            Should -Invoke Set-VMDvdDrive -Times 1 -Exactly
            # Only the original Remove-Item runs: the retry never
            # fires because the detach failure short-circuits the
            # force branch.
            Should -Invoke Remove-Item -Times 1 -Exactly
        }
    }
}
