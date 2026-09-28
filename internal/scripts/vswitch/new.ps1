# vswitch/new.ps1 -- create a new virtual switch.
#
# Wire contract (locked in by Tests.ps1):
#
#   stdin JSON  : {
#                   "name":                        "<string>",                              # required
#                   "switch_type":                 "External"|"Internal"|"Private"|"NAT",   # required
#                   "net_adapter_names":           ["<string>", ...],                       # External only
#                   "allow_management_os":         <bool>,                                  # External only
#                   "notes":                       "<string>",                              # optional
#                   "nat_name":                    "<string>",                              # NAT only, required when NAT
#                   "nat_internal_address_prefix": "<CIDR>",                                # NAT only, required when NAT
#                   "nat_host_address":            "<IPv4>"                                 # NAT only, required when NAT
#                 }
#   stdout JSON : the created switch in the canonical nine-field format
#                 (six base + three NAT). NAT fields are empty strings for
#                 non-NAT switches.
#
# Validation strategy: trust the Go-side TF schema validators. Cmdlet errors
# (e.g. -SwitchType External requires -NetAdapterName) propagate through the
# structured error envelope on the catch.
#
# NAT branch: Hyper-V has no native "NAT" switch_type, so a NAT switch is
# an Internal VMSwitch plus a New-NetIPAddress on the host vNIC plus a
# New-NetNat tying the prefix to that vNIC. A NetNat with the configured
# name is idempotently adopted when present (re-apply/import safety).
#
# lint:allow-long-comment

