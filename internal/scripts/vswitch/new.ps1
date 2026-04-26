# vswitch/new.ps1 -- create a new virtual switch.
#
# Wire contract (locked in by Tests.ps1):
#
#   stdin JSON  : {
#                   "name":                "<string>",                      # required
#                   "switch_type":         "External"|"Internal"|"Private", # required
#                   "net_adapter_names":   ["<string>", ...],               # External only
#                   "allow_management_os": <bool>,                          # External/Internal only
#                   "notes":               "<string>"                       # optional
#                 }
#   stdout JSON : the created switch in the canonical read shape (same fields
#                 as get.ps1 -- create round-trips through the same contract
#                 as Read).
#
# Validation strategy: trust the Go-side TF schema validators. Cmdlet errors
# (e.g. -SwitchType External requires -NetAdapterName) propagate through the
# PLAN.md S5 envelope on the catch.

# New-HypervSwitch builds the parameter splat for New-VMSwitch from typed
# inputs, runs the cmdlet, and emits the canonical read shape.
function New-HypervSwitch {
    [CmdletBinding()]
    param(
        [Parameter(Mandatory)] [string] $Name,
        [Parameter(Mandatory)] [ValidateSet('External', 'Internal', 'Private')] [string] $SwitchType,
        [string[]]       $NetAdapterNames,
        [Nullable[bool]] $AllowManagementOS,
        [string]         $Notes
    )

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
    if ($null -ne $AllowManagementOS) {
        $newArgs.AllowManagementOS = [bool]$AllowManagementOS
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
            @{ N = 'Id';                              E = { $_.Id.ToString() } } |
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

        New-HypervSwitch @callArgs
    }
    catch {
        Write-HypervError $_
        exit 1
    }
}
