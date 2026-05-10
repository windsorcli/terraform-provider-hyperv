# port_forward/remove.ps1 -- delete a static NAT port forward and its
# companion firewall rule (if present).
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
#   stdout      : empty (caller passes dst=nil to runScript).
#
# Best-effort destroy: a missing static mapping or missing firewall
# rule is treated as success (the goal is "no mapping/rule by these
# identifiers exists," and that's already true).

function Remove-HypervPortForward {
    [CmdletBinding()]
    param(
        [Parameter(Mandatory)] [string] $NatName,
        [Parameter(Mandatory)] [ValidateSet('tcp', 'udp')] [string] $Protocol,
        [Parameter(Mandatory)] [string] $ExternalIPAddress,
        [Parameter(Mandatory)] [int]    $ExternalPort,
        [Parameter(Mandatory)] [string] $FirewallName
    )

    $protocolUpper = $Protocol.ToUpper()

    # Lookup the mapping by tuple, then Remove by StaticMappingID. The
    # cmdlet has no per-tuple delete; you delete by ID. If the lookup
    # finds nothing, the mapping is already gone -- skip the Remove.
    $existing = Get-NetNatStaticMapping -NatName $NatName -ErrorAction SilentlyContinue |
        Where-Object {
            $_.Protocol -eq $protocolUpper -and
            $_.ExternalIPAddress -eq $ExternalIPAddress -and
            $_.ExternalPort -eq $ExternalPort
        } |
        Select-Object -First 1

    if ($null -ne $existing) {
        Remove-NetNatStaticMapping -StaticMappingID $existing.StaticMappingID `
            -Confirm:$false -ErrorAction Stop
    }

    $existingFw = Get-NetFirewallRule -DisplayName $FirewallName -ErrorAction SilentlyContinue |
        Select-Object -First 1
    if ($null -ne $existingFw) {
        Remove-NetFirewallRule -DisplayName $FirewallName -ErrorAction Stop
    }
}

# Entry block. Skipped during Pester runs (dot-source sets InvocationName='.').
if ($MyInvocation.InvocationName -ne '.') {
    try {
        $params = [Console]::In.ReadToEnd() | ConvertFrom-Json
        Remove-HypervPortForward `
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
