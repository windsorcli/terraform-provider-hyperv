# vhd/get.ps1 -- read metadata for a VHD/VHDX file on the host.
#
# Wire contract (locked in by Tests.ps1):
#
#   stdin JSON  : { "path": "<absolute-path>" }
#   stdout JSON : {
#                   "Path":           "<string>",
#                   "VhdType":        "Fixed"|"Dynamic"|"Differencing",
#                   "SizeBytes":      <int64>,            # logical size
#                   "FileSizeBytes":  <int64>,            # actual on-disk
#                   "BlockSizeBytes": <int64>,
#                   "ParentPath":     "<string>"|null,    # null unless Differencing
#                   "Format":         "VHD"|"VHDX",
#                   "Attached":       <bool>              # in use by any VM
#                 }
#   stderr/exit : missing file -> ObjectNotFound envelope, exit 1 -> Go maps
#                 to ErrNotFound so Read calls RemoveResource.
#
# Unlike image_file/get, no SHA-256: VHD content integrity is the OS's
# concern. Drift surfaces via FileSizeBytes and Attached instead.
#
# lint:allow-long-comment

# Get-HypervVHD reads a VHD's metadata. Same Test-Path-first pattern as
# image_file: a missing file returns $false (no error), so the missing
# branch sidesteps the SilentlyContinue trap. Permission/IO errors from
# Test-Path or Get-VHD propagate via $ErrorActionPreference='Stop'.
function Get-HypervVHD {
    [CmdletBinding()]
    param(
        [Parameter(Mandatory)] [string] $Path
    )
    if (-not (Test-Path -LiteralPath $Path -PathType Leaf)) {
        $exception = [System.Management.Automation.ItemNotFoundException]::new(
            "VHD not found at path '$Path'.")
        $errorRecord = [System.Management.Automation.ErrorRecord]::new(
            $exception, 'VHDNotFound',
            [System.Management.Automation.ErrorCategory]::ObjectNotFound, $Path)
        throw $errorRecord
    }
    $vhd = Get-VHD -Path $Path -ErrorAction Stop
    [pscustomobject]@{
        Path           = $vhd.Path
        VhdType        = $vhd.VhdType.ToString()
        SizeBytes      = [int64] $vhd.Size
        FileSizeBytes  = [int64] $vhd.FileSize
        BlockSizeBytes = [int64] $vhd.BlockSize
        ParentPath     = $vhd.ParentPath
        Format         = $vhd.VhdFormat.ToString()
        Attached       = [bool] $vhd.Attached
    } | Write-HypervResult
}

# Entry block. Skipped during Pester runs (dot-source sets InvocationName='.').
if ($MyInvocation.InvocationName -ne '.') {
    try {
        $params = Read-HypervStdinPayload
        Get-HypervVHD -Path $params.path
    }
    catch {
        Write-HypervError $_
        exit 1
    }
}
