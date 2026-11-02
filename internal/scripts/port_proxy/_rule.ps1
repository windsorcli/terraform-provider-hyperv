# port_proxy/_rule.ps1 -- helpers shared by the port_proxy verb scripts.
# The Go side prepends this body to each verb; Pester dot-sources it.

# Invoke-HypervNetsh runs netsh with the given arguments and throws its output when it exits nonzero.
function Invoke-HypervNetsh {
    [CmdletBinding()]
    param(
        [Parameter(Mandatory)] [string[]] $ArgumentList
    )
    $output = & netsh @ArgumentList 2>&1 | ForEach-Object { $_.ToString() }
    if ($LASTEXITCODE -ne 0) {
        throw "netsh $($ArgumentList -join ' ') failed (exit $LASTEXITCODE): $(($output -join ' ').Trim())"
    }
    $output
}

# Get-HypervPortProxyRule reads one v4tov4 TCP rule from the registry, where netsh stores
# each rule as a value named '<listen_address>/<listen_port>' holding '<connect_address>/<connect_port>'.
# Returns $null when no rule exists for the listen pair.
function Get-HypervPortProxyRule {
    [CmdletBinding()]
    param(
        [Parameter(Mandatory)] [string] $ListenAddress,
        [Parameter(Mandatory)] [int]    $ListenPort
    )
    $valueName = "$ListenAddress/$ListenPort"
    $item = Get-ItemProperty -Path 'HKLM:\SYSTEM\CurrentControlSet\Services\PortProxy\v4tov4\tcp' `
        -Name $valueName -ErrorAction SilentlyContinue
    if ($null -eq $item) { return $null }
    $property = $item.PSObject.Properties[$valueName]
    if ($null -eq $property) { return $null }

    $connectAddress, $connectPort = ([string]$property.Value).Split('/', 2)
    [pscustomobject]@{
        ListenAddress  = $ListenAddress
        ListenPort     = $ListenPort
        ConnectAddress = $connectAddress
        ConnectPort    = [int]$connectPort
    }
}

# New-HypervPortProxyNotFoundError builds the ObjectNotFound record the Go side maps to ErrNotFound.
function New-HypervPortProxyNotFoundError {
    [CmdletBinding()]
    param(
        [Parameter(Mandatory)] [string] $ListenAddress,
        [Parameter(Mandatory)] [int]    $ListenPort
    )
    $exception = [System.Management.Automation.ItemNotFoundException]::new(
        "No portproxy v4tov4 rule found for listen_address='$ListenAddress', listen_port='$ListenPort'.")
    [System.Management.Automation.ErrorRecord]::new(
        $exception, 'PortProxyNotFound',
        [System.Management.Automation.ErrorCategory]::ObjectNotFound,
        "$ListenAddress/$ListenPort")
}

# Find-HypervExcludedPortRange returns the 'start-end' TCP exclusion range holding $Port, or $null.
function Find-HypervExcludedPortRange {
    [CmdletBinding()]
    param(
        [Parameter(Mandatory)] [int] $Port
    )
    $lines = Invoke-HypervNetsh -ArgumentList @('interface', 'ipv4', 'show', 'excludedportrange', 'protocol=tcp')
    foreach ($line in $lines) {
        $parts = @($line -split '\s+' | Where-Object { $_ -ne '' })
        if ($parts.Count -lt 2) { continue }
        $start = 0
        $end = 0
        if (-not [int]::TryParse($parts[0], [ref]$start)) { continue }
        if (-not [int]::TryParse($parts[1], [ref]$end)) { continue }
        if ($Port -ge $start -and $Port -le $end) { return "$start-$end" }
    }
    $null
}

# Get-HypervIpHelperProcessId returns the PID of the IP Helper service, which owns every portproxy listener.
function Get-HypervIpHelperProcessId {
    [CmdletBinding()]
    param()
    $service = Get-CimInstance -ClassName Win32_Service -Filter "Name='iphlpsvc'" -ErrorAction Stop
    if ($null -eq $service -or $service.State -ne 'Running') {
        throw "The IP Helper service (iphlpsvc) is not running, so portproxy rules can't bind. Start it with 'Start-Service iphlpsvc'."
    }
    [int]$service.ProcessId
}

# Wait-HypervPortProxyListener polls until the IP Helper service listens on the rule's address and port.
# Throws when it doesn't within the timeout, naming any other process that holds the port.
function Wait-HypervPortProxyListener {
    [CmdletBinding()]
    param(
        [Parameter(Mandatory)] [string] $ListenAddress,
        [Parameter(Mandatory)] [int]    $ListenPort,
        [Parameter(Mandatory)] [int]    $OwnerProcessId,
        [int] $Attempts = 10,
        [int] $DelayMilliseconds = 200
    )
    for ($i = 0; $i -lt $Attempts; $i++) {
        $listeners = @(Get-NetTCPConnection -State Listen -LocalPort $ListenPort -ErrorAction SilentlyContinue |
                Where-Object { $_.LocalAddress -eq $ListenAddress })
        if (@($listeners | Where-Object { $_.OwningProcess -eq $OwnerProcessId }).Count -gt 0) { return }
        if ($i -lt $Attempts - 1) { Start-Sleep -Milliseconds $DelayMilliseconds }
    }
    $others = @($listeners | Where-Object { $_.OwningProcess -ne $OwnerProcessId } | ForEach-Object { $_.OwningProcess })
    $holder = if ($others.Count -gt 0) { " Process $($others[0]) already listens on that port." } else { '' }
    throw "netsh accepted the portproxy rule for ${ListenAddress}:$ListenPort, but the IP Helper service isn't listening on it.$holder"
}

# Get-HypervPortProxyFirewallState reports the firewall rule as present only when it exists and is
# enabled, since set.ps1 disables the rule rather than deleting it when firewall_rule.enabled is false.
function Get-HypervPortProxyFirewallState {
    [CmdletBinding()]
    param(
        [Parameter(Mandatory)] [string] $FirewallName
    )
    $rule = Get-NetFirewallRule -DisplayName $FirewallName -ErrorAction SilentlyContinue | Select-Object -First 1
    [pscustomobject]@{
        Present = $null -ne $rule -and $rule.Enabled.ToString() -eq 'True'
        Profile = if ($null -ne $rule) { $rule.Profile.ToString() } else { '' }
    }
}

# Write-HypervPortProxyResult emits the canonical eight-field read result for a rule and its firewall rule.
function Write-HypervPortProxyResult {
    [CmdletBinding()]
    param(
        [Parameter(Mandatory)] $Rule,
        [Parameter(Mandatory)] [string] $FirewallName
    )
    $firewall = Get-HypervPortProxyFirewallState -FirewallName $FirewallName
    [pscustomobject]@{
        Id                  = "$($Rule.ListenAddress):$($Rule.ListenPort)"
        ListenAddress       = $Rule.ListenAddress
        ListenPort          = [int]$Rule.ListenPort
        ConnectAddress      = $Rule.ConnectAddress
        ConnectPort         = [int]$Rule.ConnectPort
        FirewallRulePresent = [bool]$firewall.Present
        FirewallRuleName    = $FirewallName
        FirewallRuleProfile = $firewall.Profile
    } | Write-HypervResult
}
