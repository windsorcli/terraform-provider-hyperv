# vm/list.ps1 -- enumerate VMs whose name matches a prefix.
#
# Wire contract (locked in by Tests.ps1):
#
#   stdin JSON  : { "name_prefix": "<string>" }
#   stdout JSON : [ { "name": "<vm-name>" }, ... ]  -- always a JSON array,
#                 even when zero or one match.
#   stderr/exit : 0 on success (including the empty-result case).
#
# Used by the acceptance-test sweeper (internal/acctest/sweep.go) to find
# orphan tfacc-* VMs after a crashed run. The wire shape is intentionally
# minimal -- the sweeper only needs Name to call RemoveVM, so we omit the
# full read shape (State, MemoryAssigned, generation, ...) that get.ps1
# emits. A bigger shape would mean a slower enumeration and a wider
# blast radius if the script-Go contract drifts.
#
# Why a parameterized prefix instead of a hardcoded 'tfacc-': the script
# is contract-clean even if the project's sweep prefix changes, and
# future callers (a hypothetical `terraform state pull -inventory` shape)
# can reuse the script without forking.

# Get-HypervVMByPrefix returns Get-VM filtered by `Name -like "${prefix}*"`.
# The wildcard form lives at the call site (not in the parameter) because
# parameter ValidatePattern can't carry a wildcard; doing the construction
# here also keeps the contract honest -- callers supply "tfacc-", not
# "tfacc-*", and don't have to know about PowerShell's wildcard syntax.
function Get-HypervVMByPrefix {
    [CmdletBinding()]
    param(
        [Parameter(Mandatory)] [string] $NamePrefix
    )
    $pattern = "${NamePrefix}*"
    $results = @(Get-VM -ErrorAction Stop |
        Where-Object { $_.Name -like $pattern } |
        ForEach-Object { [pscustomobject]@{ Name = $_.Name } })

    # -InputObject prevents the pipeline from unrolling a single-element
    # array into a scalar, so the output shape is always a JSON array
    # (even with zero or one match). Without it, ConvertTo-Json would
    # emit '{}' for a single result instead of '[{...}]', breaking the
    # Go-side []VMName decoder.
    ConvertTo-Json -InputObject $results -Depth 10 -Compress
}

# Entry block. Skipped during Pester runs (dot-source sets InvocationName='.').
if ($MyInvocation.InvocationName -ne '.') {
    try {
        $params = Read-HypervStdinParams
        Get-HypervVMByPrefix -NamePrefix $params.name_prefix
    }
    catch {
        Write-HypervError $_
        exit 1
    }
}
