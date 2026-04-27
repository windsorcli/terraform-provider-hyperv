# vhd/new.ps1 -- create a new VHD/VHDX file.
#
# Wire contract (locked in by Tests.ps1):
#
#   stdin JSON  : {
#                   "path":             "<absolute-path>",         # required
#                   "vhd_type":         "fixed"|"dynamic"|"differencing",
#                   "size_bytes":       <int64>,                   # required for fixed/dynamic
#                   "parent_path":      "<absolute-path>",         # required for differencing
#                   "block_size_bytes": <int64>                    # optional
#                 }
#   stdout JSON : same shape as get.ps1 (Path, VhdType, SizeBytes, ...).
#
# Mode semantics:
#   fixed         - pre-allocates the full SizeBytes on disk; slow create,
#                   no runtime expansion.
#   dynamic       - sparse VHDX; FileSize starts ~5 MiB and grows toward
#                   SizeBytes as the guest writes blocks.
#   differencing  - read-only parent + writable child; Hyper-V inherits
#                   SizeBytes and BlockSizeBytes from the parent. SizeBytes
#                   in the input is rejected at the schema layer (not here).
#
# Differencing on a missing/invalid parent returns InvalidArgument with
# fullyQualifiedErrorId starting "InvalidParameter,Microsoft.Vhd." -- the
# Go-side errors.go maps that to ErrInvalidParentPath (spike #3).

# Read-HypervVHDResult emits the canonical 8-field shape. Inline duplicate
# of get.ps1's tail because the runtime concatenates only preamble + a
# single verb script per call (no cross-script helpers).
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

# New-HypervVHDFixed creates a pre-allocated VHD/VHDX. -Fixed is the
# Hyper-V switch that selects this layout.
function New-HypervVHDFixed {
    [CmdletBinding()]
    param(
        [Parameter(Mandatory)] [string] $Path,
        [Parameter(Mandatory)] [int64]  $SizeBytes,
        [Nullable[int64]]               $BlockSizeBytes
    )
    $newArgs = @{
        Path        = $Path
        SizeBytes   = $SizeBytes
        Fixed       = $true
        ErrorAction = 'Stop'
    }
    if ($null -ne $BlockSizeBytes) {
        $newArgs.BlockSizeBytes = [int64] $BlockSizeBytes
    }
    New-VHD @newArgs | Out-Null
    Read-HypervVHDResult -Path $Path
}

# New-HypervVHDDynamic creates a sparse VHD/VHDX. -Dynamic is the default
# for New-VHD when neither -Fixed nor -Differencing is set, but we pass it
# explicitly so the contract is unambiguous.
function New-HypervVHDDynamic {
    [CmdletBinding()]
    param(
        [Parameter(Mandatory)] [string] $Path,
        [Parameter(Mandatory)] [int64]  $SizeBytes,
        [Nullable[int64]]               $BlockSizeBytes
    )
    $newArgs = @{
        Path        = $Path
        SizeBytes   = $SizeBytes
        Dynamic     = $true
        ErrorAction = 'Stop'
    }
    if ($null -ne $BlockSizeBytes) {
        $newArgs.BlockSizeBytes = [int64] $BlockSizeBytes
    }
    New-VHD @newArgs | Out-Null
    Read-HypervVHDResult -Path $Path
}

# New-HypervVHDDifferencing creates a child that reads from -ParentPath
# and writes new blocks locally. Size and block size are inherited from
# the parent and rejected if supplied (Hyper-V rule).
function New-HypervVHDDifferencing {
    [CmdletBinding()]
    param(
        [Parameter(Mandatory)] [string] $Path,
        [Parameter(Mandatory)] [string] $ParentPath
    )
    New-VHD -Path $Path -ParentPath $ParentPath -Differencing -ErrorAction Stop | Out-Null
    Read-HypervVHDResult -Path $Path
}

# Entry block. Skipped during Pester runs (dot-source sets InvocationName='.').
if ($MyInvocation.InvocationName -ne '.') {
    try {
        $params = [Console]::In.ReadToEnd() | ConvertFrom-Json

        switch ($params.vhd_type) {
            'fixed' {
                $callArgs = @{
                    Path      = $params.path
                    SizeBytes = [int64] $params.size_bytes
                }
                if ($params.PSObject.Properties.Name -contains 'block_size_bytes' -and
                    $null -ne $params.block_size_bytes) {
                    $callArgs.BlockSizeBytes = [int64] $params.block_size_bytes
                }
                New-HypervVHDFixed @callArgs
            }
            'dynamic' {
                $callArgs = @{
                    Path      = $params.path
                    SizeBytes = [int64] $params.size_bytes
                }
                if ($params.PSObject.Properties.Name -contains 'block_size_bytes' -and
                    $null -ne $params.block_size_bytes) {
                    $callArgs.BlockSizeBytes = [int64] $params.block_size_bytes
                }
                New-HypervVHDDynamic @callArgs
            }
            'differencing' {
                New-HypervVHDDifferencing `
                    -Path       $params.path `
                    -ParentPath $params.parent_path
            }
            default {
                throw "Unknown vhd_type '$($params.vhd_type)'; expected 'fixed', 'dynamic', or 'differencing'."
            }
        }
    }
    catch {
        Write-HypervError $_
        exit 1
    }
}
