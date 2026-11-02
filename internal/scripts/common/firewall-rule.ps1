# common/firewall-rule.ps1 -- the firewall_rule helpers shared by nat_static_mapping and port_proxy.
# The Go side prepends this body to each of their verb scripts; Pester dot-sources it.

# Get-HypervFirewallRuleState reports the rule as present only when it exists and is enabled,
# since Sync-HypervFirewallRule disables the rule rather than deleting it when enabled is false.
function Get-HypervFirewallRuleState {
    [CmdletBinding()]
    param(
        [Parameter(Mandatory)] [string] $Name
    )
    $rule = Get-NetFirewallRule -DisplayName $Name -ErrorAction SilentlyContinue | Select-Object -First 1
    [pscustomobject]@{
        Present = $null -ne $rule -and $rule.Enabled.ToString() -eq 'True'
        # Profile is a flags enum; ToString() gives the named form ('Any', 'Domain, Private') the client decodes.
        Profile = if ($null -ne $rule) { $rule.Profile.ToString() } else { '' }
    }
}

# New-HypervFirewallRule opens LocalPort inbound, scoped to LocalAddress when the caller passes one.
function New-HypervFirewallRule {
    [CmdletBinding()]
    param(
        [Parameter(Mandatory)] [string] $Name,
        [Parameter(Mandatory)] [ValidateSet('TCP', 'UDP')] [string] $Protocol,
        [Parameter(Mandatory)] [int]    $LocalPort,
        [Parameter(Mandatory)] [string] $FirewallProfile,
        [string] $LocalAddress
    )
    $ruleParams = @{
        DisplayName = $Name
        Direction   = 'Inbound'
        Action      = 'Allow'
        Protocol    = $Protocol
        LocalPort   = $LocalPort
        Profile     = $FirewallProfile
    }
    if ($LocalAddress) { $ruleParams.LocalAddress = $LocalAddress }
    New-NetFirewallRule @ruleParams -ErrorAction Stop | Out-Null
}

# Sync-HypervFirewallRule updates an existing rule in place, enabling or disabling it to match Enabled.
# It recreates the rule when Enabled is true and the rule is gone, such as after an out-of-band delete.
function Sync-HypervFirewallRule {
    [CmdletBinding()]
    param(
        [Parameter(Mandatory)] [bool]   $Enabled,
        [Parameter(Mandatory)] [string] $Name,
        [Parameter(Mandatory)] [ValidateSet('TCP', 'UDP')] [string] $Protocol,
        [Parameter(Mandatory)] [int]    $LocalPort,
        [Parameter(Mandatory)] [string] $FirewallProfile,
        [string] $LocalAddress
    )
    $existing = Get-NetFirewallRule -DisplayName $Name -ErrorAction SilentlyContinue | Select-Object -First 1
    if ($null -ne $existing) {
        # -Enabled binds to the NetSecurity.Enabled enum, whose members are the strings 'True' and 'False'.
        $setParams = @{
            DisplayName = $Name
            Enabled     = if ($Enabled) { 'True' } else { 'False' }
            Profile     = $FirewallProfile
        }
        if ($LocalAddress) { $setParams.LocalAddress = $LocalAddress }
        Set-NetFirewallRule @setParams -ErrorAction Stop
    }
    elseif ($Enabled) {
        New-HypervFirewallRule -Name $Name -Protocol $Protocol -LocalPort $LocalPort `
            -FirewallProfile $FirewallProfile -LocalAddress $LocalAddress
    }
}

# Remove-HypervFirewallRule deletes the rule, treating a rule that is already gone as success.
function Remove-HypervFirewallRule {
    [CmdletBinding()]
    param(
        [Parameter(Mandatory)] [string] $Name
    )
    $existing = Get-NetFirewallRule -DisplayName $Name -ErrorAction SilentlyContinue | Select-Object -First 1
    if ($null -ne $existing) {
        Remove-NetFirewallRule -DisplayName $Name -ErrorAction Stop
    }
}
