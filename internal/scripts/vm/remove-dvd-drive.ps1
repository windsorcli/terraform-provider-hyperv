# vm/remove-dvd-drive.ps1 -- detach a DVD drive from a VM at a specific
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
#                 already empty surfaces the same -> ErrNotFound, which
#                 the resource-layer reconciliation in Update treats
#                 as a no-op.
#
# Like remove-hard-disk-drive.ps1, this is slot-keyed: the iso_path
# isn't part of the wire payload because the slot tuple alone
# identifies which DVD drive to remove. Whatever ISO (if any) was
# loaded gets implicitly ejected as part of the detach.

function Remove-HypervVMDvdDrive {
    [CmdletBinding()]
    [Diagnostics.CodeAnalysis.SuppressMessageAttribute(
        'PSReviewUnusedParameter', 'ControllerType',
        Justification = 'Part of the wire contract for slot identification; Remove-VMDvdDrive itself does not accept -ControllerType (gen 1 = IDE, gen 2 = SCSI is implicit).')]
    param(
        [Parameter(Mandatory)] [string] $Name,
        [Parameter(Mandatory)] [ValidateSet('SCSI', 'IDE')] [string] $ControllerType,
        [Parameter(Mandatory)] [int]    $ControllerNumber,
        [Parameter(Mandatory)] [int]    $ControllerLocation
    )
    Remove-VMDvdDrive `
        -VMName $Name `
        -ControllerNumber $ControllerNumber `
        -ControllerLocation $ControllerLocation `
        -ErrorAction Stop
    @{} | Write-HypervResult
}

# Entry block. Skipped during Pester runs (dot-source sets InvocationName='.').
if ($MyInvocation.InvocationName -ne '.') {
    try {
        $params = [Console]::In.ReadToEnd() | ConvertFrom-Json
        Remove-HypervVMDvdDrive `
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
