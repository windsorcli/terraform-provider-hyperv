# vm/add-hard-disk-drive.ps1 -- attach an existing VHD to a VM at a
# specific controller slot.
#
# Wire contract (locked in by Tests.ps1):
#
#   stdin JSON  : {
#                   "name":                "<vm-name>",
#                   "controller_type":     "SCSI" | "IDE",
#                   "controller_number":   <int>,
#                   "controller_location": <int>,
#                   "path":                "<absolute-path-to-vhdx>"
#                 }
#   stdout JSON : {} on success (the resource layer does its own Get-VM
#                 round-trip via vm/get.ps1 for state hydration).
#   stderr/exit : missing VM -> ObjectNotFound -> ErrNotFound on the Go
#                 side. Slot already occupied / parent VHD missing /
#                 path on a path Hyper-V can't see -> InvalidArgument
#                 -> ErrPSExecution; surfaces verbatim with the cmdlet
#                 error so the operator knows which slot or which path.
#
# Doesn't validate controller_type against generation (gen 1 supports
# IDE+SCSI, gen 2 SCSI only); Add-VMHardDiskDrive's own error is clear
# enough to let propagate rather than duplicate the rule here.
#
# lint:allow-long-comment

function Add-HypervVMHardDiskDrive {
    [CmdletBinding()]
    param(
        [Parameter(Mandatory)] [string] $Name,
        [Parameter(Mandatory)] [ValidateSet('SCSI', 'IDE')] [string] $ControllerType,
        [Parameter(Mandatory)] [int]    $ControllerNumber,
        [Parameter(Mandatory)] [int]    $ControllerLocation,
        [Parameter(Mandatory)] [string] $Path
    )
    Add-VMHardDiskDrive `
        -VMName $Name `
        -ControllerType $ControllerType `
        -ControllerNumber $ControllerNumber `
        -ControllerLocation $ControllerLocation `
        -Path $Path `
        -ErrorAction Stop | Out-Null
    @{} | Write-HypervResult
}

# Entry block. Skipped during Pester runs (dot-source sets InvocationName='.').
if ($MyInvocation.InvocationName -ne '.') {
    try {
        $params = Read-HypervStdinPayload
        Add-HypervVMHardDiskDrive `
            -Name               $params.name `
            -ControllerType     $params.controller_type `
            -ControllerNumber   $params.controller_number `
            -ControllerLocation $params.controller_location `
            -Path               $params.path
    }
    catch {
        Write-HypervError $_
        exit 1
    }
}
