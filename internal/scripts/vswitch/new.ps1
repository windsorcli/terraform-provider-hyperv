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
#   stdout JSON : the created switch in the canonical nine-field read shape
#                 (six base + three NAT). NAT fields are empty strings for
#                 non-NAT switches.
#
# Validation strategy: trust the Go-side TF schema validators. Cmdlet errors
# (e.g. -SwitchType External requires -NetAdapterName) propagate through the
# PLAN.md S5 envelope on the catch.
#
# NAT branch -- Hyper-V has no "NAT" switch_type natively. A NAT switch is
# an Internal VMSwitch + a New-NetIPAddress on the host vNIC + a New-NetNat
# tying the prefix to that vNIC. The script orchestrates all three. Windows
# allows exactly one NetNat instance per host; the singleton check fires
# BEFORE creating the VMSwitch so a conflict doesn't leave a half-
# provisioned NAT-less Internal switch behind.

# New-HypervSwitch builds the parameter splat for New-VMSwitch from typed
# inputs, runs the cmdlet, and emits the canonical read shape. For NAT
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
    # AllowManagementOS lives on New-VMSwitch's NetAdapterName /
    # NetAdapterInterfaceDescription parameter sets (External-only). The
    # SwitchType parameter set used for Internal/Private does NOT accept
    # the flag -- forwarding it forces multi-set ambiguity and PowerShell
    # errors with "Parameter set cannot be resolved using the specified
    # named parameters." Internal switches always have a host vNIC
    # implicitly (that's what makes them Internal vs Private), so there's
    # nothing meaningful to set anyway. Gate the cmdlet param to External;
    # throw a clear contract error if the caller passed it for any other
    # type so the error attribute-anchors at the schema layer instead of
    # surfacing the cmdlet's opaque diagnostic.
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
# host vNIC + NetNat tying the prefix to that vNIC. Singleton-checks the
# host's NetNat before creating the switch so a conflict doesn't leave a
# half-provisioned Internal switch behind.
#
# Adoption: if a NetNat with the planned name already exists (idempotent
# re-apply or terraform import), New-NetNat is skipped and the existing
# instance is reused. A NetNat with a DIFFERENT name fails the singleton
# guard.
function New-HypervNatSwitch {
    [CmdletBinding()]
    param(
        [Parameter(Mandatory)] [string] $Name,
        [string] $Notes,
        [Parameter(Mandatory)] [string] $NatName,
        [Parameter(Mandatory)] [string] $NatInternalAddressPrefix,
        [Parameter(Mandatory)] [string] $NatHostAddress
    )

    # Singleton precondition: Windows allows exactly one NetNat per host. If
    # one already exists with a different name, we must abort BEFORE creating
    # the VMSwitch (otherwise we'd strand a NAT-less Internal switch on the
    # host that the operator would have to clean up by hand).
    $existingNat = Get-NetNat -ErrorAction SilentlyContinue | Select-Object -First 1
    $adoptNat = $false
    if ($null -ne $existingNat) {
        if ($existingNat.Name -ne $NatName) {
            throw "A NetNat instance named '$($existingNat.Name)' already exists on this host. " +
                "Windows allows exactly one NetNat per host -- remove the existing instance, " +
                "or set nat_name = '$($existingNat.Name)' to adopt it."
        }
        # Same name: adopt and skip New-NetNat below.
        $adoptNat = $true
    }

    # Derive PrefixLength from the CIDR. The Go-side schema validator
    # already shape-checked this string, so split-and-int is safe.
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

    New-NetIPAddress `
        -InterfaceAlias "vEthernet ($Name)" `
        -IPAddress $NatHostAddress `
        -PrefixLength $prefixLength `
        -AddressFamily 'IPv4' `
        -ErrorAction Stop | Out-Null

    if (-not $adoptNat) {
        New-NetNat `
            -Name $NatName `
            -InternalIPInterfaceAddressPrefix $NatInternalAddressPrefix `
            -ErrorAction Stop | Out-Null
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
        $params = [Console]::In.ReadToEnd() | ConvertFrom-Json

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
