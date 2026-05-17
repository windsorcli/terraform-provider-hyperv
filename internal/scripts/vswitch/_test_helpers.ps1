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

# NetNat / NetIPAddress cmdlet stubs for the NAT switch_type branch. Same
# rationale as the Hyper-V stubs above: define unconditionally so Pester
# parameter-filter binding works consistently across PS 5.1 / 7.x even
# when the NetNat / NetTCPIP modules aren't installed (macOS dev hosts).
function Get-NetNat {
    [CmdletBinding()]
    param(
        [Parameter(Position = 0)] [string] $Name
    )
}

function New-NetNat {
    [CmdletBinding()]
    param(
        [string] $Name,
        [string] $InternalIPInterfaceAddressPrefix
    )
}

function Set-NetNat {
    [CmdletBinding()]
    param(
        [string] $Name,
        [string] $InternalIPInterfaceAddressPrefix
    )
}

function Remove-NetNat {
    [CmdletBinding()]
    param(
        [string] $Name,
        [switch] $Confirm
    )
}

function Get-NetIPAddress {
    [CmdletBinding()]
    param(
        [string] $InterfaceAlias,
        [string] $IPAddress,
        [int]    $PrefixLength,
        [string] $AddressFamily
    )
}

function New-NetIPAddress {
    [CmdletBinding()]
    param(
        [string] $InterfaceAlias,
        [string] $IPAddress,
        [int]    $PrefixLength,
        [string] $AddressFamily
    )
}

function Remove-NetIPAddress {
    [CmdletBinding()]
    param(
        [string] $InterfaceAlias,
        [string] $IPAddress,
        [switch] $Confirm
    )
}

# New-HypervSwitchSample builds a PSCustomObject in the form of a real
# Get-VMSwitch result, used as the canned return value from Mock blocks.
# Fields mirror what Get-VMSwitch emits on Windows Server 2022 + PS 5.1.
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

# New-HypervNetNatSample builds a PSCustomObject shaped like a real
# Get-NetNat result. Only the two fields the canonical read shape exposes
# (Name, InternalIPInterfaceAddressPrefix) are populated -- Get-NetNat
# returns more, but the typed contract only consumes these.
function New-HypervNetNatSample {
    [CmdletBinding()]
    param(
        [string] $Name = 'windsor-nat',
        [string] $InternalIPInterfaceAddressPrefix = '192.168.100.0/24'
    )
    [pscustomobject]@{
        Name                             = $Name
        InternalIPInterfaceAddressPrefix = $InternalIPInterfaceAddressPrefix
    }
}

# New-HypervNetIPAddressSample builds a PSCustomObject shaped like a real
# Get-NetIPAddress result for the host vNIC of an Internal/NAT switch. The
# canonical read consumes IPAddress + PrefixLength.
function New-HypervNetIPAddressSample {
    [CmdletBinding()]
    param(
        [string] $InterfaceAlias = 'vEthernet (windsor-nat)',
        [string] $IPAddress = '192.168.100.1',
        [int]    $PrefixLength = 24,
        [string] $AddressFamily = 'IPv4'
    )
    [pscustomobject]@{
        InterfaceAlias = $InterfaceAlias
        IPAddress      = $IPAddress
        PrefixLength   = $PrefixLength
        AddressFamily  = $AddressFamily
    }
}
