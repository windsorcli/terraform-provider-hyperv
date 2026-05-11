# vswitch/list.ps1 -- enumerate virtual switches whose name matches a prefix.
#
# Wire contract (locked in by Tests.ps1):
#
#   stdin JSON  : { "name_prefix": "<string>" }
#   stdout JSON : [ { "name": "<switch-name>" }, ... ] -- always a JSON
#                 array, even on zero or one match.
#   stderr/exit : 0 on success (including the empty-result case).
#
# Used by the acceptance-test sweeper. Minimal wire shape -- only Name
# is carried because the sweeper's RemoveVMSwitch call only needs the
# name (the existing acctest bar uses Private + Internal switches only;
# NAT-switch sweep support can extend this script with a NatName field
# when NAT acctests land).
#
# Same prefix-filter-after-Get-VMSwitch pattern as vm/list.ps1: the
# wildcard form via -Name behaves inconsistently across PS versions on
# no-match, so we filter with Where-Object in PowerShell after the
# enumeration call.

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

    # -InputObject keeps the shape array-typed even at zero or one
    # match -- see vm/list.ps1 header for the full rationale.
    ConvertTo-Json -InputObject $results -Depth 10 -Compress
}

# Entry block. Skipped during Pester runs (dot-source sets InvocationName='.').
if ($MyInvocation.InvocationName -ne '.') {
    try {
        $params = [Console]::In.ReadToEnd() | ConvertFrom-Json
        Get-HypervVMSwitchByPrefix -NamePrefix $params.name_prefix
    }
    catch {
        Write-HypervError $_
        exit 1
    }
}
