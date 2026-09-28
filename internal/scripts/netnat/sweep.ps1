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
# Combined list-and-remove (unlike the split list.ps1/remove.ps1 pattern
# for VMs and switches) saves a second SSH hop. A Remove-NetNat failure
# on one instance logs and continues rather than aborting the sweep.
#
# lint:allow-long-comment

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
    # [string[]] typing keeps a single match from unboxing to a scalar in the JSON output.
    [string[]]$removed = @()

    # $_ -and guard avoids a Set-StrictMode PropertyNotFound if a $null element ever reaches the pipeline.
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
        $params = Read-HypervStdinPayload
        Invoke-HypervNetNatSweep -NamePrefix $params.name_prefix
    }
    catch {
        Write-HypervError $_
        exit 1
    }
}