# New-HypervSwitch builds the parameter splat for New-VMSwitch from typed
# inputs, runs the cmdlet, and emits the canonical read format. For NAT
# switches it additionally provisions NetIPAddress + NetNat.
function New-HypervSwitch {
    [CmdletBinding()]
    param(
        [Parameter(Mandatory)] [string] $Name,
        [Parameter(Mandatory)] [ValidateSet('External', 'Internal', 'Private', 'NAT')] [string] $SwitchType,
        [string[]]       $NetAdapterNames,
        [Nullable[bool]] $AllowManagementOS,
        [string]         $Notes,
        [string]         $NatName,
        [string]         $NatInternalAddressPrefix,
        [string]         $NatHostAddress
    )

    if ($SwitchType -eq 'NAT') {
        return New-HypervNatSwitch -Name $Name -Notes:($PSBoundParameters['Notes']) `
            -NatName $NatName `
            -NatInternalAddressPrefix $NatInternalAddressPrefix `
            -NatHostAddress $NatHostAddress
    }

    $newArgs = @{
        Name        = $Name
        ErrorAction = 'Stop'
    }
    if ($SwitchType -eq 'External') {
        $newArgs.NetAdapterName = $NetAdapterNames
    }
    else {
        $newArgs.SwitchType = $SwitchType
    }
    # AllowManagementOS is External-only: forwarding it on Internal/Private causes New-VMSwitch parameter-set ambiguity.
    if ($null -ne $AllowManagementOS -and $SwitchType -eq 'External') {
        $newArgs.AllowManagementOS = [bool]$AllowManagementOS
    }
    elseif ($null -ne $AllowManagementOS) {
        throw "allow_management_os is not valid for switch_type '$SwitchType' (External only)"
    }
    if ($PSBoundParameters.ContainsKey('Notes')) {
        $newArgs.Notes = $Notes
    }

    New-VMSwitch @newArgs |
        Select-Object `
            Name,
            @{ N = 'SwitchType';                      E = { $_.SwitchType.ToString() } },
            AllowManagementOS,
            NetAdapterInterfaceDescription,
            Notes,
            @{ N = 'Id';                              E = { $_.Id.ToString() } },
            @{ N = 'NatName';                         E = { '' } },
            @{ N = 'NatInternalAddressPrefix';        E = { '' } },
            @{ N = 'NatHostAddress';                  E = { '' } } |
        Write-HypervResult
}

# New-HypervNatSwitch provisions an Internal VMSwitch + NetIPAddress on the
# host vNIC + NetNat tying the prefix to that vNIC.
#
# Idempotent adoption: if a NetNat with the planned name already exists
# (re-apply or terraform import), New-NetNat is skipped and the existing
# instance is reused. Prefix mismatch on the same-named NetNat throws --
# RequiresReplace on the prefix attribute would otherwise loop, since
# adoption locks state to the host's actual prefix.
function New-HypervNatSwitch {
    [CmdletBinding()]
    param(
        [Parameter(Mandatory)] [string] $Name,
        [string] $Notes,
        [Parameter(Mandatory)] [string] $NatName,
        [Parameter(Mandatory)] [string] $NatInternalAddressPrefix,
        [Parameter(Mandatory)] [string] $NatHostAddress
    )

    $existingNat = Get-NetNat -Name $NatName -ErrorAction SilentlyContinue
    $adoptNat = $false
    if ($null -ne $existingNat) {
        # Adopt only if the prefix matches; a mismatch would loop through RequiresReplace on every apply.
        if ($existingNat.InternalIPInterfaceAddressPrefix -ne $NatInternalAddressPrefix) {
            throw "A NetNat named '$NatName' already exists with prefix " +
                "'$($existingNat.InternalIPInterfaceAddressPrefix)', but the plan asks for " +
                "'$NatInternalAddressPrefix'. Set nat_internal_address_prefix to " +
                "'$($existingNat.InternalIPInterfaceAddressPrefix)' to adopt the existing " +
                "instance, or remove the existing NetNat to let this resource create a fresh one."
        }
        $adoptNat = $true
    }

    # Belt-and-braces CIDR check for direct script invocations that bypass the Go-side schema validator.
    if ($NatInternalAddressPrefix -notmatch '^(\d{1,3}\.){3}\d{1,3}/\d+$') {
        throw "nat_internal_address_prefix '$NatInternalAddressPrefix' is not a valid CIDR (expected 'A.B.C.D/N')."
    }
    $prefixLength = [int]($NatInternalAddressPrefix.Split('/')[1])

    $vmsArgs = @{
        Name        = $Name
        SwitchType  = 'Internal'
        ErrorAction = 'Stop'
    }
    if ($PSBoundParameters.ContainsKey('Notes')) {
        $vmsArgs.Notes = $Notes
    }
    $sw = New-VMSwitch @vmsArgs

    # A failure after New-VMSwitch would otherwise leave an orphan switch blocking every future apply on "already exists".
    $ipCreated = $false
    $natCreated = $false
    try {
        New-NetIPAddress `
            -InterfaceAlias "vEthernet ($Name)" `
            -IPAddress $NatHostAddress `
            -PrefixLength $prefixLength `
            -AddressFamily 'IPv4' `
            -ErrorAction Stop | Out-Null
        $ipCreated = $true

        if (-not $adoptNat) {
            New-NetNat `
                -Name $NatName `
                -InternalIPInterfaceAddressPrefix $NatInternalAddressPrefix `
                -ErrorAction Stop | Out-Null
            $natCreated = $true
        }
    }
    catch {
        # Capture the original failure before cleanup, which mirrors remove.ps1's teardown order and must not overwrite it.
        $original = $_
        if ($natCreated) {
            try { Remove-NetNat -Name $NatName -Confirm:$false -ErrorAction Stop }
            catch { $null = $_ }
        }
        if ($ipCreated) {
            try {
                Remove-NetIPAddress `
                    -InterfaceAlias "vEthernet ($Name)" `
                    -IPAddress $NatHostAddress `
                    -Confirm:$false `
                    -ErrorAction Stop
            }
            catch { $null = $_ }
        }
        try { Remove-VMSwitch -Name $Name -Force -ErrorAction Stop }
        catch { $null = $_ }
        throw $original
    }

    $sw |
        Select-Object `
            Name,
            @{ N = 'SwitchType';                      E = { 'NAT' } },
            AllowManagementOS,
            NetAdapterInterfaceDescription,
            Notes,
            @{ N = 'Id';                              E = { $_.Id.ToString() } },
            @{ N = 'NatName';                         E = { $NatName } },
            @{ N = 'NatInternalAddressPrefix';        E = { $NatInternalAddressPrefix } },
            @{ N = 'NatHostAddress';                  E = { $NatHostAddress } } |
        Write-HypervResult
}

# Entry block. Skipped during Pester runs (dot-source sets InvocationName='.').
if ($MyInvocation.InvocationName -ne '.') {
    try {
        $params = Read-HypervStdinPayload

        $callArgs = @{
            Name       = $params.name
            SwitchType = $params.switch_type
        }
        if ($params.PSObject.Properties.Name -contains 'net_adapter_names' -and $null -ne $params.net_adapter_names) {
            $callArgs.NetAdapterNames = @($params.net_adapter_names)
        }
        if ($params.PSObject.Properties.Name -contains 'allow_management_os' -and $null -ne $params.allow_management_os) {
            $callArgs.AllowManagementOS = [bool]$params.allow_management_os
        }
        if ($params.PSObject.Properties.Name -contains 'notes' -and $null -ne $params.notes) {
            $callArgs.Notes = $params.notes
        }
        if ($params.PSObject.Properties.Name -contains 'nat_name' -and $null -ne $params.nat_name) {
            $callArgs.NatName = $params.nat_name
        }
        if ($params.PSObject.Properties.Name -contains 'nat_internal_address_prefix' -and $null -ne $params.nat_internal_address_prefix) {
            $callArgs.NatInternalAddressPrefix = $params.nat_internal_address_prefix
        }
        if ($params.PSObject.Properties.Name -contains 'nat_host_address' -and $null -ne $params.nat_host_address) {
            $callArgs.NatHostAddress = $params.nat_host_address
        }

        New-HypervSwitch @callArgs
    }
    catch {
        Write-HypervError $_
        exit 1
    }
}
