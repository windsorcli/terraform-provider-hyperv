# _test_helpers.ps1 -- shared Pester setup for the port_proxy verb scripts.
# Stubs the Windows-only cmdlets so the suites run on Linux pwsh as well.

function Get-CimInstance {
    [CmdletBinding()]
    param(
        [string] $ClassName,
        [string] $Filter
    )
}

function Get-NetTCPConnection {
    [CmdletBinding()]
    param(
        [string] $State,
        [int]    $LocalPort
    )
}

function Get-NetFirewallRule {
    [CmdletBinding()]
    param(
        [string] $DisplayName
    )
}

function New-NetFirewallRule {
    [CmdletBinding()]
    param(
        [string] $DisplayName,
        [string] $Direction,
        [string] $Action,
        [string] $Protocol,
        [string] $LocalAddress,
        [int]    $LocalPort,
        [string] $Profile
    )
}

function Set-NetFirewallRule {
    [CmdletBinding()]
    param(
        [string] $DisplayName,
        [string] $Enabled,
        [string] $Profile,
        [string] $LocalAddress
    )
}

function Remove-NetFirewallRule {
    [CmdletBinding()]
    param(
        [string] $DisplayName
    )
}

# New-HypervPortProxyRegistryItem models Get-ItemProperty's result for one PortProxy value.
function New-HypervPortProxyRegistryItem {
    [CmdletBinding()]
    param(
        [string] $ListenAddress = '0.0.0.0',
        [int]    $ListenPort = 15000,
        [string] $ConnectAddress = '172.30.98.222',
        [int]    $ConnectPort = 50000
    )
    $item = [pscustomobject]@{}
    $item | Add-Member -NotePropertyName "$ListenAddress/$ListenPort" -NotePropertyValue "$ConnectAddress/$ConnectPort"
    $item
}

# New-HypervIpHelperService models Get-CimInstance Win32_Service for iphlpsvc.
function New-HypervIpHelperService {
    [CmdletBinding()]
    param(
        [string] $State = 'Running',
        [int]    $ProcessId = 2644
    )
    [pscustomobject]@{ Name = 'iphlpsvc'; State = $State; ProcessId = $ProcessId }
}

# New-HypervListenerSample models one Get-NetTCPConnection -State Listen row.
function New-HypervListenerSample {
    [CmdletBinding()]
    param(
        [string] $LocalAddress = '0.0.0.0',
        [int]    $LocalPort = 15000,
        [int]    $OwningProcess = 2644
    )
    [pscustomobject]@{ LocalAddress = $LocalAddress; LocalPort = $LocalPort; OwningProcess = $OwningProcess }
}

# New-HypervFirewallRuleSample models the Get-NetFirewallRule fields the read result uses.
function New-HypervFirewallRuleSample {
    [CmdletBinding()]
    param(
        [string] $DisplayName = 'hyperv-pp-0.0.0.0-15000',
        [string] $Profile = 'Any',
        [string] $Enabled = 'True'
    )
    [pscustomobject]@{ DisplayName = $DisplayName; Profile = $Profile; Enabled = $Enabled }
}

# Excluded-range output as netsh printed it on the bench after Containers-HNS was enabled.
$script:HypervExcludedRangeText = @(
    '',
    'Protocol tcp Port Exclusion Ranges',
    '',
    'Start Port    End Port',
    '----------    --------',
    '      5357        5357',
    '     50074       50173',
    '',
    '* - Administered port exclusions.'
)
