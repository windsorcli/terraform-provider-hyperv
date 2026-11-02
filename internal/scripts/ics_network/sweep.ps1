# ics_network/sweep.ps1 -- delete every HCN ICS network whose name matches
# a prefix. Used by the acceptance-test sweeper to clear orphan `tfacc-*`
# networks left over from failed runs.
#
# Wire contract (locked in by Tests.ps1):
#
#   stdin JSON  : { "name_prefix": "<string>" }
#   stdout JSON : { "removed": [ "<name>", ... ] } -- always an object
#                 with a `removed` array, even on zero matches.
#   stderr/exit : 0 on success, including zero matches. A failed delete
#                 on one network logs a warning and the sweep continues.
#
# lint:allow-long-comment

# Invoke-HypervIcsNetworkSweep deletes the ICS networks under the prefix and returns their names.
function Invoke-HypervIcsNetworkSweep {
    [CmdletBinding()]
    param(
        # An empty prefix would match every ICS network on the host.
        [Parameter(Mandatory)] [ValidateNotNullOrEmpty()] [string] $NamePrefix
    )
    $pattern = "${NamePrefix}*"
    [string[]]$removed = @()

    $call = Invoke-HypervHcnEnumerate
    if ($call.HResult -ne 0) {
        throw (New-HypervHcnErrorRecord -HResult $call.HResult -Operation 'enumerate' -Target $NamePrefix -ErrorRecord $call.ErrorRecord)
    }
    $ids = @()
    # Assign before wrapping: PS 5.1 emits a parsed JSON array as one pipeline object.
    if (-not [string]::IsNullOrWhiteSpace($call.Result)) { $ids = $call.Result | ConvertFrom-Json }

    foreach ($id in @($ids)) {
        try {
            $network = Read-HypervIcsNetwork -Id $id
        }
        catch {
            continue
        }
        if ($network.Name -notlike $pattern) { continue }
        $delete = Invoke-HypervHcnDelete -Id $id
        if ($delete.HResult -eq 0) {
            $removed += $network.Name
        }
        else {
            Write-Warning ("HcnDeleteNetwork failed for '{0}' with {1}" -f $network.Name, (Format-HypervHResult -HResult $delete.HResult))
        }
    }

    ConvertTo-Json -InputObject ([pscustomobject]@{ removed = $removed }) -Depth 10 -Compress
}

# Entry block. Skipped during Pester runs (dot-source sets InvocationName='.').
if ($MyInvocation.InvocationName -ne '.') {
    try {
        $params = Read-HypervStdinPayload
        Invoke-HypervIcsNetworkSweep -NamePrefix $params.name_prefix
    }
    catch {
        Write-HypervError $_
        exit 1
    }
}
