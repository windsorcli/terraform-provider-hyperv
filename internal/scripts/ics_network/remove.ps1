# ics_network/remove.ps1 -- delete one HCN ICS network by ID.
#
# Wire contract (locked in by Tests.ps1):
#
#   stdin JSON  : { "id": "<HCN network GUID>" }
#   stdout      : empty (caller passes dst=nil to runScript).
#
# A network that is already gone is success. Deleting the last ICS
# network also removes the VMSwitch every ICS network shares.
#
# lint:allow-long-comment

# Remove-HypervIcsNetwork deletes the network, treating a not-found HRESULT as success.
function Remove-HypervIcsNetwork {
    [CmdletBinding()]
    param(
        [Parameter(Mandatory)] [guid] $Id
    )
    $call = Invoke-HypervHcnDelete -Id $Id
    if ($call.HResult -eq 0 -or (Test-HypervHcnNotFound -HResult $call.HResult)) { return }
    throw (New-HypervHcnErrorRecord -HResult $call.HResult -Operation 'delete' -Target $Id.ToString() -ErrorRecord $call.ErrorRecord)
}

# Entry block. Skipped during Pester runs (dot-source sets InvocationName='.').
if ($MyInvocation.InvocationName -ne '.') {
    try {
        $params = Read-HypervStdinPayload
        Remove-HypervIcsNetwork -Id $params.id
    }
    catch {
        Write-HypervError $_
        exit 1
    }
}
