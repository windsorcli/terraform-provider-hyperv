# vhd/set.ps1 -- the only in-place mutation a VHD supports: resize.
#
# Wire contract (locked in by Tests.ps1):
#
#   stdin JSON  : { "path": "<absolute-path>", "size_bytes": <int64> }
#   stdout JSON : same shape as get.ps1 (re-read after the resize lands).
#
# Other mutations (vhd_type, parent_path, block_size_bytes, path) are
# RequiresReplace at the schema layer and never reach this script.
#
# Resize-VHD constraints worth surfacing as cmdlet errors (we do NOT pre-
# validate -- the cmdlet's diagnostics are clearer than anything we'd write):
#   - Shrink requires the trailing blocks be empty; run Optimize-VHD first.
#   - Online resize works for VHDX on Gen 2 VMs only; Gen 1 must be powered off.
#   - Fixed-format resize rewrites the entire file (slow on multi-GB disks).

# Read-HypervVHDResult emits the canonical 8-field shape. Inline duplicate
# of get.ps1's tail because the runtime concatenates only preamble + a
# single verb script per call.
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
        $params = Read-HypervStdinParams
        Set-HypervVHD -Path $params.path -SizeBytes ([int64] $params.size_bytes)
    }
    catch {
        Write-HypervError $_
        exit 1
    }
}
