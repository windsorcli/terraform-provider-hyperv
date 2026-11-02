# ics_network/_hcn.ps1 -- computenetwork.dll (HCN) bindings shared by the ics_network verb scripts.
# The Go side prepends this body to each verb; Pester dot-sources it.

# One C# line per array element, so the Go-side minifier can't alter the source.
$script:HypervHcnSource = @(
    'using System;',
    'using System.Runtime.InteropServices;',
    'public static class HypervHcn {',
    '  [DllImport("computenetwork.dll", CharSet = CharSet.Unicode)] public static extern int HcnEnumerateNetworks(string query, out IntPtr networks, out IntPtr errorRecord);',
    '  [DllImport("computenetwork.dll", CharSet = CharSet.Unicode)] public static extern int HcnCreateNetwork(ref Guid id, string settings, out IntPtr network, out IntPtr errorRecord);',
    '  [DllImport("computenetwork.dll", CharSet = CharSet.Unicode)] public static extern int HcnOpenNetwork(ref Guid id, out IntPtr network, out IntPtr errorRecord);',
    '  [DllImport("computenetwork.dll", CharSet = CharSet.Unicode)] public static extern int HcnQueryNetworkProperties(IntPtr network, string query, out IntPtr properties, out IntPtr errorRecord);',
    '  [DllImport("computenetwork.dll", CharSet = CharSet.Unicode)] public static extern int HcnDeleteNetwork(ref Guid id, out IntPtr errorRecord);',
    '  [DllImport("computenetwork.dll")] public static extern int HcnCloseNetwork(IntPtr network);',
    '  [DllImport("ole32.dll")] public static extern void CoTaskMemFree(IntPtr p);',
    '  public static string Take(IntPtr p) { if (p == IntPtr.Zero) { return null; } string s = Marshal.PtrToStringUni(p); CoTaskMemFree(p); return s; }',
    '}'
) -join "`n"

$script:HypervHcnQuery = '{"SchemaVersion":{"Major":2,"Minor":0}}'

# Initialize-HypervHcn compiles the HypervHcn P/Invoke class once per session.
function Initialize-HypervHcn {
    [CmdletBinding()]
    param()
    if (-not ('HypervHcn' -as [type])) {
        Add-Type -TypeDefinition $script:HypervHcnSource
    }
}

# Invoke-HypervHcnEnumerate returns the HRESULT, the JSON array of network IDs, and the HCN error record.
function Invoke-HypervHcnEnumerate {
    [CmdletBinding()]
    param()
    Initialize-HypervHcn
    $networks = [IntPtr]::Zero
    $errorRecord = [IntPtr]::Zero
    $hr = [HypervHcn]::HcnEnumerateNetworks($script:HypervHcnQuery, [ref]$networks, [ref]$errorRecord)
    [pscustomobject]@{ HResult = $hr; Result = [HypervHcn]::Take($networks); ErrorRecord = [HypervHcn]::Take($errorRecord) }
}

# Invoke-HypervHcnQuery opens a network, reads its properties JSON, and closes the handle.
function Invoke-HypervHcnQuery {
    [CmdletBinding()]
    param(
        [Parameter(Mandatory)] [guid] $Id
    )
    Initialize-HypervHcn
    $handle = [IntPtr]::Zero
    $properties = [IntPtr]::Zero
    $errorRecord = [IntPtr]::Zero
    $hr = [HypervHcn]::HcnOpenNetwork([ref]$Id, [ref]$handle, [ref]$errorRecord)
    if ($hr -ne 0) {
        return [pscustomobject]@{ HResult = $hr; Operation = 'open'; Result = $null; ErrorRecord = [HypervHcn]::Take($errorRecord) }
    }
    try {
        $hr = [HypervHcn]::HcnQueryNetworkProperties($handle, $script:HypervHcnQuery, [ref]$properties, [ref]$errorRecord)
    }
    finally {
        [void][HypervHcn]::HcnCloseNetwork($handle)
    }
    [pscustomobject]@{ HResult = $hr; Operation = 'query'; Result = [HypervHcn]::Take($properties); ErrorRecord = [HypervHcn]::Take($errorRecord) }
}

