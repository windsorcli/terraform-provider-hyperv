# ics_network/get.ps1 -- read one HCN ICS network by ID.
#
# Wire contract (locked in by Tests.ps1):
#
#   stdin JSON  : { "id": "<HCN network GUID>" }
#   stdout JSON : single six-field object (Id, Name, AddressPrefix,
#                 HostAddress, IsolationId, SwitchName).
#   stderr/exit : missing network -> ObjectNotFound envelope, exit 1 ->
#                 Go maps to ErrNotFound so Read calls RemoveResource.
#                 Missing hns service -> NotInstalled envelope naming
#                 the Containers-HNS feature.
#
# lint:allow-long-comment

# Get-HypervIcsNetwork emits the read result for one ICS network.
function Get-HypervIcsNetwork {
    [CmdletBinding()]
    param(
        [Parameter(Mandatory)] [guid] $Id
    )
    Read-HypervIcsNetwork -Id $Id | Write-HypervResult
}

# Entry block. Skipped during Pester runs (dot-source sets InvocationName='.').
if ($MyInvocation.InvocationName -ne '.') {
    try {
        $params = Read-HypervStdinPayload
        Get-HypervIcsNetwork -Id $params.id
    }
    catch {
        Write-HypervError $_
        exit 1
    }
}
