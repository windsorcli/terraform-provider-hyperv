# port_proxy/get.ps1 -- read a netsh portproxy v4tov4 rule and its firewall rule.
#
# Wire contract (locked in by Tests.ps1):
#
#   stdin JSON  : {
#                   "listen_address": "<IPv4>",
#                   "listen_port":    <int>,
#                   "firewall_name":  "<string>"
#                 }
#   stdout JSON : single nine-field object (Id, ListenAddress, ListenPort,
#                 ConnectAddress, ConnectPort, FirewallRulePresent,
#                 FirewallRuleName, FirewallRuleProfile, Listening).
#                 Listening is false when the rule exists but the IP
#                 Helper service isn't listening on it.
#   stderr/exit : missing rule -> ObjectNotFound envelope, exit 1 ->
#                 Go maps to ErrNotFound so Read calls RemoveResource.
#
# lint:allow-long-comment

# Get-HypervPortProxy emits the rule for a listen pair, or throws ObjectNotFound when there is none.
function Get-HypervPortProxy {
    [CmdletBinding()]
    param(
        [Parameter(Mandatory)] [string] $ListenAddress,
        [Parameter(Mandatory)] [int]    $ListenPort,
        [Parameter(Mandatory)] [string] $FirewallName
    )
    $rule = Get-HypervPortProxyRule -ListenAddress $ListenAddress -ListenPort $ListenPort
    if ($null -eq $rule) {
        throw (New-HypervPortProxyNotFoundError -ListenAddress $ListenAddress -ListenPort $ListenPort)
    }
    Write-HypervPortProxyResult -Rule $rule -FirewallName $FirewallName
}

# Entry block. Skipped during Pester runs (dot-source sets InvocationName='.').
if ($MyInvocation.InvocationName -ne '.') {
    try {
        $params = Read-HypervStdinPayload
        Get-HypervPortProxy `
            -ListenAddress $params.listen_address `
            -ListenPort $params.listen_port `
            -FirewallName $params.firewall_name
    }
    catch {
        Write-HypervError $_
        exit 1
    }
}
