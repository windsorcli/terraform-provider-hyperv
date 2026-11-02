# port_proxy/remove.ps1 -- delete a netsh portproxy v4tov4 rule and its
# firewall rule, if present.
#
# Wire contract (locked in by Tests.ps1):
#
#   stdin JSON  : {
#                   "listen_address": "<IPv4>",
#                   "listen_port":    <int>,
#                   "firewall_name":  "<string>"
#                 }
#   stdout      : empty (caller passes dst=nil to runScript).
#
# A missing rule or firewall rule is success: nothing by those identifiers remains.
#
# lint:allow-long-comment

# Remove-HypervPortProxy deletes the rule and its firewall rule, skipping whichever is already gone.
function Remove-HypervPortProxy {
    [CmdletBinding()]
    param(
        [Parameter(Mandatory)] [string] $ListenAddress,
        [Parameter(Mandatory)] [int]    $ListenPort,
        [Parameter(Mandatory)] [string] $FirewallName
    )

    if ($null -ne (Get-HypervPortProxyRule -ListenAddress $ListenAddress -ListenPort $ListenPort)) {
        Invoke-HypervNetsh -ArgumentList @(
            'interface', 'portproxy', 'delete', 'v4tov4',
            "listenaddress=$ListenAddress", "listenport=$ListenPort") | Out-Null
    }

    $existingFw = Get-NetFirewallRule -DisplayName $FirewallName -ErrorAction SilentlyContinue | Select-Object -First 1
    if ($null -ne $existingFw) {
        Remove-NetFirewallRule -DisplayName $FirewallName -ErrorAction Stop
    }
}

# Entry block. Skipped during Pester runs (dot-source sets InvocationName='.').
if ($MyInvocation.InvocationName -ne '.') {
    try {
        $params = Read-HypervStdinPayload
        Remove-HypervPortProxy `
            -ListenAddress $params.listen_address `
            -ListenPort $params.listen_port `
            -FirewallName $params.firewall_name
    }
    catch {
        Write-HypervError $_
        exit 1
    }
}
