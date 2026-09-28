# image_file/remove.ps1 -- delete a file from the host.
#
# Wire contract (locked in by Tests.ps1):
#
#   stdin JSON  : { "path": "<absolute-path>", "force": <bool>,
#                   "expected_sha256": "<hex or empty>" }
#   stdout      : empty (caller passes dst=nil to runScript).
#   stderr/exit : missing file -> ObjectNotFound envelope, exit 1 -> Go
#                 maps to ErrNotFound so Delete treats already-gone as
#                 success. Content drift -> InvalidData/
#                 ImageFileContentDrift -> ErrContentDrift so Delete
#                 refuses instead of deleting.
#
# Delete is gated on the Go side to source_mode=url; host_path mode never
# reaches this script, since the provider didn't place that file.
#
# expected_sha256 is the resource's last-known state.sha256 (empty skips
# the check); a mismatch means the file changed since last read, so the
# delete is refused rather than silently removing unrecognized content.
#
# force is the opt-in detach-then-retry escape hatch: when true and a
# sharing violation's holders are Hyper-V DVDs, the script detaches each
# slot and retries once. Default false is the safe behavior when the VM
# holder isn't being destroyed in the same operation.
#
# lint:allow-long-comment

# Get-HypervImageFileDvdHolder enumerates the Hyper-V DVD drives whose
# mounted media path equals $Path, returning a VMName + slot tuple per
# match (empty array means the lock has a non-Hyper-V source). Same
# per-VM walk as Invoke-HypervDvdSafeReplace in new.ps1, for the same
# reason: the -VMName '*' wildcard form leaves VMName unpopulated on
# PS 5.1 / older Hyper-V modules.
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

# Remove-HypervImageFile deletes a file at the given path. On a Win32
# ERROR_SHARING_VIOLATION from Remove-Item, it looks up which VM has the
# file attached as DVD media and re-throws naming the (VMName, slot)
# tuple, instead of the cmdlet's bare "used by another process."
#
# -Force additionally detaches each Hyper-V DVD holder and retries the
# delete once: the cross-module-destroy escape hatch for a VM resource
# living in a different Terraform state. Opt-in (default $false) since
# the detach drifts state the hyperv_vm resource tracks.
#
# lint:allow-long-comment
function Remove-HypervImageFile {
    [CmdletBinding()]
    param(
        [Parameter(Mandatory)] [string] $Path,
        [switch] $Force,
        [Parameter()] [string] $ExpectedSha256 = ''
    )
    if (-not (Test-Path -LiteralPath $Path -PathType Leaf)) {
        $exception = [System.Management.Automation.ItemNotFoundException]::new(
            "Image file not found at path '$Path'.")
        $errorRecord = [System.Management.Automation.ErrorRecord]::new(
            $exception, 'ImageFileNotFound',
            [System.Management.Automation.ErrorCategory]::ObjectNotFound, $Path)
        throw $errorRecord
    }
    if ($ExpectedSha256) {
        # Same cost as the Read-time hash: full-file SHA-256, so a
        # multi-GiB VHDX adds real time to every destroy.
        $actualHash = (Get-FileHash -LiteralPath $Path -Algorithm SHA256).Hash.ToLowerInvariant()
        if ($actualHash -ne $ExpectedSha256.ToLowerInvariant()) {
            $exception = [System.IO.InvalidDataException]::new(
                "Refusing to delete '$Path': its content no longer matches this resource's " +
                "last-known state (expected sha256=$($ExpectedSha256.ToLowerInvariant()), " +
                "found sha256=$actualHash). Something else changed this file since it was " +
                "last read.")
            $errorRecord = [System.Management.Automation.ErrorRecord]::new(
                $exception, 'ImageFileContentDrift',
                [System.Management.Automation.ErrorCategory]::InvalidData, $Path)
            throw $errorRecord
        }
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

        # @(...) prevents StrictMode from tripping on .Count when the pipeline unrolls an empty result to $null.
        try {
            $holders = @(Get-HypervImageFileDvdHolder -Path $Path)
        }
        catch {
            # A degraded Hyper-V lookup shouldn't mask the sharing-violation diagnostic; fall back to no-holders.
            $holders = @()
        }

        if ($Force -and $holders.Count -gt 0) {
            # Two-phase: a detach refusal (VM state, permissions) needs different remediation than a post-detach re-lock, so each gets its own catch.
            Invoke-HypervImageFileForceDetach -Holders $holders

            try {
                Remove-Item -LiteralPath $Path -Force -ErrorAction Stop
                return
            }
            catch {
                # Empty holders here: the Hyper-V slots were already detached, so blaming them again would be wrong; a new non-Hyper-V holder took the lock.
                $message = Format-HypervImageFileLockedMessage -Path $Path -Holders @()
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
        $params = Read-HypervStdinPayload
        $forceFlag = $false
        if ($null -ne $params.PSObject.Properties['force']) {
            $forceFlag = [bool] $params.force
        }
        $expectedSha256 = ''
        if ($null -ne $params.PSObject.Properties['expected_sha256']) {
            $expectedSha256 = [string] $params.expected_sha256
        }
        Remove-HypervImageFile -Path $params.path -Force:$forceFlag -ExpectedSha256 $expectedSha256
    }
    catch {
        Write-HypervError $_
        exit 1
    }
}
