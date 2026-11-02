# nat_static_mapping/get.ps1 -- read a static NAT port forward by its
# (nat_name, protocol, external_ip, external_port) lookup tuple, plus
# the optional companion firewall rule.
#
# Wire contract (locked in by Tests.ps1):
#
#   stdin JSON  : {
#                   "nat_name":      "<string>",
#                   "protocol":      "tcp"|"udp",
#                   "external_ip":   "<IPv4>",
#                   "external_port": <int>,
#                   "firewall_name": "<string>"
#                 }
#   stdout JSON : single eleven-field object (Id, StaticMappingId,
#                 NatName, Protocol, ExternalIPAddress, ExternalPort,
#                 InternalIPAddress, InternalPort, FirewallRulePresent,
#                 FirewallRuleName, FirewallRuleProfile).
#   stderr/exit : missing mapping -> ObjectNotFound envelope, exit 1 ->
#                 Go maps to ErrNotFound so Read calls RemoveResource.
#
# lint:allow-long-comment

# Get-HypervNatStaticMapping enumerates the NatStaticMapping list scoped
# to NatName and filters in-process for the (Protocol, ExternalIP,
# ExternalPort) tuple -- the cmdlet has no per-port filter parameter,
# so the script does the matching. Missing tuple throws an explicit
# ObjectNotFound so the typed client maps it to ErrNotFound.
function Get-HypervNatStaticMapping {
    [CmdletBinding()]
    param(
        [Parameter(Mandatory)] [string] $NatName,
        [Parameter(Mandatory)] [ValidateSet('tcp', 'udp')] [string] $Protocol,
        [Parameter(Mandatory)] [string] $ExternalIPAddress,
        [Parameter(Mandatory)] [int]    $ExternalPort,
        [Parameter(Mandatory)] [string] $FirewallName
    )

    $protocolUpper = $Protocol.ToUpper()

    # NetNat allows the same external_port across protocols (TCP/80 +
    # UDP/80 coexist). Match ALL of (protocol, external_ip,
    # external_port) -- a port-only match would silently return the
    # wrong mapping.
    $mapping = Get-NetNatStaticMapping -NatName $NatName -ErrorAction SilentlyContinue |
        Where-Object {
            $_.Protocol -eq $protocolUpper -and
            $_.ExternalIPAddress -eq $ExternalIPAddress -and
            $_.ExternalPort -eq $ExternalPort
        } |
        Select-Object -First 1

    if ($null -eq $mapping) {
        $exception = [System.Management.Automation.ItemNotFoundException]::new(
            "No NAT static mapping found for nat_name='$NatName', protocol='$Protocol', " +
                "external_ip='$ExternalIPAddress', external_port='$ExternalPort'.")
        $errorRecord = [System.Management.Automation.ErrorRecord]::new(
            $exception, 'NatStaticMappingNotFound',
            [System.Management.Automation.ErrorCategory]::ObjectNotFound,
            "$NatName/$Protocol/$ExternalIPAddress/$ExternalPort")
        throw $errorRecord
    }

    # A missing firewall rule leaves the mapping in state; only the mapping decides whether the resource exists.
    $firewall = Get-HypervFirewallRuleState -Name $FirewallName

    [pscustomobject]@{
        Id                  = "${NatName}:${Protocol}:${ExternalIPAddress}:${ExternalPort}"
        StaticMappingId     = [int]$mapping.StaticMappingID
        NatName             = $NatName
        Protocol            = $protocolUpper
        ExternalIPAddress   = $mapping.ExternalIPAddress
        ExternalPort        = [int]$mapping.ExternalPort
        InternalIPAddress   = $mapping.InternalIPAddress
        InternalPort        = [int]$mapping.InternalPort
        FirewallRulePresent = [bool]$firewall.Present
        FirewallRuleName    = $FirewallName
        FirewallRuleProfile = $firewall.Profile
    } | Write-HypervResult
}

# Entry block. Skipped during Pester runs (dot-source sets InvocationName='.').
if ($MyInvocation.InvocationName -ne '.') {
    try {
        $params = Read-HypervStdinPayload
        Get-HypervNatStaticMapping `
            -NatName $params.nat_name `
            -Protocol $params.protocol `
            -ExternalIPAddress $params.external_ip `
            -ExternalPort $params.external_port `
            -FirewallName $params.firewall_name
    }
    catch {
        Write-HypervError $_
        exit 1
    }
}
