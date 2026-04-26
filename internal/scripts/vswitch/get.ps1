# vswitch/get.ps1 -- fetch a single virtual switch by name.
#
# Wire contract (locked in by Tests.ps1):
#
#   stdin JSON  : { "name": "<switch-name>" }
#   stdout JSON : single VMSwitch object with the keys
#                   Name, SwitchType, AllowManagementOS,
#                   NetAdapterInterfaceDescription, Notes, Id.
#                 SwitchType is the enum stringified ("External"/"Internal"/
#                 "Private"); Id is the Guid stringified.
#   stderr/exit : missing switch -> Write-HypervError envelope with
#                 category=ObjectNotFound + exit 1, mapped to ErrNotFound on
#                 the Go side (resource Read calls RemoveResource).
#                 vmms-stopped surfaces as ResourceUnavailable -> ErrUnavailable.
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
        [Parameter(Mandatory)] [string] $Name
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
        if ($_.CategoryInfo.Category -ne [System.Management.Automation.ErrorCategory]::ObjectNotFound) {
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
    $sw |
        Select-Object `
            Name,
            @{ N = 'SwitchType';                      E = { $_.SwitchType.ToString() } },
            AllowManagementOS,
            NetAdapterInterfaceDescription,
            Notes,
            @{ N = 'Id';                              E = { $_.Id.ToString() } } |
        Write-HypervResult
}

# Entry block. Skipped during Pester runs (dot-source sets InvocationName='.').
if ($MyInvocation.InvocationName -ne '.') {
    try {
        $params = [Console]::In.ReadToEnd() | ConvertFrom-Json
        Get-HypervSwitch -Name $params.name
    }
    catch {
        Write-HypervError $_
        exit 1
    }
}
