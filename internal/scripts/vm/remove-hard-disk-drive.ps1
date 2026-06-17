# vm/remove-hard-disk-drive.ps1 -- detach a VHD from a VM at a specific
# controller slot.
#
# Wire contract (locked in by Tests.ps1):
#
#   stdin JSON  : {
#                   "name":                "<vm-name>",
#                   "controller_type":     "SCSI" | "IDE",
#                   "controller_number":   <int>,
#                   "controller_location": <int>
#                 }
#   stdout JSON : {} on success.
#   stderr/exit : missing VM -> ObjectNotFound -> ErrNotFound. Slot
#                 already empty -> Remove-VMHardDiskDrive surfaces an
#                 ObjectNotFound from Hyper-V's perspective; the Go-side
#                 reconciliation treats this as a no-op (the slot is
#                 already in the desired empty state).
#
# Path is intentionally NOT a parameter here -- the slot identifies the
# attachment, not the underlying VHD. Two attachments to the same VHD at
# different slots would otherwise need disambiguation, and that matches
# the cmdlet's own contract (Remove-VMHardDiskDrive keys on slot, not
# path).

function Remove-HypervVMHardDiskDrive {
    [CmdletBinding()]
    param(
        [Parameter(Mandatory)] [string] $Name,
        [Parameter(Mandatory)] [ValidateSet('SCSI', 'IDE')] [string] $ControllerType,
        [Parameter(Mandatory)] [int]    $ControllerNumber,
        [Parameter(Mandatory)] [int]    $ControllerLocation
    )
    Remove-VMHardDiskDrive `
        -VMName $Name `
        -ControllerType $ControllerType `
        -ControllerNumber $ControllerNumber `
        -ControllerLocation $ControllerLocation `
        -ErrorAction Stop
    @{} | Write-HypervResult
}

# Entry block. Skipped during Pester runs (dot-source sets InvocationName='.').
if ($MyInvocation.InvocationName -ne '.') {
    try {
        $params = Read-HypervStdinParams
        Remove-HypervVMHardDiskDrive `
            -Name               $params.name `
            -ControllerType     $params.controller_type `
            -ControllerNumber   $params.controller_number `
            -ControllerLocation $params.controller_location
    }
    catch {
        Write-HypervError $_
        exit 1
    }
}
