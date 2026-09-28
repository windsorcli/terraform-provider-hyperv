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
# Gen 2 (UEFI) only: each wire entry is resolved to its device handle
# via Get-VM* with a slot/name filter, then passed in wire order to
# Set-VMFirmware -BootOrder. The schema layer guards against gen 1 at
# plan time; the cmdlet's own error is the backstop. No pre-diff against
# Get-VMFirmware: the Go-side resource layer already diffs plan vs.
# state before calling this script, and a re-set is cheap regardless.
#
# lint:allow-long-comment

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

    # Preserve File/Unknown-type firmware entries (UEFI bootloader paths the schema doesn't model): Set-VMFirmware -BootOrder replaces the whole sequence, so omitting them would silently drop them.
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
        $params = Read-HypervStdinPayload
        Set-HypervVMBootOrder -Name $params.name -BootOrder $params.boot_order
    }
    catch {
        Write-HypervError $_
        exit 1
    }
}
