# vm/set-boot-order.ps1 -- replace the boot order on a gen 2 VM.
#
# Wire contract (locked in by Tests.ps1):
#
#   stdin JSON  : {
#                   "name":       "<vm-name>",
#                   "boot_order": [
#                     { "type": "dvd_drive" | "hard_disk_drive",
#                       "controller_type":     "SCSI" | "IDE",
#                       "controller_number":   <int>,
#                       "controller_location": <int> },
#                     { "type": "network_adapter",
#                       "name": "<nic-name>" },
#                     ...
#                   ]
#                 }
#   stdout JSON : {} on success.
#   stderr/exit : missing VM or device ref -> ObjectNotFound -> ErrNotFound.
#                 Cmdlet errors (e.g., empty BootOrder, gen 1 VM) -> the
#                 cmdlet's category, surfaced via Write-HypervError.
#
# Gen 2 (UEFI) only: Set-VMFirmware -BootOrder takes VMComponentObject[].
# Each wire entry is resolved to its actual device handle via Get-VM*
# with the slot/name filter, and the resolved devices are passed in
# wire order to Set-VMFirmware. The schema layer guards against gen 1
# at plan time; the cmdlet's "this command cannot be run on a
# generation 1 virtual machine" error is the backstop if it ever
# reaches us anyway.
#
# Why this script doesn't loop over Get-VMFirmware first to diff
# vs current: idempotent re-set is cheap (Set-VMFirmware is a single
# config write) and the Go-side resource layer already does the
# plan-vs-state diff before deciding to call us. A second diff here
# would be redundant.

# Resolve-HypervVMBootDevice maps a single wire entry to the underlying
# device handle Set-VMFirmware -BootOrder expects. Helper kept separate
# from the dispatch so the switch in the parent function stays terse.
function Resolve-HypervVMBootDevice {
    [CmdletBinding()]
    param(
        [Parameter(Mandatory)] [string] $VMName,
        [Parameter(Mandatory)]          $Entry
    )
    # Get-VMHardDiskDrive's parameter sets on Server 2022 + PS 5.1
    # don't accept the (-VMName, -ControllerType, -ControllerNumber,
    # -ControllerLocation) combination cleanly -- PowerShell
    # complains "A parameter cannot be found that matches parameter
    # name 'ControllerType'" because the parameter-set resolver
    # picks a set without it. Fetch all attachments for the VM and
    # filter in-script: same effective lookup, no parameter-set
    # ambiguity, and only N-of-N HDDs are scanned (always tiny on a
    # real VM).
    switch ($Entry.type) {
        'hard_disk_drive' {
            $match = Get-VMHardDiskDrive -VMName $VMName -ErrorAction Stop |
                Where-Object {
                    $_.ControllerType.ToString() -eq $Entry.controller_type -and
                    [int] $_.ControllerNumber   -eq [int] $Entry.controller_number -and
                    [int] $_.ControllerLocation -eq [int] $Entry.controller_location
                } | Select-Object -First 1
            if (-not $match) {
                throw "boot_order references hard_disk_drive at $($Entry.controller_type) $($Entry.controller_number):$($Entry.controller_location), but no such drive is attached to '$VMName'."
            }
            return $match
        }
        'dvd_drive' {
            $match = Get-VMDvdDrive -VMName $VMName -ErrorAction Stop |
                Where-Object {
                    $_.ControllerType.ToString() -eq $Entry.controller_type -and
                    [int] $_.ControllerNumber   -eq [int] $Entry.controller_number -and
                    [int] $_.ControllerLocation -eq [int] $Entry.controller_location
                } | Select-Object -First 1
            if (-not $match) {
                throw "boot_order references dvd_drive at $($Entry.controller_type) $($Entry.controller_number):$($Entry.controller_location), but no such drive is attached to '$VMName'."
            }
            return $match
        }
        'network_adapter' {
            return Get-VMNetworkAdapter -VMName $VMName `
                -Name $Entry.name `
                -ErrorAction Stop
        }
        default {
            throw "Unsupported boot_order entry type: '$($Entry.type)' (expected 'hard_disk_drive', 'dvd_drive', or 'network_adapter')."
        }
    }
}

function Set-HypervVMBootOrder {
    [CmdletBinding()]
    param(
        [Parameter(Mandatory)] [string]   $Name,
        [Parameter(Mandatory)] [object[]] $BootOrder
    )
    $devices = foreach ($entry in $BootOrder) {
        Resolve-HypervVMBootDevice -VMName $Name -Entry $entry
    }

    # Preserve File-type and Unknown-type firmware entries the schema
    # doesn't model: UEFI bootloader paths (e.g. \EFI\BOOT\BOOTX64.EFI)
    # that Hyper-V or the guest OS registers on first boot.
    # Set-VMFirmware -BootOrder REPLACES the entire firmware boot
    # sequence -- anything not in the list is removed -- so without
    # this readback the first apply on a VM that has booted would
    # silently drop those entries. Hyper-V may recreate a default EFI
    # loader on next boot in some configurations, but the behavior is
    # implementation-specific; we preserve explicitly. Drive-type
    # ('Drive', 'Network') entries are NOT preserved here -- the
    # user's declared boot_order is the source of truth for those.
    $preserved = @()
    $firmware = Get-VMFirmware -VMName $Name -ErrorAction Stop
    if ($firmware -and $firmware.BootOrder) {
        $preserved = @($firmware.BootOrder | Where-Object {
            $_.BootType -eq 'File' -or $_.BootType -eq 'Unknown'
        })
    }

    $finalOrder = @()
    if ($devices)   { $finalOrder += @($devices) }
    if ($preserved) { $finalOrder += $preserved }

    Set-VMFirmware -VMName $Name -BootOrder $finalOrder -ErrorAction Stop
    @{} | Write-HypervResult
}

# Entry block. Skipped during Pester runs (dot-source sets InvocationName='.').
if ($MyInvocation.InvocationName -ne '.') {
    try {
        $params = Read-HypervStdinParams
        Set-HypervVMBootOrder -Name $params.name -BootOrder $params.boot_order
    }
    catch {
        Write-HypervError $_
        exit 1
    }
}
