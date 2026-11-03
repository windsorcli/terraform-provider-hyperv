# port_proxy/set.ps1 -- retarget a netsh portproxy v4tov4 rule and reconcile
# its firewall rule.
#
# Wire contract (locked in by Tests.ps1):
#
#   stdin JSON  : same fields as new.ps1. listen_address and listen_port
#                 identify the existing rule; connect_address,
#                 connect_port, and firewall.{enabled, profile} are applied.
#   stdout JSON : the updated rule in the canonical nine-field read format.
#   stderr/exit : missing rule -> ObjectNotFound envelope, exit 1.
#
# lint:allow-long-comment

# Set-HypervPortProxy points an existing rule at a new connect address and port, then creates, updates,
# or disables the firewall rule so it matches the requested state. A rule with no listener is deleted and
# added again, since netsh set has been observed to leave it unbound; that touches no other rule.
function Set-HypervPortProxy {
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

    $existing = Get-HypervPortProxyRule -ListenAddress $ListenAddress -ListenPort $ListenPort
    if ($null -eq $existing) {
        throw (New-HypervPortProxyNotFoundError -ListenAddress $ListenAddress -ListenPort $ListenPort)
    }

    if (-not (Test-HypervPortProxyListener -ListenAddress $ListenAddress -ListenPort $ListenPort)) {
        Invoke-HypervNetsh -ArgumentList @(
            'interface', 'portproxy', 'delete', 'v4tov4',
            "listenaddress=$ListenAddress", "listenport=$ListenPort") | Out-Null
        Invoke-HypervNetsh -ArgumentList @(
            'interface', 'portproxy', 'add', 'v4tov4',
            "listenaddress=$ListenAddress", "listenport=$ListenPort",
            "connectaddress=$ConnectAddress", "connectport=$ConnectPort") | Out-Null
        Wait-HypervPortProxyListener -ListenAddress $ListenAddress -ListenPort $ListenPort -OwnerProcessId $ownerProcessId
    }
    elseif ($existing.ConnectAddress -ne $ConnectAddress -or $existing.ConnectPort -ne $ConnectPort) {
        Invoke-HypervNetsh -ArgumentList @(
            'interface', 'portproxy', 'set', 'v4tov4',
            "listenaddress=$ListenAddress", "listenport=$ListenPort",
            "connectaddress=$ConnectAddress", "connectport=$ConnectPort") | Out-Null
    }

    Sync-HypervFirewallRule -Enabled $FirewallEnabled -Name $FirewallName -Protocol 'TCP' -LocalPort $ListenPort `
        -FirewallProfile $FirewallProfile -LocalAddress (Get-HypervFirewallLocalAddress -ListenAddress $ListenAddress)

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
        $fw = $params.firewall
        Set-HypervPortProxy `
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
