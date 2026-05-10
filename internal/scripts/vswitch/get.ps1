# vswitch/get.ps1 -- fetch a single virtual switch by name.
#
# Wire contract (locked in by Tests.ps1):
#
#   stdin JSON  : { "name": "<switch-name>", "nat_name": "<string>"? }
#                 nat_name is optional; the Go-side resource Read passes it
#                 from prior state for NAT-typed resources so the script
#                 can join Get-NetIPAddress + Get-NetNat into the read shape.
#   stdout JSON : single VMSwitch object with the keys
#                   Name, SwitchType, AllowManagementOS,
#                   NetAdapterInterfaceDescription, Notes, Id,
#                   NatName, NatInternalAddressPrefix, NatHostAddress.
#                 SwitchType is the enum stringified ("External"/"Internal"/
#                 "Private") OR synthesized "NAT" when nat_name is supplied
#                 and Get-NetNat + Get-NetIPAddress both succeed; Id is the
#                 Guid stringified. NAT fields are empty strings for non-NAT
#                 switches.
#   stderr/exit : missing switch -> Write-HypervError envelope with
#                 category=ObjectNotFound + exit 1, mapped to ErrNotFound on
#                 the Go side (resource Read calls RemoveResource).
#                 vmms-stopped surfaces as ResourceUnavailable -> ErrUnavailable.
#                 For NAT switches, missing NetNat or missing NetIPAddress
#                 also surfaces as ObjectNotFound -- partial NAT teardown
#                 means the resource as a whole is gone.
#
# Tests dot-source this file (`. ./get.ps1`); the entry block is guarded so it
# only runs when the script is invoked directly. The select-block shape is
# duplicated across get/new/set on purpose -- the Go runtime concatenates only
# preamble + a single verb script per call, so cross-script helpers aren't
# visible at runtime.

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
        # "Switch missing" surfaces in two shapes:
        #   1. CategoryInfo.Category = ObjectNotFound -- the documented
        #      contract; what some Hyper-V module versions emit.
        #   2. CategoryInfo.Category = InvalidArgument with
        #      FullyQualifiedErrorId =
        #      'InvalidParameter,Microsoft.HyperV.PowerShell.Commands.GetVMSwitch'
        #      -- what Get-VMSwitch actually emits on Server 2022 + PS 5.1
        #      (verified 2026-04 against a real bench; the acc test for
        #      hyperv_virtual_switch's CheckDestroy caught this). The FQId
        #      is precise enough that unrelated InvalidArgument errors
        #      (bad name format, etc.) still propagate as the design
        #      intends -- only the canonical "Get-VMSwitch says not found"
        #      shape is treated as missing.
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

    # NAT augmentation. Hyper-V reports SwitchType=Internal for the
    # underlying VMSwitch; we synthesize SwitchType=NAT when the caller
    # passes -NatName and the matching NetNat + NetIPAddress are both
    # present on the host. Either piece missing means the NAT triple has
    # been partially torn down out-of-band -- treat as resource-gone so
    # the Go side's RemoveResource fires and the next apply can recreate
    # cleanly rather than reporting drift on a half-broken state.
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
        if ($null -eq $netNat -or $null -eq $natIp) {
            $exception = [System.Management.Automation.ItemNotFoundException]::new(
                "NAT switch '$Name' is missing its NetNat ('$NatName') or NetIPAddress (vEthernet ($Name)). Treating the resource as gone.")
            $errorRecord = [System.Management.Automation.ErrorRecord]::new(
                $exception, 'VMSwitchNATPartial',
                [System.Management.Automation.ErrorCategory]::ObjectNotFound, $Name)
            throw $errorRecord
        }
        $synthesizedType = 'NAT'
        $natNameOut = $netNat.Name
        $natPrefixOut = $netNat.InternalIPInterfaceAddressPrefix
        $natHostOut = $natIp.IPAddress
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
        $params = [Console]::In.ReadToEnd() | ConvertFrom-Json
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
