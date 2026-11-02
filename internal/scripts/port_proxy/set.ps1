# port_proxy/set.ps1 -- retarget a netsh portproxy v4tov4 rule and reconcile
# its firewall rule.
#
# Wire contract (locked in by Tests.ps1):
#
#   stdin JSON  : same fields as new.ps1. listen_address and listen_port
#                 identify the existing rule; connect_address,
#                 connect_port, and firewall.{enabled, profile} are applied.
#   stdout JSON : the updated rule in the canonical eight-field read format.
#   stderr/exit : missing rule -> ObjectNotFound envelope, exit 1.
#
# lint:allow-long-comment

# Set-HypervPortProxy points an existing rule at a new connect address and port, then creates, updates,
# or disables the firewall rule so it matches the requested state.
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

    $existing = Get-HypervPortProxyRule -ListenAddress $ListenAddress -ListenPort $ListenPort
    if ($null -eq $existing) {
        throw (New-HypervPortProxyNotFoundError -ListenAddress $ListenAddress -ListenPort $ListenPort)
    }

    if ($existing.ConnectAddress -ne $ConnectAddress -or $existing.ConnectPort -ne $ConnectPort) {
        Invoke-HypervNetsh -ArgumentList @(
            'interface', 'portproxy', 'set', 'v4tov4',
            "listenaddress=$ListenAddress", "listenport=$ListenPort",
            "connectaddress=$ConnectAddress", "connectport=$ConnectPort") | Out-Null
    }

    $existingFw = Get-NetFirewallRule -DisplayName $FirewallName -ErrorAction SilentlyContinue | Select-Object -First 1
    if ($null -ne $existingFw) {
        # -Enabled binds to the NetSecurity.Enabled enum, whose members are the strings 'True' and 'False'.
        $enabledValue = if ($FirewallEnabled) { 'True' } else { 'False' }
        Set-NetFirewallRule -DisplayName $FirewallName -Enabled $enabledValue -Profile $FirewallProfile `
            -LocalAddress (Get-HypervFirewallLocalAddress -ListenAddress $ListenAddress) -ErrorAction Stop
    }
    elseif ($FirewallEnabled) {
        New-HypervPortProxyFirewallRule -FirewallName $FirewallName -ListenAddress $ListenAddress `
            -ListenPort $ListenPort -FirewallProfile $FirewallProfile
    }

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
