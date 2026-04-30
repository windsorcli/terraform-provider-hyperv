# vm/add-network-adapter.ps1 -- attach a new NIC to a VM and bind it to
# a virtual switch.
#
# Wire contract (locked in by Tests.ps1):
#
#   stdin JSON  : {
#                   "name":        "<adapter-display-name>",
#                   "vm_name":     "<vm-name>",
#                   "switch_name": "<vswitch-name>"
#                 }
#   stdout JSON : {} on success.
#   stderr/exit : missing VM   -> ObjectNotFound -> ErrNotFound
#                 missing switch -> InvalidArgument (Hyper-V's "switch
#                 not found" surfaces here as part of the Add cmdlet's
#                 input validation) -> ErrPSExecution.
#
# The display name is the user's slot key for diff/reconciliation; the
# Go-side resource layer enforces uniqueness within a VM's NIC list at
# plan time.

function Add-HypervVMNetworkAdapter {
    [CmdletBinding()]
    param(
        [Parameter(Mandatory)] [string] $Name,
        [Parameter(Mandatory)] [string] $VMName,
        [Parameter(Mandatory)] [string] $SwitchName
    )
    Add-VMNetworkAdapter `
        -VMName $VMName `
        -Name $Name `
        -SwitchName $SwitchName `
        -ErrorAction Stop | Out-Null
    @{} | Write-HypervResult
}

# Entry block. Skipped during Pester runs (dot-source sets InvocationName='.').
if ($MyInvocation.InvocationName -ne '.') {
    try {
        $params = [Console]::In.ReadToEnd() | ConvertFrom-Json
        Add-HypervVMNetworkAdapter `
            -Name       $params.name `
            -VMName     $params.vm_name `
            -SwitchName $params.switch_name
    }
    catch {
        Write-HypervError $_
        exit 1
    }
}
