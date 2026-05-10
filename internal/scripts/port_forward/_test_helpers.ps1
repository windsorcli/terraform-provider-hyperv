# _test_helpers.ps1 -- shared Pester setup for the port_forward verb
# scripts. Underscore prefix keeps it out of Pester's *.Tests.ps1
# discovery glob.
#
# Stubs for the NetNat / NetFirewall cmdlets the port_forward scripts
# call. Defined unconditionally (not gated on `Get-Command`) on purpose:
# when the real NetNat / NetSecurity modules are present (Windows
# runners), their parameter sets require certain combinations -- e.g.
# `Add-NetNatStaticMapping -NatName -Protocol` without the address /
# port pair doesn't resolve cleanly on PS 5.1, and the binder rejects
# the call before Pester's mock body runs, returning a zero-call count.
# Defining stubs in this script's scope shadows the module cmdlets in
# the BeforeAll dot-source scope, so Pester mocks the simple stub
# surface (no parameter sets, no validators) and ParameterFilters see
# the bound values consistently across PS 5.1 / 7.x.
#
# In production scripts run via -EncodedCommand in a fresh runspace,
# the real cmdlets are still used; this shadow only applies to test
# execution.

# NetNat singleton-resolution: port_forward references an existing NAT
# by name (provider precondition). Get-NetNat is the cross-resource
# lookup; Add/Remove/Get-NetNatStaticMapping are the actual port-
# forward cmdlets.
function Get-NetNat {
    [CmdletBinding()]
    param(
        [string] $Name
    )
}

function Add-NetNatStaticMapping {
    [CmdletBinding()]
    param(
        [string] $NatName,
        [string] $Protocol,
        [string] $ExternalIPAddress,
        [int]    $ExternalPort,
        [string] $InternalIPAddress,
        [int]    $InternalPort
    )
}

function Get-NetNatStaticMapping {
    [CmdletBinding()]
    param(
        [string] $NatName,
        [int]    $StaticMappingID
    )
}

function Remove-NetNatStaticMapping {
    [CmdletBinding()]
    param(
        [int]    $StaticMappingID,
        [switch] $Confirm
    )
}

# NetFirewall: optional companion. The port_forward resource opens the
# inbound port via New-NetFirewallRule by default; nested block
# enabled=false skips it.
function Get-NetFirewallRule {
    [CmdletBinding()]
    param(
        [string] $DisplayName
    )
}

function New-NetFirewallRule {
    [CmdletBinding()]
    param(
        [string] $DisplayName,
        [string] $Direction,
        [string] $Action,
        [string] $Protocol,
        [int]    $LocalPort,
        [string] $Profile
    )
}

function Set-NetFirewallRule {
    [CmdletBinding()]
    param(
        [string] $DisplayName,
        [bool]   $Enabled,
        [string] $Profile
    )
}

function Remove-NetFirewallRule {
    [CmdletBinding()]
    param(
        [string] $DisplayName
    )
}

# New-HypervPortForwardSample builds a PSCustomObject shaped like a real
# Get-NetNatStaticMapping result. Field set mirrors what the canonical
# read shape projects.
function New-HypervPortForwardSample {
    [CmdletBinding()]
    param(
        [int]    $StaticMappingID = 1,
        [string] $NatName = 'windsor-nat',
        [string] $Protocol = 'TCP',
        [string] $ExternalIPAddress = '0.0.0.0',
        [int]    $ExternalPort = 80,
        [string] $InternalIPAddress = '192.168.100.10',
        [int]    $InternalPort = 30080
    )
    [pscustomobject]@{
        StaticMappingID   = $StaticMappingID
        NatName           = $NatName
        Protocol          = $Protocol
        ExternalIPAddress = $ExternalIPAddress
        ExternalPort      = $ExternalPort
        InternalIPAddress = $InternalIPAddress
        InternalPort      = $InternalPort
    }
}

# New-HypervFirewallRuleSample builds a PSCustomObject shaped like a
# real Get-NetFirewallRule result. Only the fields the canonical read
# shape consumes (DisplayName, Profile, Enabled) are populated.
function New-HypervFirewallRuleSample {
    [CmdletBinding()]
    param(
        [string] $DisplayName = 'windsor-pf-tcp-80',
        [string] $Profile = 'Any',
        [string] $Enabled = 'True'
    )
    [pscustomobject]@{
        DisplayName = $DisplayName
        Profile     = $Profile
        Enabled     = $Enabled
    }
}

# New-HypervNetNatSample mirrors the vswitch helper: the precondition
# probe in port_forward/new.ps1 (Get-NetNat -Name <nat_name>) returns
# this shape on success, $null on missing.
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
