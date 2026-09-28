# vswitch/list.ps1 -- enumerate virtual switches whose name matches a prefix.
#
# Wire contract (locked in by Tests.ps1):
#
#   stdin JSON  : { "name_prefix": "<string>" }
#   stdout JSON : [ { "name": "<switch-name>" }, ... ] -- always a JSON
#                 array, even on zero or one match.
#   stderr/exit : 0 on success (including the empty-result case).
#
# Used by the acceptance-test sweeper, which only needs the name for its
# RemoveVMSwitch call. Same prefix-filter-after-Get-VMSwitch pattern as
# vm/list.ps1: the wildcard form via -Name behaves inconsistently across
# PS versions on no-match, so Where-Object filters after enumeration.
#
# lint:allow-long-comment

# Get-HypervVMSwitchByPrefix returns Get-VMSwitch filtered by
# `Name -like "${prefix}*"`. Symmetric with Get-HypervVMByPrefix.
function Get-HypervVMSwitchByPrefix {
    [CmdletBinding()]
    param(
        [Parameter(Mandatory)] [string] $NamePrefix
    )
    $pattern = "${NamePrefix}*"
    $results = @(Get-VMSwitch -ErrorAction Stop |
        Where-Object { $_.Name -like $pattern } |
        ForEach-Object { [pscustomobject]@{ Name = $_.Name } })

    # -InputObject keeps the output array-typed even at zero or one match (see vm/list.ps1).
    ConvertTo-Json -InputObject $results -Depth 10 -Compress
}

# Entry block. Skipped during Pester runs (dot-source sets InvocationName='.').
if ($MyInvocation.InvocationName -ne '.') {
    try {
        $params = Read-HypervStdinPayload
        Get-HypervVMSwitchByPrefix -NamePrefix $params.name_prefix
    }
    catch {
        Write-HypervError $_
        exit 1
    }
}
