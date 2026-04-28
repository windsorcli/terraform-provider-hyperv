# vm/add-dvd-drive.ps1 -- attach a DVD drive (optionally with an ISO
# loaded) to a VM at a specific controller slot.
#
# Wire contract (locked in by Tests.ps1):
#
#   stdin JSON  : {
#                   "name":                "<vm-name>",
#                   "controller_type":     "SCSI" | "IDE",
#                   "controller_number":   <int>,
#                   "controller_location": <int>,
#                   "iso_path":            "<absolute-path-to-iso>" | null
#                 }
#   stdout JSON : {} on success.
#   stderr/exit : missing VM -> ObjectNotFound -> ErrNotFound. Slot
#                 already occupied -> InvalidArgument -> ErrPSExecution.
#                 Bad ISO path -> InvalidArgument -> ErrPSExecution.
#
# iso_path is optional: an empty DVD drive (no medium loaded) is a
# legitimate configuration. Add-VMDvdDrive without -Path produces an
# empty drive that can later have an ISO inserted via the swap path
# (detach + attach with iso_path set).
#
# Like add-hard-disk-drive.ps1, this script doesn't validate the
# generation/controller-type pairing -- Hyper-V's cmdlet errors
# clearly with "cannot attach IDE devices to a generation 2 virtual
# machine" if SCSI/IDE is paired with the wrong gen.

function Add-HypervVMDvdDrive {
    [CmdletBinding()]
    param(
        [Parameter(Mandatory)] [string] $Name,
        [Parameter(Mandatory)] [ValidateSet('SCSI', 'IDE')] [string] $ControllerType,
        [Parameter(Mandatory)] [int]    $ControllerNumber,
        [Parameter(Mandatory)] [int]    $ControllerLocation,
        [string]                        $Path
    )
    # Add-VMDvdDrive does NOT accept -ControllerType -- the controller
    # type is implicit from the VM's generation (gen 1 -> IDE, gen 2 ->
    # SCSI). The wire payload still carries controller_type for slot
    # identification on the Go side (so a future config that has both
    # SCSI and IDE DVDs on the same VM would have unique slot tuples)
    # but we don't pass it through to the cmdlet.
    $cmdletArgs = @{
        VMName             = $Name
        ControllerNumber   = $ControllerNumber
        ControllerLocation = $ControllerLocation
        ErrorAction        = 'Stop'
    }
    if ($PSBoundParameters.ContainsKey('Path') -and -not [string]::IsNullOrEmpty($Path)) {
        $cmdletArgs.Path = $Path
    }
    Add-VMDvdDrive @cmdletArgs | Out-Null
    @{} | Write-HypervResult
}

# Entry block. Skipped during Pester runs (dot-source sets InvocationName='.').
if ($MyInvocation.InvocationName -ne '.') {
    try {
        $params = [Console]::In.ReadToEnd() | ConvertFrom-Json
        $callArgs = @{
            Name               = $params.name
            ControllerType     = $params.controller_type
            ControllerNumber   = $params.controller_number
            ControllerLocation = $params.controller_location
        }
        # Treat the iso_path key as optional: a payload that omits it
        # entirely OR sets it to null both produce an empty DVD drive.
        # PSObject.Properties lookup is the strict-mode-safe way to
        # check for an absent key (direct $params.iso_path access
        # throws under Set-StrictMode -Version 3.0).
        $isoProp = $params.PSObject.Properties['iso_path']
        if ($null -ne $isoProp -and $null -ne $isoProp.Value -and -not [string]::IsNullOrEmpty($isoProp.Value)) {
            $callArgs.Path = $isoProp.Value
        }
        Add-HypervVMDvdDrive @callArgs
    }
    catch {
        Write-HypervError $_
        exit 1
    }
}
