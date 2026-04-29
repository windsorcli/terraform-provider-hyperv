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

# Read-HypervVMResult emits the canonical 10-field shape. Inline duplicate
# of the same logic in new.ps1 / set.ps1 because the runtime concatenates
# only preamble + a single verb script per call (no cross-script helpers).
function Read-HypervVMResult {
    [CmdletBinding()]
    param(
        [Parameter(Mandatory)] $Vm
    )
    # SecureBoot and BootOrder both come from Get-VMFirmware, and
    # both are gen-2-only -- the cmdlet errors on gen 1 with "not
    # supported for the current configuration", which we don't want
    # to surface. Single fetch covers both fields when on gen 2.
    $secureBoot = $null
    $bootOrder  = @()
    if ($Vm.Generation -eq 2) {
        $firmware = Get-VMFirmware -VM $Vm -ErrorAction Stop
        $secureBoot = ($firmware.SecureBoot.ToString() -eq 'On')
        $bootOrder = @(
            foreach ($entry in $firmware.BootOrder) {
                # The Microsoft.HyperV.PowerShell.VMBootSourceType enum
                # only distinguishes Drive / Network / File / Unknown --
                # NOT HardDiskDrive vs DvdDrive (both surface as 'Drive').
                # The .NET type of $entry.Device is the real
                # discriminator: HardDiskDrive vs DvdDrive vs
                # VMNetworkAdapter. Verified empirically against
                # Server 2022 + PS 5.1 (2026-04 bench session).
                #
                # File-type entries (UEFI bootloader paths -- e.g.,
                # \EFI\BOOT\BOOTX64.EFI) and Unknown are silently
                # skipped: not yet in the schema, and emitting a
                # half-shaped record the Go side can't act on would
                # surface as a phantom diff every plan.
                $deviceType = $entry.Device.GetType().Name
                switch ($deviceType) {
                    'HardDiskDrive' {
                        [pscustomobject]@{
                            Type               = 'hard_disk_drive'
                            ControllerType     = $entry.Device.ControllerType.ToString()
                            ControllerNumber   = [int] $entry.Device.ControllerNumber
                            ControllerLocation = [int] $entry.Device.ControllerLocation
                            Name               = ''
                        }
                    }
                    'DvdDrive' {
                        [pscustomobject]@{
                            Type               = 'dvd_drive'
                            ControllerType     = $entry.Device.ControllerType.ToString()
                            ControllerNumber   = [int] $entry.Device.ControllerNumber
                            ControllerLocation = [int] $entry.Device.ControllerLocation
                            Name               = ''
                        }
                    }
                    'VMNetworkAdapter' {
                        [pscustomobject]@{
                            Type               = 'network_adapter'
                            ControllerType     = ''
                            ControllerNumber   = 0
                            ControllerLocation = 0
                            Name               = $entry.Device.Name
                        }
                    }
                }
            }
        )
    }
    # Hard-disk drives flow as an array even when empty -- ConvertTo-Json
    # serializes an empty PowerShell array to `[]` only when it's
    # explicitly typed as an array (the @() prefix below). Without that
    # cast a single-HDD case round-trips as a scalar object, breaking the
    # Go-side decode into []HardDiskDrive.
    $hdds = @(
        Get-VMHardDiskDrive -VM $Vm -ErrorAction Stop |
            Select-Object `
                @{ N = 'Path';               E = { $_.Path } },
                @{ N = 'ControllerType';     E = { $_.ControllerType.ToString() } },
                @{ N = 'ControllerNumber';   E = { [int] $_.ControllerNumber } },
                @{ N = 'ControllerLocation'; E = { [int] $_.ControllerLocation } }
    )
    # Network adapters: same @() wrapper rationale as HDDs -- empty
    # array on the wire becomes []NetworkAdapter on the Go side, not
    # nil, which keeps state stable when no NICs are attached.
    # NICs include IPAddresses so the resource layer can surface a
    # top-level ip_addresses flatten. Empty IPAddresses is the common
    # case (Off VM, or integration services not reporting yet).
    #
    # Direct pscustomobject construction (rather than Select-Object
    # with computed property) sidesteps a PS 5.1 ConvertTo-Json
    # quirk: an empty array inside a Select-Object computed-property
    # serializes as `{}` instead of `[]`, breaking the Go-side
    # decode into []string. Building the object directly preserves
    # the [string[]] cast through the JSON serializer.
    $nics = @(
        foreach ($nic in (Get-VMNetworkAdapter -VM $Vm -ErrorAction Stop)) {
            [pscustomobject]@{
                Name        = $nic.Name
                SwitchName  = $nic.SwitchName
                IPAddresses = [string[]] @($nic.IPAddresses)
            }
        }
    )
    # DVD drives: same shape as HardDiskDrives. An empty drive (no ISO
    # loaded) emits Path as the empty string, not null -- the cmdlet's
    # raw .Path property is "" in that case and we don't translate.
    $dvds = @(
        Get-VMDvdDrive -VM $Vm -ErrorAction Stop |
            Select-Object `
                @{ N = 'Path';               E = { if ($_.Path) { $_.Path } else { '' } } },
                @{ N = 'ControllerType';     E = { $_.ControllerType.ToString() } },
                @{ N = 'ControllerNumber';   E = { [int] $_.ControllerNumber } },
                @{ N = 'ControllerLocation'; E = { [int] $_.ControllerLocation } }
    )
    [pscustomobject]@{
        Name                = $Vm.Name
        Id                  = $Vm.Id.ToString()
        Generation          = [int] $Vm.Generation
        ProcessorCount      = [int] $Vm.ProcessorCount
        MemoryStartupBytes  = [int64] $Vm.MemoryStartup
        MemoryAssignedBytes = [int64] $Vm.MemoryAssigned
        State               = $Vm.State.ToString()
        Notes               = $Vm.Notes
        Path                = $Vm.Path
        SecureBootEnabled   = $secureBoot
        HardDiskDrives      = $hdds
        NetworkAdapters     = $nics
        DvdDrives           = $dvds
        BootOrder           = $bootOrder
    } | Write-HypervResult
}

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
        $params = [Console]::In.ReadToEnd() | ConvertFrom-Json
        Get-HypervVM -Name $params.name
    }
    catch {
        Write-HypervError $_
        exit 1
    }
}
