# _test_helpers.ps1 -- shared Pester setup for the nat_static_mapping
# verb scripts, underscore-prefixed to stay out of Pester's
# *.Tests.ps1 glob. Stubs the NetNat / NetFirewall cmdlets
# unconditionally, since the real modules' parameter sets reject some
# bound-value combinations before Pester's mock body runs; these
# simplified stubs shadow them so ParameterFilters see bound values
# consistently. Production scripts run via -EncodedCommand in a fresh
# runspace and never see it.

# NetNat singleton-resolution: nat_static_mapping references an existing NAT
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

# NetFirewall: optional companion. The nat_static_mapping resource opens the
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
        # Production cmdlet's -Enabled binds to the
        # Microsoft.PowerShell.Cmdletization.GeneratedTypes.NetSecurity.Enabled
        # enum whose string values are "True" and "False" -- NOT to
        # [bool]. Stub mirrors the string surface so production code's
        # `-Enabled "True"` form binds cleanly under Pester.
        [string] $Enabled,
        [string] $Profile
    )
}

function Remove-NetFirewallRule {
    [CmdletBinding()]
    param(
        [string] $DisplayName
    )
}

# New-HypervNatStaticMappingSample builds a PSCustomObject modeled on a real
# Get-NetNatStaticMapping result. Field set mirrors the canonical read format.
function New-HypervNatStaticMappingSample {
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

# New-HypervFirewallRuleSample builds a PSCustomObject modeled on a
# real Get-NetFirewallRule result. Only the fields the canonical read
# format consumes (DisplayName, Profile, Enabled) are populated.
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
# probe in nat_static_mapping/new.ps1 (Get-NetNat -Name <nat_name>) returns
# this structure on success, $null on missing.
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
