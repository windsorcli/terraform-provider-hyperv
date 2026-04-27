# _test_helpers.ps1 -- shared Pester setup for the vhd verb scripts.
# Underscore prefix keeps it out of Pester's *.Tests.ps1 discovery glob.
#
# Stubs for the Hyper-V cmdlets the vhd scripts call. Same rationale as
# vswitch's test helper: when the real Hyper-V module is loaded, its
# parameter sets impose constraints that drop bound values during Pester
# mock interactions on PS 5.1. Stub functions with simple parameter sets
# sidestep that.
#
# In production scripts run via -EncodedCommand in a fresh runspace, so
# the real cmdlets are still used; this shadow only applies to test
# execution.

function Get-VHD {
    [CmdletBinding()]
    param(
        [Parameter(Position = 0)] [string] $Path
    )
}

function New-VHD {
    [CmdletBinding()]
    param(
        [string] $Path,
        [int64]  $SizeBytes,
        [string] $ParentPath,
        [int64]  $BlockSizeBytes,
        [switch] $Fixed,
        [switch] $Dynamic,
        [switch] $Differencing
    )
}

function Resize-VHD {
    [CmdletBinding()]
    param(
        [string] $Path,
        [int64]  $SizeBytes
    )
}

function Test-Path {
    [CmdletBinding()]
    param(
        [string] $LiteralPath,
        [string] $PathType
    )
}

function Remove-Item {
    [CmdletBinding()]
    param(
        [string] $LiteralPath,
        [switch] $Force
    )
}

# New-HypervVHDSample builds a Get-VHD-shaped object for use as the canned
# return value from Mock blocks. Defaults model a typical 32 GiB dynamic
# VHDX; per-test overrides cover differencing and fixed shapes.
function New-HypervVHDSample {
    [CmdletBinding()]
    param(
        [string] $Path           = 'C:\hyperv\vhds\sample.vhdx',
        [string] $VhdType        = 'Dynamic',
        [int64]  $Size           = 34359738368,   # 32 GiB
        [int64]  $FileSize       = 4194304,        # ~4 MiB sparse
        [int64]  $BlockSize      = 33554432,       # 32 MiB (VHDX default)
        [string] $ParentPath     = '',
        [string] $VhdFormat      = 'VHDX',
        [bool]   $Attached       = $false
    )
    [pscustomobject]@{
        Path       = $Path
        VhdType    = $VhdType
        Size       = $Size
        FileSize   = $FileSize
        BlockSize  = $BlockSize
        ParentPath = $ParentPath
        VhdFormat  = $VhdFormat
        Attached   = $Attached
    }
}
