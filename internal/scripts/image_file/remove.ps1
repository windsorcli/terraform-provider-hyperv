# image_file/remove.ps1 -- delete a file from the host.
#
# Wire contract (locked in by Tests.ps1):
#
#   stdin JSON  : { "path": "<absolute-path>", "force": <bool> }
#   stdout      : empty (caller passes dst=nil to runScript).
#   stderr/exit : missing file -> Write-HypervError envelope with
#                 category=ObjectNotFound + exit 1, mapped to ErrNotFound on
#                 the Go side so Delete can treat already-gone as success.
#
# Delete is gated on the Go side: only invoked when the source mode placed
# the file (source_mode=url). For host_path mode, Delete is a no-op in Go --
# the user did not ask the provider to put the file there, so removing it on
# destroy would surprise them.
#
# `force` is the opt-in detach-then-retry escape hatch. When true and the
# initial Remove-Item hits a sharing violation whose holders are Hyper-V
# DVDs, the script detaches each DVD slot (Set-VMDvdDrive -Path $null) and
# retries the delete once. Same diagnostic surfaces on retry failure or
# when the holder is non-Hyper-V (AV scan, Explorer preview, etc.) -- the
# detach loop has nothing to act on in that case. When false (default),
# the original sharing-violation diagnostic surfaces immediately, which
# is the safe behavior for resources whose VM holder isn't being
# destroyed in the same operation.

