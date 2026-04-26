# _test_helpers.ps1 -- shared Pester setup for the vswitch verb scripts.
# Underscore prefix keeps it out of Pester's *.Tests.ps1 discovery glob.
#
# Stubs for the Hyper-V cmdlets the vswitch scripts call. Defined
# unconditionally (not gated on `Get-Command`) on purpose: when the real
# Hyper-V module is present (Windows runners), its Set-VMSwitch parameter
# sets require certain combinations -- e.g. -Name + -NetAdapterName without
# -SwitchType doesn't resolve to a complete parameter set on PS 5.1 and the
# binder rejects the call before Pester's mock body runs, returning a
# zero-call count. Defining stubs in this script's scope shadows the module
# cmdlets in the BeforeAll dot-source scope, so Pester mocks the simple
# stub surface (no parameter sets, no validators) and ParameterFilters see
# the bound values consistently across PS 5.1 and 7.x.
#
# In production scripts run via -EncodedCommand in a fresh runspace, so the
# real Hyper-V cmdlets are still used; this shadow only applies to test
# execution.

function Get-VMSwitch {
    [CmdletBinding()]
    param(
        [Parameter(Position = 0)] [string] $Name
    )
}

function New-VMSwitch {
    [CmdletBinding()]
    param(
        [string]   $Name,
        [string]   $SwitchType,
        [string[]] $NetAdapterName,
        [bool]     $AllowManagementOS,
        [string]   $Notes
    )
}

function Set-VMSwitch {
    [CmdletBinding()]
    param(
        [string]   $Name,
        [string[]] $NetAdapterName,
        [bool]     $AllowManagementOS,
        [string]   $Notes
    )
}

function Remove-VMSwitch {
    [CmdletBinding()]
    param(
        [string] $Name,
        [switch] $Force
    )
}

# New-HypervSwitchSample builds a PSCustomObject shaped like a real
# Get-VMSwitch result, used as the canned return value from Mock blocks. The
# shape mirrors what spike #2 captured on Server 2022 + PS 5.1.
function New-HypervSwitchSample {
    [CmdletBinding()]
    param(
        [string] $Name = 'sw0',
        [string] $SwitchType = 'External',
        [bool]   $AllowManagementOS = $true,
        [string] $NetAdapterInterfaceDescription = 'Intel(R) Ethernet I210',
        [string] $Notes = '',
        [string] $Id = '12345678-1234-5678-1234-567812345678'
    )
    [pscustomobject]@{
        Name                           = $Name
        SwitchType                     = $SwitchType
        AllowManagementOS              = $AllowManagementOS
        NetAdapterInterfaceDescription = $NetAdapterInterfaceDescription
        Notes                          = $Notes
        Id                             = [guid]$Id
    }
}
