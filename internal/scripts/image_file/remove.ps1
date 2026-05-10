# image_file/remove.ps1 -- delete a file from the host.
#
# Wire contract (locked in by Tests.ps1):
#
#   stdin JSON  : { "path": "<absolute-path>" }
#   stdout      : empty (caller passes dst=nil to runScript).
#   stderr/exit : missing file -> Write-HypervError envelope with
#                 category=ObjectNotFound + exit 1, mapped to ErrNotFound on
#                 the Go side so Delete can treat already-gone as success.
#
# Delete is gated on the Go side: only invoked when the source mode placed
# the file (source_mode=url). For host_path mode, Delete is a no-op in Go --
# the user did not ask the provider to put the file there, so removing it on
# destroy would surprise them.

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
function Remove-HypervImageFile {
    [CmdletBinding()]
    param(
        [Parameter(Mandatory)] [string] $Path
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
        $isSharingViolation = ($_.Exception.HResult -eq -2147024864) -or
                              ($_.Exception.Message -match 'being used by another process')
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
        if ($holders.Count -eq 0) {
            $detail = "No Hyper-V DVD attachment matches this path; another process " +
                "(antivirus scan, Explorer preview, etc.) is holding the file."
        }
        else {
            $tuples = $holders | ForEach-Object {
                "VM '$($_.VMName)' has it attached at controller " +
                "$($_.ControllerNumber)/$($_.ControllerLocation)"
            }
            $detail = ($tuples -join '; ') + ". Detach the dvd_drive (or " +
                "taint the hyperv_image_file resource) so the next apply " +
                "can release the lock."
        }
        $message = "Cannot remove '$Path': $detail"
        $exception = [System.IO.IOException]::new($message)
        $errorRecord = [System.Management.Automation.ErrorRecord]::new(
            $exception, 'ImageFileLocked',
            [System.Management.Automation.ErrorCategory]::ResourceBusy, $Path)
        throw $errorRecord
    }
}

# Entry block. Skipped during Pester runs (dot-source sets InvocationName='.').
if ($MyInvocation.InvocationName -ne '.') {
    try {
        $params = [Console]::In.ReadToEnd() | ConvertFrom-Json
        Remove-HypervImageFile -Path $params.path
    }
    catch {
        Write-HypervError $_
        exit 1
    }
}
