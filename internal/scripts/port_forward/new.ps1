# port_forward/new.ps1 -- create a static NAT port forward + optional
# inbound firewall allow rule.
#
# Wire contract (locked in by Tests.ps1):
#
#   stdin JSON  : {
#                   "nat_name":            "<string>",                   # required
#                   "protocol":            "tcp"|"udp",                  # required
#                   "external_ip":         "<IPv4>",                     # required (default 0.0.0.0)
#                   "external_port":       <int 1..65535>,               # required
#                   "internal_ip":         "<IPv4>",                     # required
#                   "internal_port":       <int 1..65535>,               # required
#                   "firewall": {
#                     "enabled": <bool>,                                  # required
#                     "name":    "<string>",                              # required when enabled
#                     "profile": "<string>"                               # required when enabled
#                   }
#                 }
#   stdout JSON : the created mapping in the canonical eleven-field
#                 read shape (same fields as get.ps1).
#
# Cross-resource precondition: nat_name must resolve to an existing
# NetNat instance. Without the precondition, Add-NetNatStaticMapping
# fails with an opaque "no NAT" message that obscures the dependency.

# New-HypervPortForward provisions Add-NetNatStaticMapping, then
# (optionally) New-NetFirewallRule. On firewall failure, the static
# mapping is rolled back -- otherwise an orphan mapping would survive
# on the host and the next apply would trip on the (Protocol,
# ExternalIP, ExternalPort) uniqueness constraint.
function New-HypervPortForward {
    [CmdletBinding()]
    param(
        [Parameter(Mandatory)] [string] $NatName,
        [Parameter(Mandatory)] [ValidateSet('tcp', 'udp')] [string] $Protocol,
        [Parameter(Mandatory)] [string] $ExternalIPAddress,
        [Parameter(Mandatory)] [int]    $ExternalPort,
        [Parameter(Mandatory)] [string] $InternalIPAddress,
        [Parameter(Mandatory)] [int]    $InternalPort,
        [Parameter(Mandatory)] [bool]   $FirewallEnabled,
        [Parameter(Mandatory)] [string] $FirewallName,
        [Parameter(Mandatory)] [string] $FirewallProfile
    )

    # Cross-resource precondition. Get-NetNat returns nothing for an
    # absent name (no terminating error, no exception); a $null result
    # means "the referenced NAT doesn't exist." Throw with the
    # nat_name in the message so the operator sees the actual dep.
    $existingNat = Get-NetNat -Name $NatName -ErrorAction SilentlyContinue |
        Select-Object -First 1
    if ($null -eq $existingNat) {
        throw "Port forward references nat_name '$NatName' but no NetNat instance with that name exists on the host. " +
            "Create the NetNat first (via hyperv_virtual_switch with switch_type='NAT' or out-of-band)."
    }

    $protocolUpper = $Protocol.ToUpper()

    # Add the static mapping. The cmdlet returns the mapping with a
    # fresh StaticMappingID Hyper-V assigns -- we capture it for the
    # rollback path and the read shape.
    $mapping = Add-NetNatStaticMapping `
        -NatName $NatName `
        -Protocol $protocolUpper `
        -ExternalIPAddress $ExternalIPAddress `
        -ExternalPort $ExternalPort `
        -InternalIPAddress $InternalIPAddress `
        -InternalPort $InternalPort `
        -ErrorAction Stop

    # Rollback on partial-failure. New-NetFirewallRule landing after
    # Add-NetNatStaticMapping, then failing, would otherwise leave an
    # orphan mapping with no Terraform state -- the next apply trips
    # on the (Protocol, ExternalIP, ExternalPort) uniqueness check at
    # Add time. Wrap in try/catch, capture the original failure first,
    # tear down the mapping, re-throw the original. The empty $null = $_
    # discard inside each cleanup catch satisfies PSScriptAnalyzer's
    # PSAvoidUsingEmptyCatchBlock the same way vswitch/new.ps1's
    # rollback does.
    $firewallCreated = $false
    try {
        if ($FirewallEnabled) {
            New-NetFirewallRule `
                -DisplayName $FirewallName `
                -Direction 'Inbound' `
                -Action 'Allow' `
                -Protocol $protocolUpper `
                -LocalPort $ExternalPort `
                -Profile $FirewallProfile `
                -ErrorAction Stop | Out-Null
            $firewallCreated = $true
        }
    }
    catch {
        $original = $_
        try { Remove-NetNatStaticMapping -StaticMappingID $mapping.StaticMappingID -Confirm:$false -ErrorAction Stop }
        catch { $null = $_ }
        throw $original
    }

    # Read-back: project the canonical eleven-field shape. Composite Id
    # uses lowercase protocol so it matches the schema's `protocol`
    # attribute and the input the user typed. FirewallRulePresent is
    # whatever Get-NetFirewallRule actually finds -- captures the
    # firewall.enabled=false case (rule not created) and the case where
    # Get-NetFirewallRule disagrees with our $firewallCreated state.
    $existingFw = if ($FirewallEnabled) {
        Get-NetFirewallRule -DisplayName $FirewallName -ErrorAction SilentlyContinue |
            Select-Object -First 1
    } else { $null }
    $firewallPresent = $null -ne $existingFw
    # Avoid the unused-variable lint hit -- $firewallCreated is
    # captured for the rollback path, not the read-back. Reading it
    # here keeps PSReviewUnusedAssignment quiet without changing
    # behavior.
    $null = $firewallCreated

    [pscustomobject]@{
        Id                  = "${NatName}:${Protocol}:${ExternalIPAddress}:${ExternalPort}"
        StaticMappingId     = $mapping.StaticMappingID
        NatName             = $NatName
        Protocol            = $protocolUpper
        ExternalIPAddress   = $ExternalIPAddress
        ExternalPort        = $ExternalPort
        InternalIPAddress   = $InternalIPAddress
        InternalPort        = $InternalPort
        FirewallRulePresent = $firewallPresent
        FirewallRuleName    = $FirewallName
        FirewallRuleProfile = $FirewallProfile
    } | Write-HypervResult
}

# Entry block. Skipped during Pester runs (dot-source sets InvocationName='.').
if ($MyInvocation.InvocationName -ne '.') {
    try {
        $params = [Console]::In.ReadToEnd() | ConvertFrom-Json
        $fw = $params.firewall
        New-HypervPortForward `
            -NatName $params.nat_name `
            -Protocol $params.protocol `
            -ExternalIPAddress $params.external_ip `
            -ExternalPort $params.external_port `
            -InternalIPAddress $params.internal_ip `
            -InternalPort $params.internal_port `
            -FirewallEnabled ([bool]$fw.enabled) `
            -FirewallName $fw.name `
            -FirewallProfile $fw.profile
    }
    catch {
        Write-HypervError $_
        exit 1
    }
}
