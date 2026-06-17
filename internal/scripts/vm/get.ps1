# vm/get.ps1 -- read a VM's metadata.
#
# Wire contract (locked in by Tests.ps1):
#
#   stdin JSON  : { "name": "<vm-name>" }
#   stdout JSON : {
#                   "Name":                       "<string>",
#                   "Id":                         "<guid>",
#                   "Generation":                 1|2,
#                   "ProcessorCount":             <int>,
#                   "MemoryStartupBytes":         <int64>,
#                   "MemoryAssignedBytes":        <int64>,
#                   "State":                      "Off"|"Running"|"Saved"|"Paused"|...,
#                   "Notes":                      "<string>",
#                   "Path":                       "<vm-config-dir>",
#                   "SecureBootEnabled":          <bool>|null,    # null on gen 1
#                   "HardDiskDrives":             [
#                     { "Path":               "<absolute-path>",
#                       "ControllerType":     "SCSI"|"IDE",
#                       "ControllerNumber":   <int>,
#                       "ControllerLocation": <int> },
#                     ...
#                   ],
#                   "NetworkAdapters":            [
#                     { "Name":        "<display-name>",
#                       "SwitchName":  "<vswitch-name>",
#                       "IPAddresses": ["<ip>", ...] },   # empty when VM is Off
#                                                         # or integration services
#                                                         # haven't reported in.
#                     ...
#                   ],
#                   "DvdDrives":                  [
#                     { "Path":               "<absolute-path>" | "",
#                       "ControllerType":     "SCSI"|"IDE",
#                       "ControllerNumber":   <int>,
#                       "ControllerLocation": <int> },
#                     ...
#                   ],
#                   "BootOrder":                  [
#                     # gen 2 only; always [] on gen 1.
#                     # Discriminated by Type: hard_disk_drive / dvd_drive
#                     # entries carry ControllerType / Number / Location;
#                     # network_adapter entries carry Name. Unused fields
#                     # are emitted as zero values (Go decodes via the
#                     # type discriminator).
#                     { "Type":               "hard_disk_drive"|"dvd_drive"|"network_adapter",
#                       "ControllerType":     "SCSI"|"IDE"|"",
#                       "ControllerNumber":   <int>,
#                       "ControllerLocation": <int>,
#                       "Name":               "<nic-name>"|"" },
#                     ...
#                   ]
#                 }
#   stderr/exit : missing VM -> Write-HypervError envelope with
#                 category=ObjectNotFound + exit 1, mapped to ErrNotFound on
#                 the Go side so resource Read calls RemoveResource.
#
# boot_order is gen-2-only in this slice. Gen 1 (BIOS StartupOrder, a
# 4-string enum from {CD, IDEHardDrive, LegacyNetworkAdapter, Floppy})
# is deferred to a follow-up; the schema validator rejects boot_order
# on gen 1 at plan time.


# Get-HypervVM fetches a VM by name. Same Stop + selective ObjectNotFound
# catch pattern as vswitch/get.ps1 -- a missing VM raises ObjectNotFound
# (mapped to ErrNotFound -> RemoveResource on the Go side); other errors
# (ResourceUnavailable, PermissionDenied) propagate untouched.
function Get-HypervVM {
    [CmdletBinding()]
    param(
        [Parameter(Mandatory)] [string] $Name
    )
    try {
        $vm = Get-VM -Name $Name -ErrorAction Stop
    }
    catch {
        # "VM missing" surfaces in two shapes (mirror of the
        # vswitch/get.ps1 fix from the M1d acc-test PR):
        #   1. CategoryInfo.Category = ObjectNotFound -- the documented
        #      contract; what some Hyper-V module versions emit.
        #   2. CategoryInfo.Category = InvalidArgument with
        #      FullyQualifiedErrorId =
        #      'InvalidParameter,Microsoft.HyperV.PowerShell.Commands.GetVM'
        #      -- what Get-VM actually emits on Server 2022 + PS 5.1
        #      (verified 2026-04 against a real bench; the acc test
        #      for hyperv_vm's CheckDestroy caught this).
        $isMissing = (
            $_.CategoryInfo.Category -eq [System.Management.Automation.ErrorCategory]::ObjectNotFound
        ) -or (
            $_.FullyQualifiedErrorId -eq 'InvalidParameter,Microsoft.HyperV.PowerShell.Commands.GetVM'
        )
        if (-not $isMissing) {
            throw
        }
        $exception = [System.Management.Automation.ItemNotFoundException]::new(
            "Hyper-V was unable to find a VM with name '$Name'.")
        $errorRecord = [System.Management.Automation.ErrorRecord]::new(
            $exception, 'VMNotFound',
            [System.Management.Automation.ErrorCategory]::ObjectNotFound, $Name)
        throw $errorRecord
    }
    Read-HypervVMResult -Vm $vm
}

# Entry block. Skipped during Pester runs (dot-source sets InvocationName='.').
if ($MyInvocation.InvocationName -ne '.') {
    try {
        $params = Read-HypervStdinParams
        Get-HypervVM -Name $params.name
    }
    catch {
        Write-HypervError $_
        exit 1
    }
}
