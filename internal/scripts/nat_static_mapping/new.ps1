# nat_static_mapping/new.ps1 -- create a static NAT port forward + optional
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
#                 read format (same fields as get.ps1).
#
# Cross-resource precondition: nat_name must resolve to an existing
# NetNat instance. Without the precondition, Add-NetNatStaticMapping
# fails with an opaque "no NAT" message that obscures the dependency.
#
# lint:allow-long-comment

# Invoke-WithNetNatRetry is defined in nat_static_mapping/_retry.ps1, which
# the Go-side loadNatStaticMappingWithRetry prepends to this script body
# before sending it to the runner.

# New-HypervNatStaticMapping provisions Add-NetNatStaticMapping, then
# (optionally) New-NetFirewallRule. On firewall failure, the static
# mapping is rolled back -- otherwise an orphan mapping would survive
# on the host and the next apply would trip on the (Protocol,
# ExternalIP, ExternalPort) uniqueness constraint.
function New-HypervNatStaticMapping {
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

    # Capture the fresh StaticMappingID Hyper-V assigns for the rollback path and read-back below.
    $mapping = Invoke-WithNetNatRetry {
        Add-NetNatStaticMapping `
            -NatName $NatName `
            -Protocol $protocolUpper `
            -ExternalIPAddress $ExternalIPAddress `
            -ExternalPort $ExternalPort `
            -InternalIPAddress $InternalIPAddress `
            -InternalPort $InternalPort `
            -ErrorAction Stop
    }

    # A failed firewall rule after the mapping lands would otherwise orphan the mapping; tear it down and re-throw.
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
        }
    }
    catch {
        $original = $_
        try { Remove-NetNatStaticMapping -StaticMappingID $mapping.StaticMappingID -Confirm:$false -ErrorAction Stop }
        catch { $null = $_ }
        throw $original
    }

    # Re-probe the firewall rule so the host's actual state, not the caller's input, is what's reported back.
    $existingFw = Get-NetFirewallRule -DisplayName $FirewallName -ErrorAction SilentlyContinue |
        Select-Object -First 1
    $firewallPresent = $null -ne $existingFw
    $firewallProfile = if ($firewallPresent) { $existingFw.Profile.ToString() } else { '' }

    [pscustomobject]@{
        Id                  = "${NatName}:${Protocol}:${ExternalIPAddress}:${ExternalPort}"
        StaticMappingId     = [int]$mapping.StaticMappingID
        NatName             = $NatName
        Protocol            = $protocolUpper
        ExternalIPAddress   = $ExternalIPAddress
        ExternalPort        = [int]$ExternalPort
        InternalIPAddress   = $InternalIPAddress
        InternalPort        = [int]$InternalPort
        FirewallRulePresent = [bool]$firewallPresent
        FirewallRuleName    = $FirewallName
        FirewallRuleProfile = $firewallProfile
    } | Write-HypervResult
}

# Entry block. Skipped during Pester runs (dot-source sets InvocationName='.').
if ($MyInvocation.InvocationName -ne '.') {
    try {
        $params = Read-HypervStdinPayload
        $fw = $params.firewall
        New-HypervNatStaticMapping `
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
        # Translate the Add-NetNatStaticMapping sharing-violation
        # signature into a clearer "port is in a Windows exclusion range"
        # error before surfacing; pass-through for any other error class.
        $translated = Resolve-NetNatPortConflictMessage `
            -ErrorRecord $_ `
            -Protocol $params.protocol `
            -ExternalPort $params.external_port
        Write-HypervError $translated
        exit 1
    }
}