# Invoke-HypervHcnCreate creates a network from its settings JSON and closes the returned handle.
function Invoke-HypervHcnCreate {
    [CmdletBinding()]
    param(
        [Parameter(Mandatory)] [guid]   $Id,
        [Parameter(Mandatory)] [string] $Settings
    )
    Initialize-HypervHcn
    $handle = [IntPtr]::Zero
    $errorRecord = [IntPtr]::Zero
    $hr = [HypervHcn]::HcnCreateNetwork([ref]$Id, $Settings, [ref]$handle, [ref]$errorRecord)
    if ($handle -ne [IntPtr]::Zero) { [void][HypervHcn]::HcnCloseNetwork($handle) }
    [pscustomobject]@{ HResult = $hr; ErrorRecord = [HypervHcn]::Take($errorRecord) }
}

# Invoke-HypervHcnDelete deletes a network by ID.
function Invoke-HypervHcnDelete {
    [CmdletBinding()]
    param(
        [Parameter(Mandatory)] [guid] $Id
    )
    Initialize-HypervHcn
    $errorRecord = [IntPtr]::Zero
    $hr = [HypervHcn]::HcnDeleteNetwork([ref]$Id, [ref]$errorRecord)
    [pscustomobject]@{ HResult = $hr; ErrorRecord = [HypervHcn]::Take($errorRecord) }
}

# Format-HypervHResult renders an HRESULT as 0xXXXXXXXX, which avoids signed-int comparisons.
function Format-HypervHResult {
    [CmdletBinding()]
    param(
        [Parameter(Mandatory)] [int] $HResult
    )
    '0x{0:X8}' -f $HResult
}

# Test-HypervHcnNotFound reports whether an HRESULT means the network doesn't exist.
# Open returns 0x803B0001 for an unknown ID; delete returns 0x80070490.
function Test-HypervHcnNotFound {
    [CmdletBinding()]
    param(
        [Parameter(Mandatory)] [int] $HResult
    )
    (Format-HypervHResult -HResult $HResult) -in @('0x803B0001', '0x80070490')
}

# New-HypervHcnErrorRecord maps a failed HCN call to the ErrorRecord the Go side categorizes.
# A missing network is ObjectNotFound, and a missing hns service names the feature to enable.
function New-HypervHcnErrorRecord {
    [CmdletBinding()]
    param(
        [Parameter(Mandatory)] [int]    $HResult,
        [Parameter(Mandatory)] [string] $Operation,
        [Parameter(Mandatory)] [string] $Target,
        [AllowNull()] [AllowEmptyString()] [string] $ErrorRecord
    )
    $code = Format-HypervHResult -HResult $HResult
    $detail = ''
    if (-not [string]::IsNullOrWhiteSpace($ErrorRecord)) {
        $parsed = $ErrorRecord | ConvertFrom-Json
        if ($parsed.PSObject.Properties.Name -contains 'Error') { $detail = ([string]$parsed.Error).Trim() }
    }

    if ($code -eq '0x800706D9') {
        $message = "The Host Network Service (hns) is not available; HCN $Operation returned $code. " +
        "Enable it with 'Enable-WindowsOptionalFeature -Online -FeatureName Containers-HNS -All', then restart the host."
        $category = [System.Management.Automation.ErrorCategory]::NotInstalled
        $errorId = 'HcnServiceUnavailable'
    }
    elseif (Test-HypervHcnNotFound -HResult $HResult) {
        $message = "HCN network '$Target' was not found ($code)."
        $category = [System.Management.Automation.ErrorCategory]::ObjectNotFound
        $errorId = 'HcnNetworkNotFound'
    }
    else {
        $message = "HCN $Operation for '$Target' failed with $code"
        if ($detail -ne '') { $message += ": $detail" }
        $category = [System.Management.Automation.ErrorCategory]::InvalidOperation
        $errorId = 'HcnCallFailed'
    }
    [System.Management.Automation.ErrorRecord]::new(
        [System.InvalidOperationException]::new($message), $errorId, $category, $Target)
}

