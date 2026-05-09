# vswitch/set.ps1 -- update mutable attributes of an existing virtual switch.
#
# Wire contract (locked in by Tests.ps1):
#
#   stdin JSON  : {
#                   "name":                "<string>",                       # required (target switch)
#                   "switch_type":         "External"|"Internal"|"Private",  # optional, validation hint only
#                   "net_adapter_names":   ["<string>", ...],                # External only, optional
#                   "allow_management_os": <bool>,                            # optional
#                   "notes":               "<string>"                         # optional
#                 }
#   stdout JSON : the updated switch in the canonical read shape (same fields
#                 as get.ps1 -- emitted by re-reading after the mutation lands).
#
# Only keys present in the input are touched. switch_type is immutable
# (RequiresReplace plan modifier on the Go side) and is NOT forwarded to
# Set-VMSwitch -- when present in the payload it's used purely to mirror
# new.ps1's Private + AllowManagementOS reject path. The Go-side Update
# should populate it from prior state so the validation kicks in.

# Set-HypervSwitch applies a partial update via Set-VMSwitch, then re-reads
# via Get-VMSwitch so the emitted shape matches Read exactly. Two-step instead
# of -PassThru because Set-VMSwitch's -PassThru behavior across NIC rebinding
# is uneven across PS 5.1 / 7.x.
function Set-HypervSwitch {
    [CmdletBinding()]
    param(
        [Parameter(Mandatory)] [string] $Name,
        [ValidateSet('External', 'Internal', 'Private', 'NAT')] [string] $SwitchType,
        [string[]]       $NetAdapterNames,
        [Nullable[bool]] $AllowManagementOS,
        [string]         $Notes,
        [string]         $NatName,
        [string]         $NatInternalAddressPrefix
    )

    # Existence pre-check. Symmetric with get.ps1 / remove.ps1: Set-VMSwitch
    # on a missing switch raises an InvalidArgument error which the Go side
    # would map to ErrPSExecution -- losing the ErrNotFound semantics that
    # let Update recover gracefully from out-of-band deletion.
    #
    # Stop + selective catch instead of SilentlyContinue: a transient WMI
    # fault, permission error, or cluster-connectivity blip would otherwise
    # be indistinguishable from "switch missing", get remapped to ObjectNotFound,
    # and let the Go-side Update drop the resource from state -- after which
    # the next apply calls New-VMSwitch and fails on a name conflict, forcing
    # a manual import or taint to recover.
    try {
        $existing = Get-VMSwitch -Name $Name -ErrorAction Stop
    }
    catch {
        if ($_.CategoryInfo.Category -ne [System.Management.Automation.ErrorCategory]::ObjectNotFound) {
            throw
        }
        $existing = $null
    }
    if ($null -eq $existing) {
        $exception = [System.Management.Automation.ItemNotFoundException]::new(
            "Hyper-V was unable to find a virtual switch with name '$Name'.")
        $errorRecord = [System.Management.Automation.ErrorRecord]::new(
            $exception, 'VMSwitchNotFound',
            [System.Management.Automation.ErrorCategory]::ObjectNotFound, $Name)
        throw $errorRecord
    }

    # Symmetric with new.ps1: AllowManagementOS is meaningful only for
    # External switches. We read the host-side truth from $existing
    # (populated by Get-VMSwitch above) rather than the caller-supplied
    # $SwitchType so the guard fires unconditionally. If we trusted the
    # caller hint, an Update payload that omits switch_type would
    # short-circuit the check and pass -AllowManagementOS=$false through
    # to Set-VMSwitch on a real Internal switch -- which silently
    # converts it to Private (a type mutation with no error, surfacing
    # only as state drift on the next refresh). $existing.SwitchType is
    # an enum; ToString() matches the convention get.ps1 uses for the
    # canonical projection.
    if ($null -ne $AllowManagementOS -and $existing.SwitchType.ToString() -ne 'External') {
        throw "allow_management_os is not valid for switch_type '$($existing.SwitchType)' (External only)"
    }

    # NAT branch. The mutable-in-place set is narrow:
    #   - Notes lives on the underlying VMSwitch -> Set-VMSwitch.
    #   - nat_internal_address_prefix lives on NetNat -> Set-NetNat.
    # Everything else (nat_name, nat_host_address, switch_type) is
    # RequiresReplace at the schema layer; it never reaches Update.
    if ($SwitchType -eq 'NAT') {
        $touchedSwitch = $false
        if ($PSBoundParameters.ContainsKey('Notes')) {
            Set-VMSwitch -Name $Name -Notes $Notes -ErrorAction Stop
            $touchedSwitch = $true
        }
        $touchedNat = $false
        if ($PSBoundParameters.ContainsKey('NatInternalAddressPrefix')) {
            Set-NetNat -Name $NatName `
                -InternalIPInterfaceAddressPrefix $NatInternalAddressPrefix `
                -ErrorAction Stop | Out-Null
            $touchedNat = $true
        }
        if (-not $touchedSwitch -and -not $touchedNat) {
            throw "Set-HypervSwitch requires at least one mutable attribute (notes or nat_internal_address_prefix)"
        }

        # Read-back. Mirrors get.ps1's NAT augmentation: pull the underlying
        # VMSwitch, the NetIPAddress, the NetNat, then synthesize the
        # SwitchType=NAT shape.
        $sw = Get-VMSwitch -Name $Name -ErrorAction Stop
        $natIp = Get-NetIPAddress `
            -InterfaceAlias "vEthernet ($Name)" `
            -AddressFamily 'IPv4' `
            -ErrorAction SilentlyContinue |
            Select-Object -First 1
        $netNat = Get-NetNat -Name $NatName -ErrorAction SilentlyContinue |
            Select-Object -First 1

        $natNameOut = if ($null -ne $netNat) { $netNat.Name } else { '' }
        $natPrefixOut = if ($null -ne $netNat) { $netNat.InternalIPInterfaceAddressPrefix } else { '' }
        $natHostOut = if ($null -ne $natIp) { $natIp.IPAddress } else { '' }

        $sw |
            Select-Object `
                Name,
                @{ N = 'SwitchType';                      E = { 'NAT' } },
                AllowManagementOS,
                NetAdapterInterfaceDescription,
                Notes,
                @{ N = 'Id';                              E = { $_.Id.ToString() } },
                @{ N = 'NatName';                         E = { $natNameOut } },
                @{ N = 'NatInternalAddressPrefix';        E = { $natPrefixOut } },
                @{ N = 'NatHostAddress';                  E = { $natHostOut } } |
            Write-HypervResult
        return
    }

    $setArgs = @{
        Name        = $Name
        ErrorAction = 'Stop'
    }
    if ($PSBoundParameters.ContainsKey('NetAdapterNames')) {
        # Set-VMSwitch -NetAdapterName is typed [string] (single NIC), unlike
        # New-VMSwitch which auto-unwraps a one-element [string[]]. Index
        # explicitly so the binder gets a string. NIC teaming is configured
        # outside the switch resource via Set-VMSwitchTeam; the schema's
        # list shape is preserved for symmetry, but only the first entry
        # binds the switch.
        if ($NetAdapterNames.Count -eq 0) {
            throw "net_adapter_names must contain at least one adapter"
        }
        $setArgs.NetAdapterName = $NetAdapterNames[0]
    }
    if ($null -ne $AllowManagementOS) {
        $setArgs.AllowManagementOS = [bool]$AllowManagementOS
    }
    if ($PSBoundParameters.ContainsKey('Notes')) {
        $setArgs.Notes = $Notes
    }

    # Set-VMSwitch errors with "You must specify at least one parameter" when
    # called with only -Name. The Go-side Update should never trigger this
    # (Update only runs when there's a diff), but guard explicitly so a
    # contract violation produces a clear error instead of the cmdlet's
    # confusing one. $setArgs always carries Name + ErrorAction; anything
    # beyond those is a mutable attribute.
    if ($setArgs.Count -le 2) {
        throw "Set-HypervSwitch requires at least one mutable attribute (net_adapter_names, allow_management_os, or notes)"
    }

    Set-VMSwitch @setArgs

    Get-VMSwitch -Name $Name -ErrorAction Stop |
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

# Entry block. Skipped during Pester runs (dot-source sets InvocationName='.').
if ($MyInvocation.InvocationName -ne '.') {
    try {
        $params = [Console]::In.ReadToEnd() | ConvertFrom-Json

        $callArgs = @{
            Name = $params.name
        }
        if ($params.PSObject.Properties.Name -contains 'switch_type' -and $null -ne $params.switch_type) {
            $callArgs.SwitchType = $params.switch_type
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
        if ($params.PSObject.Properties.Name -contains 'nat_name' -and $null -ne $params.nat_name -and $params.nat_name -ne '') {
            $callArgs.NatName = $params.nat_name
        }
        if ($params.PSObject.Properties.Name -contains 'nat_internal_address_prefix' -and $null -ne $params.nat_internal_address_prefix -and $params.nat_internal_address_prefix -ne '') {
            $callArgs.NatInternalAddressPrefix = $params.nat_internal_address_prefix
        }

        Set-HypervSwitch @callArgs
    }
    catch {
        Write-HypervError $_
        exit 1
    }
}
