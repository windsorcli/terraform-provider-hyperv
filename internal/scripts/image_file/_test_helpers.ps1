# _test_helpers.ps1 -- shared Pester setup for the image_file verb scripts.
# Underscore prefix keeps it out of Pester's *.Tests.ps1 discovery glob.
#
# Stubs for the cmdlets the image_file scripts call. Stubbed for the same
# reason vswitch stubs Hyper-V cmdlets: simple parameter sets sidestep the
# parameter-binding/Pester-mock interaction that drops bound values on
# PS 5.1.
#
# Note Save-HypervHttpFile (the System.Net.Http.HttpClient wrapper that
# new.ps1 uses for url-mode downloads) is NOT stubbed here -- it's defined
# in new.ps1 itself, so dot-sourcing makes it available for direct Pester
# mocking. Wrapping the .NET call in a function is what makes it mockable
# at all (Pester can't intercept .NET method invocations).
#
# In production scripts run via -EncodedCommand in a fresh runspace, so the
# real cmdlets are still used; this shadow only applies to test execution.

function Get-FileHash {
    [CmdletBinding()]
    param(
        [string] $LiteralPath,
        [string] $Algorithm
    )
}

function Test-Path {
    [CmdletBinding()]
    param(
        [string] $LiteralPath,
        [string] $PathType
    )
}

function Get-Item {
    [CmdletBinding()]
    param(
        [string] $LiteralPath
    )
}

function Move-Item {
    [CmdletBinding()]
    param(
        [string] $LiteralPath,
        [string] $Destination,
        [switch] $Force
    )
}

function Remove-Item {
    [CmdletBinding()]
    param(
        [string] $LiteralPath,
        [switch] $Force
    )
}

# New-HypervImageFileSample builds a Get-Item-shaped object for use as the
# canned return value from Mock blocks. Only the fields the image_file
# scripts read off the Get-Item result are populated.
function New-HypervImageFileSample {
    [CmdletBinding()]
    param(
        [string] $FullName = 'C:\hyperv\images\sample.vhdx',
        [int64]  $Length   = 1073741824
    )
    [pscustomobject]@{
        FullName = $FullName
        Length   = $Length
    }
}

# New-HypervImageFileHashSample builds a Get-FileHash-shaped object. The real
# cmdlet returns Hash uppercased; the canonical hash is lowercased -- tests
# default to uppercase so they exercise the lowercasing path.
function New-HypervImageFileHashSample {
    [CmdletBinding()]
    param(
        [string] $Hash = 'ABCDEF0123456789ABCDEF0123456789ABCDEF0123456789ABCDEF0123456789'
    )
    [pscustomobject]@{
        Hash = $Hash
    }
}