# Get-HypervJsonProperty returns a property of a parsed JSON object, or $null when it is absent.
function Get-HypervJsonProperty {
    [CmdletBinding()]
    param(
        [AllowNull()] $InputObject,
        [Parameter(Mandatory)] [string] $Name
    )
    if ($null -eq $InputObject) { return $null }
    $property = $InputObject.PSObject.Properties[$Name]
    if ($null -eq $property) { return $null }
    $property.Value
}

# ConvertTo-HypervIcsNetwork turns HCN network properties into the canonical six-field read result.
# The switch ID comes from the host vNIC allocator, the isolation ID from the subnet's VLAN policy.
function ConvertTo-HypervIcsNetwork {
    [CmdletBinding()]
    param(
        [Parameter(Mandatory)] [string] $Properties
    )
    $network = $Properties | ConvertFrom-Json
    $type = Get-HypervJsonProperty $network 'Type'
    if ($type -ne 'ICS') {
        throw "HCN network '$($network.ID)' has type '$type', not ICS."
    }

    $subnet = @(Get-HypervJsonProperty (@(Get-HypervJsonProperty $network 'Ipams') | Select-Object -First 1) 'Subnets') |
        Select-Object -First 1
    $route = @(Get-HypervJsonProperty $subnet 'Routes') |
        Where-Object { (Get-HypervJsonProperty $_ 'DestinationPrefix') -eq '0.0.0.0/0' } | Select-Object -First 1
    $vlan = @(Get-HypervJsonProperty $subnet 'Policies') |
        Where-Object { (Get-HypervJsonProperty $_ 'Type') -eq 'VLAN' } | Select-Object -First 1

    $resources = Get-HypervJsonProperty (Get-HypervJsonProperty (Get-HypervJsonProperty $network 'Health') 'Extra') 'Resources'
    $switchId = @(Get-HypervJsonProperty $resources 'Allocators') |
        ForEach-Object { Get-HypervJsonProperty $_ 'SwitchId' } | Where-Object { $_ } | Select-Object -First 1
    $switchName = ''
    if ($switchId) {
        $switch = Get-VMSwitch -Id $switchId -ErrorAction SilentlyContinue | Select-Object -First 1
        if ($null -ne $switch) { $switchName = $switch.Name }
    }

    $isolationId = 0
    $vlanSettings = Get-HypervJsonProperty $vlan 'Settings'
    if ($null -ne (Get-HypervJsonProperty $vlanSettings 'IsolationId')) { $isolationId = [int]$vlanSettings.IsolationId }

    [pscustomobject]@{
        Id            = ([string]$network.ID).ToLowerInvariant()
        Name          = [string](Get-HypervJsonProperty $network 'Name')
        AddressPrefix = [string](Get-HypervJsonProperty $subnet 'IpAddressPrefix')
        HostAddress   = [string](Get-HypervJsonProperty $route 'NextHop')
        IsolationId   = $isolationId
        SwitchName    = [string]$switchName
    }
}

# Read-HypervIcsNetwork queries one network by ID and returns its read result, throwing the mapped HCN error.
function Read-HypervIcsNetwork {
    [CmdletBinding()]
    param(
        [Parameter(Mandatory)] [guid] $Id
    )
    $call = Invoke-HypervHcnQuery -Id $Id
    if ($call.HResult -ne 0) {
        throw (New-HypervHcnErrorRecord -HResult $call.HResult -Operation $call.Operation `
                -Target $Id.ToString() -ErrorRecord $call.ErrorRecord)
    }
    ConvertTo-HypervIcsNetwork -Properties $call.Result
}
