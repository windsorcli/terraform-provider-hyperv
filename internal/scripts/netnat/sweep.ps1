# netnat/sweep.ps1 -- find and remove NetNat instances whose name matches
# a prefix. Used by the acceptance-test sweeper to clear orphan
# `tfacc-*` NetNats left over from failed runs.
#
# Wire contract (locked in by Tests.ps1):
#
#   stdin JSON  : { "name_prefix": "<string>" }
#   stdout JSON : { "removed": [ "<name>", ... ] } -- always an object
#                 with a `removed` array, even on zero matches.
#   stderr/exit : 0 on success (including the zero-match case).
#
# Combined list-and-remove is correct here (vs the split list.ps1 /
# remove.ps1 pattern used for VMs and switches) because the sweeper
# round-trips once and removes whatever matches in the same call --
# saves the second SSH hop and the returned `removed` list lets the
# Go-side sweeper log what it cleared. Multiple NetNats can coexist
# on a host, so the foreach loop is load-bearing, not just defensive.
#
# Best-effort per-NetNat: a Remove-NetNat failure on one instance
# logs and continues to the next rather than aborting the whole
# sweep.

# Invoke-HypervNetNatSweep enumerates Get-NetNat, filters to names
# matching the prefix, calls Remove-NetNat on each, and returns the
# names that were removed.
function Invoke-HypervNetNatSweep {
    [CmdletBinding()]
    param(
        # ValidateNotNullOrEmpty is load-bearing: [Parameter(Mandatory)] [string]
        # accepts "" -- only $null is blocked -- which would expand $pattern to
        # "*" and sweep every NetNat on the host. The validation throws a
        # ParameterBindingValidationException that the entry block's try/catch
        # routes through Write-HypervError, matching the script's error envelope.
        [Parameter(Mandatory)] [ValidateNotNullOrEmpty()] [string] $NamePrefix
    )
    $pattern = "${NamePrefix}*"
    # [string[]] is load-bearing on PS 5.1: an untyped @() becomes [Object[]],
    # and ConvertTo-Json on a single-element [Object[]] property unboxes it to
    # a scalar -- {"removed":"tfacc-nat-abc"} instead of {"removed":["tfacc-nat-abc"]}.
    # Typing the variable forces the array shape through serialization.
    [string[]]$removed = @()

    # `$_ -and ...` guard before the .Name access keeps Set-StrictMode
    # v3.0 (set by the preamble) from throwing PropertyNotFound if a
    # future PS version ever surfaces a $null element through the
    # pipeline -- real Get-NetNat with no instance outputs nothing
    # rather than $null, but the guard is free and the failure mode
    # would otherwise be a cryptic strict-mode trap mid-sweep.
    $candidates = @(Get-NetNat -ErrorAction SilentlyContinue |
        Where-Object { $_ -and $_.Name -like $pattern })

    foreach ($nat in $candidates) {
        try {
            Remove-NetNat -Name $nat.Name -Confirm:$false -ErrorAction Stop
            $removed += $nat.Name
        }
        catch {
            Write-Warning ("Remove-NetNat failed for '{0}': {1}" -f $nat.Name, $_.Exception.Message)
        }
    }

    $result = [pscustomobject]@{ removed = $removed }
    ConvertTo-Json -InputObject $result -Depth 10 -Compress
}

# Entry block. Skipped during Pester runs (dot-source sets InvocationName='.').
if ($MyInvocation.InvocationName -ne '.') {
    try {
        $params = [Console]::In.ReadToEnd() | ConvertFrom-Json
        Invoke-HypervNetNatSweep -NamePrefix $params.name_prefix
    }
    catch {
        Write-HypervError $_
        exit 1
    }
}
