# vhd/set.ps1 -- the only in-place mutation a VHD supports: resize.
#
# Wire contract (locked in by Tests.ps1):
#
#   stdin JSON  : { "path": "<absolute-path>", "size_bytes": <int64> }
#   stdout JSON : same fields as get.ps1 (re-read after the resize lands).
#
# Other mutations (vhd_type, parent_path, block_size_bytes, path) are
# RequiresReplace at the schema layer and never reach this script.
#
# No pre-validation: Resize-VHD's own diagnostics are clearer than
# anything we'd write for shrink-with-data, offline-only Gen 1 resize,
# or the multi-GB rewrite cost of a fixed-format resize.
#
# lint:allow-long-comment

# Read-HypervVHDResult emits the canonical 8-field format. Inline duplicate
# of get.ps1's tail: the runtime concatenates only preamble plus a single
# verb script per call.
function Read-HypervVHDResult {
    [CmdletBinding()]
    param(
        [Parameter(Mandatory)] [string] $Path
    )
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

# Set-HypervVHD resizes the VHD and re-reads. Pre-checks existence with the
# same Test-Path-first pattern as get/remove so a missing file lands as
# ObjectNotFound -> ErrNotFound rather than Resize-VHD's less-specific error.
function Set-HypervVHD {
    [CmdletBinding()]
    param(
        [Parameter(Mandatory)] [string] $Path,
        [Parameter(Mandatory)] [int64]  $SizeBytes
    )
    if (-not (Test-Path -LiteralPath $Path -PathType Leaf)) {
        $exception = [System.Management.Automation.ItemNotFoundException]::new(
            "VHD not found at path '$Path'.")
        $errorRecord = [System.Management.Automation.ErrorRecord]::new(
            $exception, 'VHDNotFound',
            [System.Management.Automation.ErrorCategory]::ObjectNotFound, $Path)
        throw $errorRecord
    }
    Resize-VHD -Path $Path -SizeBytes $SizeBytes -ErrorAction Stop
    Read-HypervVHDResult -Path $Path
}

# Entry block. Skipped during Pester runs (dot-source sets InvocationName='.').
if ($MyInvocation.InvocationName -ne '.') {
    try {
        $params = Read-HypervStdinPayload
        Set-HypervVHD -Path $params.path -SizeBytes ([int64] $params.size_bytes)
    }
    catch {
        Write-HypervError $_
        exit 1
    }
}
