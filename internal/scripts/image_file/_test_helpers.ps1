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

function Copy-Item {
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

# Stubbed for sweep.ps1's directory enumeration.
function Get-ChildItem {
    [CmdletBinding()]
    param(
        [string] $LiteralPath,
        [string] $Filter
    )
}

# Get-ChildItem-shaped fixture for sweep tests. Mirrors the
# vhd-side helper of the same name but lives here so the image_file
# Pester suite doesn't cross-source vhd helpers.
function New-HypervChildItemSample {
    [CmdletBinding()]
    param(
        [Parameter(Mandatory)] [string] $Name,
        [string] $ParentDir = 'C:\hyperv\tfacc',
        [bool]   $PSIsContainer = $false
    )
    # String-concat over Join-Path: PS 7 on macOS resolves the parent
    # through the PSDrive registry and errors on C:. The fixture only
    # needs a synthesized string.
    [pscustomobject]@{
        Name          = $Name
        Extension     = [System.IO.Path]::GetExtension($Name)
        FullName      = "${ParentDir}\${Name}"
        PSIsContainer = $PSIsContainer
    }
}

# Hyper-V cmdlets used by the dvd-aware replace helper (see new.ps1's
# Invoke-HypervDvdSafeReplace). Stubbed here for the same parameter-binding
# reason the rest of these stubs exist: PS 5.1 + Pester drops bound values
# when the real cmdlet's full parameter set is loaded.
function Get-VM {
    [CmdletBinding()]
    param(
        [Parameter(Position = 0)] [string] $Name
    )
}

function Get-VMDvdDrive {
    [CmdletBinding()]
    param(
        [Parameter(Position = 0)] $VM,
        [string] $VMName,
        [int]    $ControllerNumber,
        [int]    $ControllerLocation
    )
}

function Set-VMDvdDrive {
    [CmdletBinding()]
    param(
        [string] $VMName,
        [int]    $ControllerNumber,
        [int]    $ControllerLocation,
        [AllowNull()] [string] $Path
    )
}

# New-HypervImageFileVMSample builds a Get-VM-shaped object for use in
# Mock blocks. Only the .Name property is consumed by the dvd-aware
# replace helper (it walks Get-VM | ... | Get-VMDvdDrive -VMName $vm.Name),
# so the sample is intentionally minimal.
function New-HypervImageFileVMSample {
    [CmdletBinding()]
    param(
        [string] $Name = 'sample-vm'
    )
    [pscustomobject]@{
        Name = $Name
    }
}

# New-HypervImageFileVMDvdDriveSample builds a Get-VMDvdDrive-shaped object
# for use as a canned return value from Mock blocks. Mirrors the fields the
# dvd-aware replace helper reads off the cmdlet result: the (VMName,
# ControllerNumber, ControllerLocation) tuple identifies the attachment
# slot, and Path is what the helper compares against $DestinationPath.
function New-HypervImageFileVMDvdDriveSample {
    [CmdletBinding()]
    param(
        [string] $VMName             = 'sample-vm',
        [int]    $ControllerNumber   = 0,
        [int]    $ControllerLocation = 1,
        [string] $Path               = 'C:\hyperv\images\seed.iso'
    )
    [pscustomobject]@{
        VMName             = $VMName
        ControllerNumber   = $ControllerNumber
        ControllerLocation = $ControllerLocation
        Path               = $Path
    }
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
