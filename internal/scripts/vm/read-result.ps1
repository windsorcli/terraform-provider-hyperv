# vm/read-result.ps1 -- canonical Read-HypervVMResult function, prepended
# to every VM verb script that emits the read shape (get/new/set/set-state).
#
# Until 2026-04 this body lived inline in four separate scripts because the
# runtime concatenates preamble + a single verb script per call. The Go-side
# typed client (internal/hyperv/vm.go) now prepends this snippet alongside
# the preamble for the four read-emitting verbs, leaving one canonical copy
# the Pester get.Tests.ps1 contract test pins.
#
# Pester *.Tests.ps1 files dot-source this file in their BeforeAll so the
# function is in scope when the test exercises a verb script that calls
# Read-HypervVMResult.

# Read-HypervVMResult emits the canonical 14-field VM read shape consumed
# by the Go-side modelFromVM. The wire fields are deliberately PascalCase
# matched to the hyperv.VM Go struct's json tags, NOT to the schema's
# snake_case attribute names; modelFromVM does the snake_case translation.
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
            # MacAddress: emit only when the NIC has DynamicMacAddressEnabled
            # = false (i.e. user-set static MAC). Dynamic MACs come back as
            # whatever Hyper-V auto-assigned this boot, and surfacing that
            # as state would create a perpetual diff against an empty
            # config -- the resource layer treats empty string here as
            # "null state" so unset config matches unset state.
            $macAddress = if ($nic.DynamicMacAddressEnabled) { '' } else { [string] $nic.MacAddress }
            # VlanID: 0 means untagged, 1-4094 means access-mode VLAN.
            # Get-VMNetworkAdapterVlan exposes the active VLAN setting.
            # Trunk and isolation modes aren't yet supported on the
            # resource side; we emit the AccessVlanId regardless (a
            # trunk-mode NIC reports AccessVlanId=0, which the resource
            # layer surfaces as null -- correct in spirit since the user
            # didn't set vlan_id, even if Hyper-V has a different mode
            # configured out-of-band).
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
    # Memory dynamic fields come from Get-VMMemory -- the Get-VM object
    # only exposes MemoryStartup / MemoryAssigned, not
    # DynamicMemoryEnabled / Minimum / Maximum. When dynamic is off the
    # host still stores Minimum / Maximum (Hyper-V's defaults: 512MiB
    # min, 1TiB max), but the values aren't in effect, so we surface
    # null on the wire to keep state honest about what's actually
    # being managed. The Go decode into *int64 handles null cleanly.
    # -VM (not -VMName) skips the redundant name resolution; the
    # caller already handed us the resolved VM object.
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
        SnapshotFileLocation = $Vm.SnapshotFileLocation
        SmartPagingFilePath  = $Vm.SmartPagingFilePath
        AutomaticStartAction = $Vm.AutomaticStartAction.ToString()
        AutomaticStartDelay  = [int64] $Vm.AutomaticStartDelay
        AutomaticStopAction  = $Vm.AutomaticStopAction.ToString()
        CheckpointType       = $Vm.CheckpointType.ToString()
        SecureBootEnabled    = $secureBoot
        SecureBootTemplate   = $secureBootTemplate
        HardDiskDrives       = $hdds
        NetworkAdapters      = $nics
        DvdDrives            = $dvds
        BootOrder            = $bootOrder
    } | Write-HypervResult
}
