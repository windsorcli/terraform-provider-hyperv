# vswitch/get.ps1 -- fetch a single virtual switch by name.
#
# Wire contract (locked in by Tests.ps1):
#
#   stdin JSON  : { "name": "<switch-name>", "nat_name": "<string>"? }
#                 nat_name is optional; the Go-side resource Read passes it
#                 from prior state for NAT-typed resources so the script
#                 can join Get-NetIPAddress + Get-NetNat into the output.
#   stdout JSON : single VMSwitch object with the keys
#                   Name, SwitchType, AllowManagementOS,
#                   NetAdapterInterfaceDescription, Notes, Id,
#                   NatName, NatInternalAddressPrefix, NatHostAddress.
#                 SwitchType is the enum stringified ("External"/"Internal"/
#                 "Private") OR synthesized "NAT" when nat_name is supplied;
#                 Id is the Guid stringified. With nat_name, NatName echoes
#                 it, NatInternalAddressPrefix is empty when the NetNat is
#                 missing, and NatHostAddress is empty when the host IP is
#                 missing. NAT fields are empty strings for non-NAT switches.
#   stderr/exit : missing switch -> ObjectNotFound envelope, exit 1 -> Go
#                 maps to ErrNotFound (resource Read calls RemoveResource).
#                 vmms-stopped -> ResourceUnavailable -> ErrUnavailable. A
#                 missing NetNat or host IP is not ObjectNotFound: the
#                 VMSwitch still exists, so Terraform keeps it in state.
#
# Tests dot-source this file; the entry block is guarded to skip when
# invoked that way. The Select-Object projection is duplicated across
# get/new/set on purpose: the runtime concatenates only preamble plus a
# single verb script per call, so cross-script helpers aren't visible.
#
# lint:allow-long-comment

# Get-HypervSwitch fetches a switch by name. Missing-switch case throws an
# explicit ObjectNotFound so the Go-side typed client maps it to ErrNotFound
# (resource Read calls RemoveResource; data-source Read produces a clear
# attribute-anchored diagnostic).
function Get-HypervSwitch {
    [CmdletBinding()]
    param(
        [Parameter(Mandatory)] [string] $Name,
        [string] $NatName
    )
    # Stop + selective catch instead of SilentlyContinue: a transient WMI
    # fault, permission error, or cluster-connectivity blip would otherwise
    # be indistinguishable from "switch missing", get remapped to ObjectNotFound,
    # and let the Go-side resource Read drop the switch from state via
    # RemoveResource -- after which the next apply calls New-VMSwitch and
    # fails on a name conflict, requiring manual import or taint to recover.
    try {
        $sw = Get-VMSwitch -Name $Name -ErrorAction Stop
    }
    catch {
        # "Switch missing" surfaces as ObjectNotFound on some module versions, or as this specific InvalidArgument FQEId on Server 2022 + PS 5.1.
        $isMissing = (
            $_.CategoryInfo.Category -eq [System.Management.Automation.ErrorCategory]::ObjectNotFound
        ) -or (
            $_.FullyQualifiedErrorId -eq 'InvalidParameter,Microsoft.HyperV.PowerShell.Commands.GetVMSwitch'
        )
        if (-not $isMissing) {
            throw
        }
        $sw = $null
    }
    if ($null -eq $sw) {
        $exception = [System.Management.Automation.ItemNotFoundException]::new(
            "Hyper-V was unable to find a virtual switch with name '$Name'.")
        $errorRecord = [System.Management.Automation.ErrorRecord]::new(
            $exception, 'VMSwitchNotFound',
            [System.Management.Automation.ErrorCategory]::ObjectNotFound, $Name)
        throw $errorRecord
    }

    # Hyper-V reports a NAT switch as Internal; a missing NetNat or host IP blanks only its own fields.
    $synthesizedType = $sw.SwitchType.ToString()
    $natNameOut = ''
    $natPrefixOut = ''
    $natHostOut = ''
    if ($PSBoundParameters.ContainsKey('NatName') -and $NatName -ne '') {
        $natIp = Get-NetIPAddress `
            -InterfaceAlias "vEthernet ($Name)" `
            -AddressFamily 'IPv4' `
            -ErrorAction SilentlyContinue |
            Select-Object -First 1
        $netNat = Get-NetNat -Name $NatName -ErrorAction SilentlyContinue |
            Select-Object -First 1
        $synthesizedType = 'NAT'
        $natNameOut = $NatName
        if ($null -ne $netNat) {
            $natPrefixOut = $netNat.InternalIPInterfaceAddressPrefix
        }
        if ($null -ne $natIp) {
            $natHostOut = $natIp.IPAddress
        }
    }

    $sw |
        Select-Object `
            Name,
            @{ N = 'SwitchType';                      E = { $synthesizedType } },
            AllowManagementOS,
            NetAdapterInterfaceDescription,
            Notes,
            @{ N = 'Id';                              E = { $_.Id.ToString() } },
            @{ N = 'NatName';                         E = { $natNameOut } },
            @{ N = 'NatInternalAddressPrefix';        E = { $natPrefixOut } },
            @{ N = 'NatHostAddress';                  E = { $natHostOut } } |
        Write-HypervResult
}

# Entry block. Skipped during Pester runs (dot-source sets InvocationName='.').
if ($MyInvocation.InvocationName -ne '.') {
    try {
        $params = Read-HypervStdinPayload
        $callArgs = @{ Name = $params.name }
        if ($params.PSObject.Properties.Name -contains 'nat_name' -and $null -ne $params.nat_name -and $params.nat_name -ne '') {
            $callArgs.NatName = $params.nat_name
        }
        Get-HypervSwitch @callArgs
    }
    catch {
        Write-HypervError $_
        exit 1
    }
}
