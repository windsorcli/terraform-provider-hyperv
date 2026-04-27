# vm/remove.ps1 -- delete a VM. Stops the VM first if it's running.
#
# Wire contract (locked in by Tests.ps1):
#
#   stdin JSON  : { "name": "<vm-name>" }
#   stdout      : empty (caller passes dst=nil to runScript).
#   stderr/exit : missing VM -> Write-HypervError envelope with
#                 category=ObjectNotFound + exit 1, mapped to ErrNotFound on
#                 the Go side so Delete can treat already-gone as success.
#
# Stop-VM-then-Remove-VM is the standard pattern: Remove-VM -Force errors
# on a running VM ("VM cannot be removed while it is running"), so we have
# to power it off first. This is the one place the script DOES drive a
# power transition -- destroy is destructive by definition, so power-off-
# to-delete is acceptable. Non-destroy power transitions belong to
# hyperv_vm_state.

# Remove-HypervVM stops the VM (if running) and removes it. Same Stop +
# selective ObjectNotFound catch pattern as get/set: a missing VM raises
# ObjectNotFound (mapped to ErrNotFound on the Go side) so Delete is
# idempotent.
function Remove-HypervVM {
    [CmdletBinding()]
    param(
        [Parameter(Mandatory)] [string] $Name
    )
    try {
        $vm = Get-VM -Name $Name -ErrorAction Stop
    }
    catch {
        if ($_.CategoryInfo.Category -ne [System.Management.Automation.ErrorCategory]::ObjectNotFound) {
            throw
        }
        $exception = [System.Management.Automation.ItemNotFoundException]::new(
            "Hyper-V was unable to find a VM with name '$Name'.")
        $errorRecord = [System.Management.Automation.ErrorRecord]::new(
            $exception, 'VMNotFound',
            [System.Management.Automation.ErrorCategory]::ObjectNotFound, $Name)
        throw $errorRecord
    }

    # Stop only if not already off. Stop-VM on an already-off VM errors.
    if ($vm.State.ToString() -ne 'Off') {
        Stop-VM -Name $Name -Force -ErrorAction Stop
    }
    Remove-VM -Name $Name -Force -ErrorAction Stop
}

# Entry block. Skipped during Pester runs (dot-source sets InvocationName='.').
if ($MyInvocation.InvocationName -ne '.') {
    try {
        $params = [Console]::In.ReadToEnd() | ConvertFrom-Json
        Remove-HypervVM -Name $params.name
    }
    catch {
        Write-HypervError $_
        exit 1
    }
}
