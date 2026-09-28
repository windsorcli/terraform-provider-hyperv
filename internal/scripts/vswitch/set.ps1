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
#   stdout JSON : the updated switch in the canonical format (same fields
#                 as get.ps1, emitted by re-reading after the mutation lands).
#
# Only keys present in the input are touched. switch_type is immutable
# (RequiresReplace on the Go side) and is not forwarded to Set-VMSwitch;
# when present it's used only to mirror new.ps1's Private +
# AllowManagementOS reject path, populated from prior state on Update.
#
# lint:allow-long-comment

# Set-HypervSwitch applies a partial update via Set-VMSwitch, then re-reads
# via Get-VMSwitch so the output matches Read exactly. Two-step instead of
# -PassThru because Set-VMSwitch's -PassThru behavior across NIC rebinding
# is uneven across PS 5.1 / 7.x.
function Set-HypervSwitch {
    [CmdletBinding()]
    param(
        [Parameter(Mandatory)] [string] $Name,
        [ValidateSet('External', 'Internal', 'Private', 'NAT')] [string] $SwitchType,
        [string[]]       $NetAdapterNames,
        [Nullable[bool]] $AllowManagementOS,
        [string]         $Notes,
        [string]         $NatName
    )

    # Existence pre-check, symmetric with get.ps1/remove.ps1: Set-VMSwitch's own missing-switch error maps to ErrPSExecution, not ErrNotFound.
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

    # Reads $existing.SwitchType (host truth), not the caller's hint: trusting an omitted switch_type would silently flip an Internal switch to Private.
    if ($null -ne $AllowManagementOS -and $existing.SwitchType.ToString() -ne 'External') {
        throw "allow_management_os is not valid for switch_type '$($existing.SwitchType)' (External only)"
    }

    # NAT branch. Every NAT-specific input is RequiresReplace at the
    # schema layer (nat_name, nat_internal_address_prefix, and
    # nat_host_address all force replacement -- Set-NetNat does not
    # accept -InternalIPInterfaceAddressPrefix, so prefix changes can
    # only be expressed as a teardown + recreate). The only mutation
    # that reaches Update for a NAT switch is Notes, applied to the
    # underlying VMSwitch. The read-back joins Get-NetNat +
    # Get-NetIPAddress to synthesize SwitchType=NAT in the output.
    if ($SwitchType -eq 'NAT') {
        if (-not $PSBoundParameters.ContainsKey('Notes')) {
            throw "Set-HypervSwitch requires at least one mutable attribute (notes)"
        }
        Set-VMSwitch -Name $Name -Notes $Notes -ErrorAction Stop

        # Read-back mirrors get.ps1's NAT augmentation to synthesize SwitchType=NAT.
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
        # Set-VMSwitch -NetAdapterName takes a single string, unlike New-VMSwitch's [string[]]; only the first entry binds.
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
        $params = Read-HypervStdinPayload

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

        Set-HypervSwitch @callArgs
    }
    catch {
        Write-HypervError $_
        exit 1
    }
}
