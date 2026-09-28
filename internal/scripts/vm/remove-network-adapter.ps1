# vm/remove-network-adapter.ps1 -- detach a NIC from a VM by display name.
#
# Wire contract (locked in by Tests.ps1):
#
#   stdin JSON  : {
#                   "name":    "<adapter-display-name>",
#                   "vm_name": "<vm-name>"
#                 }
#   stdout JSON : {} on success.
#   stderr/exit : missing VM   -> ObjectNotFound -> ErrNotFound
#                 missing NIC  -> ObjectNotFound -> ErrNotFound (the
#                 reconciliation in Update treats this as a no-op since
#                 the desired state -- NIC removed -- is already met).
#
# Remove-VMNetworkAdapter -Name <X> removes every NIC named X; the
# Go-side resource validator rejects duplicate names within a VM's NIC
# list at plan time, so this is well-defined for names this provider set.
#
# lint:allow-long-comment

function Remove-HypervVMNetworkAdapter {
    [CmdletBinding()]
    param(
        [Parameter(Mandatory)] [string] $Name,
        [Parameter(Mandatory)] [string] $VMName
    )
    Remove-VMNetworkAdapter `
        -VMName $VMName `
        -Name $Name `
        -ErrorAction Stop
    @{} | Write-HypervResult
}

# Entry block. Skipped during Pester runs (dot-source sets InvocationName='.').
if ($MyInvocation.InvocationName -ne '.') {
    try {
        $params = Read-HypervStdinPayload
        Remove-HypervVMNetworkAdapter `
            -Name   $params.name `
            -VMName $params.vm_name
    }
    catch {
        Write-HypervError $_
        exit 1
    }
}
