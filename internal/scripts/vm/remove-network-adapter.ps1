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
# Important: Remove-VMNetworkAdapter -Name <X> removes ALL NICs whose
# display name equals X. Hyper-V allows multiple NICs with the same
# name (the cmdlet doesn't enforce uniqueness), but the Go-side
# resource validator rejects duplicate names within a VM's NIC list at
# plan time, so this script's behavior is well-defined for our case.
# A user who somehow ended up with duplicate-named NICs (e.g. via an
# out-of-band Add-VMNetworkAdapter) would see all duplicates removed
# on a single detach -- documented limitation, surfaced via the
# script's unchanged behavior.

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
        $params = Read-HypervStdinParams
        Remove-HypervVMNetworkAdapter `
            -Name   $params.name `
            -VMName $params.vm_name
    }
    catch {
        Write-HypervError $_
        exit 1
    }
}
