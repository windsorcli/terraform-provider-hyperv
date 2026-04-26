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

# Get-HypervSwitch wraps Get-VMSwitch with -ErrorAction Stop so the missing-
# switch case raises a terminating error the entry block can convert into the
# PLAN.md S5 envelope. Output goes through Write-HypervResult per the single-object
# contract.
function Get-HypervSwitch {
    [CmdletBinding()]
    param(
        [Parameter(Mandatory)] [string] $Name
    )
    Get-VMSwitch -Name $Name -ErrorAction Stop |
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