# Get-HypervImageFileDvdHolder enumerates the Hyper-V DVD drives whose
# mounted media path equals $Path. Used by Remove-HypervImageFile's
# sharing-violation diagnostic to name the holder when Remove-Item fails
# with "another process." Returns an array of pscustomobjects with
# VMName + slot tuple (controller number / location); empty array means
# nothing in Hyper-V is holding the file, so the lock has another
# source (AV scan, Explorer preview, etc.) and the diagnostic still
# surfaces a clean message indicating the holder is non-Hyper-V.
#
# Same per-VM walk shape as Invoke-HypervDvdSafeReplace in new.ps1 (see
# the comment there): Get-VMDvdDrive -VMName '*' on PS 5.1 / older
# Hyper-V module versions returns objects with the VMName scalar
# unpopulated, so we iterate Get-VM and Get-VMDvdDrive -VMName <name>
# per VM. Path normalization handles forward/back slash mix.
function Get-HypervImageFileDvdHolder {
    [CmdletBinding()]
    param(
        [Parameter(Mandatory)] [string] $Path
    )
    $normalized = ($Path -replace '/', '\')
    $holders = @()
    foreach ($vm in (Get-VM -ErrorAction Stop)) {
        foreach ($dvd in (Get-VMDvdDrive -VMName $vm.Name -ErrorAction Stop)) {
            $p = $dvd.Path
            if ($p -and [string]::Equals(
                    ($p -replace '/', '\'),
                    $normalized,
                    [System.StringComparison]::OrdinalIgnoreCase)) {
                $holders += [pscustomobject]@{
                    VMName             = $vm.Name
                    ControllerNumber   = [int] $dvd.ControllerNumber
                    ControllerLocation = [int] $dvd.ControllerLocation
                }
            }
        }
    }
    return @($holders)
}

# Format-HypervImageFileLockedMessage renders the operator-facing message
# for an ImageFileLocked error. Pulled out of Remove-HypervImageFile so the
# force-retry path and the no-force path emit identical text for the same
# (path, holders) tuple -- the operator should not see a different
# diagnostic just because they opted in to force_destroy. Empty holders
# array picks the non-Hyper-V branch.
function Format-HypervImageFileLockedMessage {
    [CmdletBinding()]
    param(
        [Parameter(Mandatory)] [string] $Path,
        [Parameter(Mandatory)] [AllowEmptyCollection()] [object[]] $Holders
    )
    if ($Holders.Count -eq 0) {
        $detail = "No Hyper-V DVD attachment matches this path; another process " +
            "(antivirus scan, Explorer preview, etc.) is holding the file."
    }
    else {
        $tuples = $Holders | ForEach-Object {
            "VM '$($_.VMName)' has it attached at controller " +
            "$($_.ControllerNumber)/$($_.ControllerLocation)"
        }
        $detail = ($tuples -join '; ') + ". Detach the dvd_drive (or " +
            "taint the hyperv_image_file resource) so the next apply " +
            "can release the lock."
    }
    return "Cannot remove '$Path': $detail"
}

# New-HypervImageFileLockedError builds an ImageFileLocked ErrorRecord.
# Centralized so force-retry and no-force paths emit byte-identical
# CategoryInfo / FullyQualifiedErrorId; tests assert on those values and
# subtle drift between the two paths would silently break the diagnostic
# contract.
function New-HypervImageFileLockedError {
    [CmdletBinding()]
    param(
        [Parameter(Mandatory)] [string] $Path,
        [Parameter(Mandatory)] [string] $Message
    )
    $exception = [System.IO.IOException]::new($Message)
    return [System.Management.Automation.ErrorRecord]::new(
        $exception, 'ImageFileLocked',
        [System.Management.Automation.ErrorCategory]::ResourceBusy, $Path)
}

# Invoke-HypervImageFileForceDetach walks a holders array and detaches
# each DVD slot via Set-VMDvdDrive -Path $null. Errors from individual
# detach calls propagate -- a partial detach is less useful than a
# clean diagnostic naming the slot that failed, since the operator
# needs to know whether to retry or escalate to the VM owner.
#
# Pulled out of Remove-HypervImageFile to keep the per-holder iteration
# in one place; the function body would otherwise pile a third nested
# loop into the catch block.
function Invoke-HypervImageFileForceDetach {
    [CmdletBinding()]
    param(
        [Parameter(Mandatory)] [AllowEmptyCollection()] [object[]] $Holders
    )
    foreach ($holder in $Holders) {
        Set-VMDvdDrive `
            -VMName $holder.VMName `
            -ControllerNumber $holder.ControllerNumber `
            -ControllerLocation $holder.ControllerLocation `
            -Path $null `
            -ErrorAction Stop
    }
}

# Remove-HypervImageFile deletes a file at the given path. Test-Path returns
# $false (no error) for non-existent paths, so the missing branch sidesteps
# the SilentlyContinue trap. Permission/IO errors from Test-Path propagate
# via $ErrorActionPreference='Stop' from the preamble.
#
# Sharing-violation diagnostic: Remove-Item on a file currently mounted as
# a Hyper-V DVD media surfaces a bare "The process cannot access the file
# because it is being used by another process" with no holder named. We
# wrap the call, detect the Win32 ERROR_SHARING_VIOLATION (HRESULT
# 0x80070020 = -2147024864 as a signed int32), look up which VM has the
# file attached, and re-throw with the (VMName, slot) tuple in the
# message so the operator sees a clear next step (taint the resource,
# remove the dvd_drive attachment, etc.). Non-sharing-violation errors
# re-throw unchanged.
#
# When -Force is set and the holders are Hyper-V DVDs, the function
# detaches each slot (Set-VMDvdDrive -Path $null) and retries the
# delete once. This is the cross-module-destroy escape hatch: the
# VM resource lives in a different terraform state that will be
# destroyed in a later apply, so Terraform can't model the dependency,
# and the locked-file diagnostic blocks the cidata module's destroy.
# Opt-in (default $false) because the detach mutates VM state the
# hyperv_vm resource tracks, drifting that state until the VM is
# itself destroyed.
function Remove-HypervImageFile {
    [CmdletBinding()]
    param(
        [Parameter(Mandatory)] [string] $Path,
        [switch] $Force
    )
    if (-not (Test-Path -LiteralPath $Path -PathType Leaf)) {
        $exception = [System.Management.Automation.ItemNotFoundException]::new(
            "Image file not found at path '$Path'.")
        $errorRecord = [System.Management.Automation.ErrorRecord]::new(
            $exception, 'ImageFileNotFound',
            [System.Management.Automation.ErrorCategory]::ObjectNotFound, $Path)
        throw $errorRecord
    }
    try {
        Remove-Item -LiteralPath $Path -Force -ErrorAction Stop
    }
    catch {
        # HResult is the canonical cross-locale signal: -2147024864 is
        # Win32 0x80070020 (ERROR_SHARING_VIOLATION) and .NET populates
        # it on every IOException regardless of OS language. Don't fall
        # back to message-text matching -- "being used by another
        # process" is the English wording, and the fallback would
        # silently miss localized hosts and re-throw the bare error
        # without our diagnostic.
        $isSharingViolation = $_.Exception.HResult -eq -2147024864
        if (-not $isSharingViolation) { throw }

        # Wrap in @(...) at the call site: PowerShell's pipeline
        # unrolls an empty-array return to $null, which trips
        # Set-StrictMode -Version 3.0's null-property-access check on
        # the following .Count read.
        #
        # try/catch the lookup itself: Get-HypervImageFileDvdHolder
        # walks Get-VM / Get-VMDvdDrive with -ErrorAction Stop, so
        # VMMS-down / WMI-flap / perms errors there would propagate
        # out of THIS catch and replace the sharing-violation
        # diagnostic with an unrelated Hyper-V management error. The
        # holder name is supplementary; the actionable message is the
        # "another process is holding it" the operator needs. Fall
        # back to the no-holders branch so that message still surfaces
        # when Hyper-V management is degraded.
        try {
            $holders = @(Get-HypervImageFileDvdHolder -Path $Path)
        }
        catch {
            $holders = @()
        }

        # Force-detach path: when -Force is set and we have Hyper-V
        # holders, detach each slot and retry the delete once. A
        # non-Hyper-V holder (no entries in $holders) leaves nothing
        # for the detach loop to act on, so the original diagnostic
        # surfaces unchanged -- the operator still needs to deal with
        # the AV / Explorer / etc. holder out-of-band.
        if ($Force -and $holders.Count -gt 0) {
            # Two-phase intentionally, not one wrapping try/catch: the
            # detach-refused case (VM in Saved/Paused/Saving, runner
            # identity missing Hyper-V Admins on the VM, live-migration
            # in flight) needs different operator remediation than the
            # detach-succeeded-but-file-still-locked case. Folding both
            # into ImageFileLocked tells the operator to "resolve the
            # lock and re-run apply" -- which is wrong when Hyper-V
            # itself refused Set-VMDvdDrive, because the next apply hits
            # the same refusal. Letting the Set-VMDvdDrive ErrorRecord
            # propagate raw names the VM and the cmdlet, pointing at
            # the VM-state fix instead.
            Invoke-HypervImageFileForceDetach -Holders $holders

            try {
                Remove-Item -LiteralPath $Path -Force -ErrorAction Stop
                return
            }
            catch {
                # Detach succeeded, but the file is still locked:
                # another holder appeared between detach and retry (AV
                # scanner, Explorer preview), or the original VM
                # re-attached via an out-of-band Set-VMDvdDrive. Naming
                # the holders we *did* detach is the right operator
                # starting point -- they're the most likely root cause
                # and the previous-holder VM is the obvious thing to
                # check next.
                $message = Format-HypervImageFileLockedMessage -Path $Path -Holders $holders
                throw (New-HypervImageFileLockedError -Path $Path -Message $message)
            }
        }

        $message = Format-HypervImageFileLockedMessage -Path $Path -Holders $holders
        throw (New-HypervImageFileLockedError -Path $Path -Message $message)
    }
}

# Entry block. Skipped during Pester runs (dot-source sets InvocationName='.').
if ($MyInvocation.InvocationName -ne '.') {
    try {
        $params = [Console]::In.ReadToEnd() | ConvertFrom-Json
        $forceFlag = $false
        if ($null -ne $params.PSObject.Properties['force']) {
            $forceFlag = [bool] $params.force
        }
        Remove-HypervImageFile -Path $params.path -Force:$forceFlag
    }
    catch {
        Write-HypervError $_
        exit 1
    }
}
