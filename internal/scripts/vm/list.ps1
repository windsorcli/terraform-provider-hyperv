# vm/list.ps1 -- enumerate VMs whose name matches a prefix.
#
# Wire contract (locked in by Tests.ps1):
#
#   stdin JSON  : { "name_prefix": "<string>" }
#   stdout JSON : [ { "name": "<vm-name>" }, ... ]  -- always a JSON array,
#                 even when zero or one match.
#   stderr/exit : 0 on success (including the empty-result case).
#
# Used by the acceptance-test sweeper to find orphan tfacc-* VMs after a
# crashed run. Only Name is emitted, not the full get.ps1 fields: the
# sweeper only needs it for RemoveVM, and a narrower contract means a
# smaller blast radius if the script-Go contract drifts. The prefix is a
# parameter, not hardcoded, so the script survives a sweep-prefix change.
#
# lint:allow-long-comment

# Get-HypervVMByPrefix returns Get-VM filtered by `Name -like "${prefix}*"`.
# The wildcard form lives at the call site, not the parameter, since
# ValidatePattern can't carry a wildcard and callers shouldn't need to
# know PowerShell's wildcard syntax.
function Get-HypervVMByPrefix {
    [CmdletBinding()]
    param(
        [Parameter(Mandatory)] [string] $NamePrefix
    )
    $pattern = "${NamePrefix}*"
    $results = @(Get-VM -ErrorAction Stop |
        Where-Object { $_.Name -like $pattern } |
        ForEach-Object { [pscustomobject]@{ Name = $_.Name } })

    # -InputObject keeps the output array-typed (even zero/one match); without it a single result serializes to '{}', breaking the Go []VMName decoder.
    ConvertTo-Json -InputObject $results -Depth 10 -Compress
}

# Entry block. Skipped during Pester runs (dot-source sets InvocationName='.').
if ($MyInvocation.InvocationName -ne '.') {
    try {
        $params = Read-HypervStdinPayload
        Get-HypervVMByPrefix -NamePrefix $params.name_prefix
    }
    catch {
        Write-HypervError $_
        exit 1
    }
}
