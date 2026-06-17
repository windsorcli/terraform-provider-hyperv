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
# Go-side errors.go maps that to ErrInvalidParentPath.

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

# Invoke-HypervVHDNew dispatches a parsed-JSON $Params object to
# the correct New-HypervVHD* function. Extracted from the entry block so
# the JSON-to-args translation (in particular the size_bytes presence
# guard) is directly Pester-testable without spawning a subprocess.
#
# size_bytes presence guard: [int64] $null silently coerces to 0, which
# New-VHD then rejects with the opaque "The parameter is incorrect"
# message. Throwing here surfaces "size_bytes is required for <mode> VHDs"
# instead. The Go-side validator catches this in normal operation; this
# is the script-layer defense in depth (mirrors the explicit null check
# already used for block_size_bytes).
function Invoke-HypervVHDNew {
    [CmdletBinding()]
    param(
        [Parameter(Mandatory)] $Params
    )
    switch ($Params.vhd_type) {
        'fixed' {
            # Two-stage check: the property-list guard is required because
            # StrictMode 3.0 throws on access to undefined PSObject properties
            # (omitted-from-JSON case). The null check then catches the
            # explicit `"size_bytes": null` case.
            if ($Params.PSObject.Properties.Name -notcontains 'size_bytes' -or
                $null -eq $Params.size_bytes) {
                throw "size_bytes is required for fixed VHDs"
            }
            $callArgs = @{
                Path      = $Params.path
                SizeBytes = [int64] $Params.size_bytes
            }
            if ($Params.PSObject.Properties.Name -contains 'block_size_bytes' -and
                $null -ne $Params.block_size_bytes) {
                $callArgs.BlockSizeBytes = [int64] $Params.block_size_bytes
            }
            New-HypervVHDFixed @callArgs
        }
        'dynamic' {
            if ($Params.PSObject.Properties.Name -notcontains 'size_bytes' -or
                $null -eq $Params.size_bytes) {
                throw "size_bytes is required for dynamic VHDs"
            }
            $callArgs = @{
                Path      = $Params.path
                SizeBytes = [int64] $Params.size_bytes
            }
            if ($Params.PSObject.Properties.Name -contains 'block_size_bytes' -and
                $null -ne $Params.block_size_bytes) {
                $callArgs.BlockSizeBytes = [int64] $Params.block_size_bytes
            }
            New-HypervVHDDynamic @callArgs
        }
        'differencing' {
            New-HypervVHDDifferencing `
                -Path       $Params.path `
                -ParentPath $Params.parent_path
        }
        default {
            throw "Unknown vhd_type '$($Params.vhd_type)'; expected 'fixed', 'dynamic', or 'differencing'."
        }
    }
}

# Entry block. Skipped during Pester runs (dot-source sets InvocationName='.').
if ($MyInvocation.InvocationName -ne '.') {
    try {
        $params = Read-HypervStdinParams
        Invoke-HypervVHDNew -Params $params
    }
    catch {
        Write-HypervError $_
        exit 1
    }
}
