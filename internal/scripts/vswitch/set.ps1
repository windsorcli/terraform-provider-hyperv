# vswitch/set.ps1 -- update mutable attributes of an existing virtual switch.
#
# Wire contract (locked in by Tests.ps1):
#
#   stdin JSON  : {
#                   "name":                "<string>",                       # required (target switch)
#                   "net_adapter_names":   ["<string>", ...],                # External only, optional
#                   "allow_management_os": <bool>,                            # optional
#                   "notes":               "<string>"                         # optional
#                 }
#   stdout JSON : the updated switch in the canonical read shape (same fields
#                 as get.ps1 -- emitted by re-reading after the mutation lands).
#
# Only keys present in the input are touched. switch_type is immutable
# (RequiresReplace plan modifier on the Go side); attempts to send it here
# are silently ignored.

# Set-HypervSwitch applies a partial update via Set-VMSwitch, then re-reads
# via Get-VMSwitch so the emitted shape matches Read exactly. Two-step instead
# of -PassThru because Set-VMSwitch's -PassThru behavior across NIC rebinding
# is uneven across PS 5.1 / 7.x.
function Set-HypervSwitch {
    [CmdletBinding()]
    param(
        [Parameter(Mandatory)] [string] $Name,
        [string[]]       $NetAdapterNames,
        [Nullable[bool]] $AllowManagementOS,
        [string]         $Notes
    )

    $setArgs = @{
        Name        = $Name
        ErrorAction = 'Stop'
    }
    if ($PSBoundParameters.ContainsKey('NetAdapterNames')) {
        $setArgs.NetAdapterName = $NetAdapterNames
    }
    if ($null -ne $AllowManagementOS) {
        $setArgs.AllowManagementOS = [bool]$AllowManagementOS
    }
    if ($PSBoundParameters.ContainsKey('Notes')) {
        $setArgs.Notes = $Notes
    }

    Set-VMSwitch @setArgs

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

        $callArgs = @{
            Name = $params.name
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

        Set-HypervSwitch @callArgs
    }
    catch {
        Write-HypervError $_
        exit 1
    }
}
