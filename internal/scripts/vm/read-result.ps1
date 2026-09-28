# vm/read-result.ps1 -- canonical Read-HypervVMResult function, prepended
# by the Go-side typed client to every VM verb script that emits the read
# format (get/new/set/set-state). Pester *.Tests.ps1 files dot-source this
# file in their BeforeAll for the same reason.

# Resolve-HypervCheckpointBasePath walks an .avhd/.avhdx checkpoint
# differencing disk back to its base VHD: while a checkpoint exists,
# Get-VMHardDiskDrive reports that leaf instead of the attached config
# path, which fails Terraform's apply-consistency check. A user-managed
# .vhdx differencing disk is untouched by the extension check below.
function Resolve-HypervCheckpointBasePath {
    [CmdletBinding()]
    param(
        [Parameter(Mandatory)] [string] $Path
    )
    $current = $Path
    # 32 is a generous cap against a cyclic or corrupt parent chain --
    # real checkpoint chains are nowhere near this deep.
    for ($depth = 0; $depth -lt 32; $depth++) {
        if ($current -notmatch '\.avhdx?$') {
            return $current
        }
        $parent = (Get-VHD -Path $current -ErrorAction Stop).ParentPath
        if (-not $parent) {
            return $current
        }
        $current = $parent
    }
    throw "Resolve-HypervCheckpointBasePath: '$Path' did not resolve to a base disk within 32 hops -- likely a cyclic or corrupt checkpoint chain."
}

# Read-HypervVMResult emits the canonical 14-field VM read format consumed
# by the Go-side modelFromVM, PascalCase matched to the hyperv.VM Go
# struct's json tags; modelFromVM does the snake_case translation.
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
    $secureBootTemplate = ''
    $bootOrder  = @()
    if ($Vm.Generation -eq 2) {
        $firmware = Get-VMFirmware -VM $Vm -ErrorAction Stop
        $secureBoot = ($firmware.SecureBoot.ToString() -eq 'On')
        # Template is meaningful only on gen 2; emit empty string on
        # gen 1 so the Go-side decode collapses to types.StringNull().
        $secureBootTemplate = [string] $firmware.SecureBootTemplate
        $bootOrder = @(
            foreach ($entry in $firmware.BootOrder) {
                # BootSourceType only distinguishes Drive/Network/File/Unknown, not HardDiskDrive vs DvdDrive; $entry.Device's .NET type is the real discriminator. A null Device (File/Unknown entries, or a removed device) is skipped: not modeled in the schema.
                if ($null -eq $entry.Device) {
                    continue
                }
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
    # @() keeps this array-typed for ConvertTo-Json; a foreach loop, not Select-Object, so a Resolve-HypervCheckpointBasePath exception propagates instead of being swallowed.
    $hdds = @(
        foreach ($hdd in (Get-VMHardDiskDrive -VM $Vm -ErrorAction Stop)) {
            [pscustomobject]@{
                Path               = Resolve-HypervCheckpointBasePath -Path $hdd.Path
                ControllerType     = $hdd.ControllerType.ToString()
                ControllerNumber   = [int] $hdd.ControllerNumber
                ControllerLocation = [int] $hdd.ControllerLocation
            }
        }
    )
    # Same @() rationale as HDDs. Direct pscustomobject construction, not Select-Object with a computed property, sidesteps a PS 5.1 quirk where an empty array inside that property serializes to `{}` instead of `[]`.
    $nics = @(
        foreach ($nic in (Get-VMNetworkAdapter -VM $Vm -ErrorAction Stop)) {
            # Empty unless DynamicMacAddressEnabled=false (user-set static MAC); a dynamic MAC would otherwise create a perpetual diff.
            $macAddress = if ($nic.DynamicMacAddressEnabled) { '' } else { [string] $nic.MacAddress }
            # AccessVlanId regardless of mode: trunk/isolation aren't yet supported, and a trunk NIC's AccessVlanId=0 correctly reads as unset.
            $vlanID = 0
            $vlanInfo = Get-VMNetworkAdapterVlan -VMNetworkAdapter $nic -ErrorAction Stop
            if ($vlanInfo -and $vlanInfo.OperationMode -eq 'Access') {
                $vlanID = [int] $vlanInfo.AccessVlanId
            }
            [pscustomobject]@{
                Name        = $nic.Name
                SwitchName  = $nic.SwitchName
                IPAddresses = [string[]] @($nic.IPAddresses)
                MacAddress  = $macAddress
                VlanID      = $vlanID
            }
        }
    )
    # DVD drives: same format as HardDiskDrives; an empty drive emits Path as "" (the cmdlet's own raw value), not null.
    $dvds = @(
        Get-VMDvdDrive -VM $Vm -ErrorAction Stop |
            Select-Object `
                @{ N = 'Path';               E = { if ($_.Path) { $_.Path } else { '' } } },
                @{ N = 'ControllerType';     E = { $_.ControllerType.ToString() } },
                @{ N = 'ControllerNumber';   E = { [int] $_.ControllerNumber } },
                @{ N = 'ControllerLocation'; E = { [int] $_.ControllerLocation } }
    )
    # Get-VM exposes only MemoryStartup/MemoryAssigned; min/max come from Get-VMMemory and surface as null when dynamic is off, even though Hyper-V still stores stale defaults for them.
    $mem = Get-VMMemory -VM $Vm -ErrorAction Stop
    $memoryDynamicEnabled = [bool] $mem.DynamicMemoryEnabled
    $memoryMinimumBytes   = if ($memoryDynamicEnabled) { [int64] $mem.Minimum } else { $null }
    $memoryMaximumBytes   = if ($memoryDynamicEnabled) { [int64] $mem.Maximum } else { $null }

    [pscustomobject]@{
        Name                 = $Vm.Name
        Id                   = $Vm.Id.ToString()
        Generation           = [int] $Vm.Generation
        ProcessorCount       = [int] $Vm.ProcessorCount
        MemoryStartupBytes   = [int64] $Vm.MemoryStartup
        MemoryAssignedBytes  = [int64] $Vm.MemoryAssigned
        MemoryDynamicEnabled = $memoryDynamicEnabled
        MemoryMinimumBytes   = $memoryMinimumBytes
        MemoryMaximumBytes   = $memoryMaximumBytes
        State                = $Vm.State.ToString()
        Notes                = $Vm.Notes
        Path                 = $Vm.Path
        SecureBootEnabled    = $secureBoot
        SecureBootTemplate   = $secureBootTemplate
        HardDiskDrives       = $hdds
        NetworkAdapters      = $nics
        DvdDrives            = $dvds
        BootOrder            = $bootOrder
    } | Write-HypervResult
}
