# _test_helpers.ps1 -- shared Pester setup for the netnat verb scripts,
# underscore-prefixed to stay out of Pester's *.Tests.ps1 discovery
# glob. Stubs for the NetNat cmdlets the sweep script calls, defined
# unconditionally (not gated on `Get-Command`) so Pester parameter-filter
# binding works consistently across PS 5.1 / 7.x even when the NetNat
# module isn't installed (macOS dev hosts), same rationale as
# vswitch/_test_helpers.ps1.

function Get-NetNat {
    [CmdletBinding()]
    param(
        [Parameter(Position = 0)] [string] $Name
    )
}

function Remove-NetNat {
    [CmdletBinding()]
    param(
        [string] $Name,
        [switch] $Confirm
    )
}

# New-HypervNetNatSample builds a PSCustomObject modeled on a real
# Get-NetNat result. Only Name is populated, since that's all the
# sweeper consumes.
function New-HypervNetNatSample {
    [CmdletBinding()]
    param(
        [string] $Name = 'tfacc-nat-sample'
    )
    [pscustomobject]@{
        Name = $Name
    }
}
