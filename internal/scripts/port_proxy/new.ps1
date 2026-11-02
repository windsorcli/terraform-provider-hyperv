# port_proxy/new.ps1 -- create a netsh portproxy v4tov4 rule plus an optional
# inbound firewall allow rule.
#
# Wire contract (locked in by Tests.ps1):
#
#   stdin JSON  : {
#                   "listen_address":  "<IPv4>",          # required (default 0.0.0.0)
#                   "listen_port":     <int 1..65535>,    # required
#                   "connect_address": "<IPv4>",          # required
#                   "connect_port":    <int 1..65535>,    # required
#                   "firewall": {
#                     "enabled": <bool>,                  # required
#                     "name":    "<string>",              # required
#                     "profile": "<string>"               # required
#                   }
#                 }
#   stdout JSON : the created rule in the canonical eight-field read format
#                 (same fields as get.ps1).
#
# lint:allow-long-comment

# New-HypervPortProxy adds the rule only after the port passes the exclusion-range check, then confirms
# the IP Helper service binds it, because netsh accepts a rule it can't bind without reporting an error.
# Any failure after the add deletes the rule again, so a failed create leaves nothing behind.
function New-HypervPortProxy {
    [CmdletBinding()]
    param(
        [Parameter(Mandatory)] [string] $ListenAddress,
        [Parameter(Mandatory)] [int]    $ListenPort,
        [Parameter(Mandatory)] [string] $ConnectAddress,
        [Parameter(Mandatory)] [int]    $ConnectPort,
        [Parameter(Mandatory)] [bool]   $FirewallEnabled,
        [Parameter(Mandatory)] [string] $FirewallName,
        [Parameter(Mandatory)] [string] $FirewallProfile
    )

    $ownerProcessId = Get-HypervIpHelperProcessId

    if ($null -ne (Get-HypervPortProxyRule -ListenAddress $ListenAddress -ListenPort $ListenPort)) {
        throw "A portproxy v4tov4 rule for ${ListenAddress}:$ListenPort already exists. " +
            "Import it with 'terraform import' using the ID '${ListenAddress}:$ListenPort', or delete it with " +
            "'netsh interface portproxy delete v4tov4 listenaddress=$ListenAddress listenport=$ListenPort'."
    }

    $excluded = Find-HypervExcludedPortRange -Port $ListenPort
    if ($null -ne $excluded) {
        throw "listen_port $ListenPort is in the Windows TCP exclusion range $excluded, so the portproxy listener " +
            "can't bind it. Pick a port outside the ranges 'netsh interface ipv4 show excludedportrange protocol=tcp' lists."
    }

    Invoke-HypervNetsh -ArgumentList @(
        'interface', 'portproxy', 'add', 'v4tov4',
        "listenaddress=$ListenAddress", "listenport=$ListenPort",
        "connectaddress=$ConnectAddress", "connectport=$ConnectPort") | Out-Null

    $firewallCreated = $false
    try {
        Wait-HypervPortProxyListener -ListenAddress $ListenAddress -ListenPort $ListenPort -OwnerProcessId $ownerProcessId
        if ($FirewallEnabled) {
            New-HypervFirewallRule -Name $FirewallName -Protocol 'TCP' -LocalPort $ListenPort `
                -FirewallProfile $FirewallProfile -LocalAddress (Get-HypervFirewallLocalAddress -ListenAddress $ListenAddress)
            $firewallCreated = $true
        }
        $rule = Get-HypervPortProxyRule -ListenAddress $ListenAddress -ListenPort $ListenPort
        if ($null -eq $rule) {
            throw (New-HypervPortProxyNotFoundError -ListenAddress $ListenAddress -ListenPort $ListenPort)
        }
    }
    catch {
        $original = $_
        try {
            Invoke-HypervNetsh -ArgumentList @(
                'interface', 'portproxy', 'delete', 'v4tov4',
                "listenaddress=$ListenAddress", "listenport=$ListenPort") | Out-Null
            if ($firewallCreated) { Remove-NetFirewallRule -DisplayName $FirewallName -ErrorAction Stop }
        }
        catch { $null = $_ }
        throw $original
    }
    Write-HypervPortProxyResult -Rule $rule -FirewallName $FirewallName
}

# Entry block. Skipped during Pester runs (dot-source sets InvocationName='.').
if ($MyInvocation.InvocationName -ne '.') {
    try {
        $params = Read-HypervStdinPayload
        $fw = $params.firewall
        New-HypervPortProxy `
            -ListenAddress $params.listen_address `
            -ListenPort $params.listen_port `
            -ConnectAddress $params.connect_address `
            -ConnectPort $params.connect_port `
            -FirewallEnabled ([bool]$fw.enabled) `
            -FirewallName $fw.name `
            -FirewallProfile $fw.profile
    }
    catch {
        Write-HypervError $_
        exit 1
    }
}
