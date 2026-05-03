# _test_helpers.ps1 -- shared Pester setup for the vm verb scripts.
# Underscore prefix keeps it out of Pester's *.Tests.ps1 discovery glob.
#
# Stubs for the Hyper-V cmdlets the vm scripts call. Same rationale as
# vswitch's test helper: when the real Hyper-V module is loaded, its
# parameter sets impose constraints that drop bound values during Pester
# mock interactions on PS 5.1. Stub functions with simple parameter sets
# sidestep that.
#
# In production scripts run via -EncodedCommand in a fresh runspace, so the
# real cmdlets are still used; this shadow only applies to test execution.

function Get-VM {
    [CmdletBinding()]
    param(
        [Parameter(Position = 0)] [string] $Name
    )
}

function New-VM {
    [CmdletBinding()]
    param(
        [string] $Name,
        [int]    $Generation,
        [int64]  $MemoryStartupBytes,
        [string] $BootDevice,
        [switch] $NoVHD
    )
}

function Set-VM {
    [CmdletBinding()]
    param(
        [string] $Name,
        [string] $Notes
    )
}

function Set-VMMemory {
    [CmdletBinding()]
    param(
        [string] $VMName,
        [bool]   $DynamicMemoryEnabled,
        [int64]  $StartupBytes,
        [int64]  $MinimumBytes,
        [int64]  $MaximumBytes
    )
}

function Get-VMMemory {
    [CmdletBinding()]
    param(
        [string] $VMName,
        [Parameter(Position = 0)] $VM
    )
}

function Set-VMProcessor {
    [CmdletBinding()]
    param(
        [string] $VMName,
        [int]    $Count
    )
}

function Set-VMFirmware {
    [CmdletBinding()]
    param(
        [string]   $VMName,
        [string]   $EnableSecureBoot,
        [object[]] $BootOrder
    )
}

function Get-VMFirmware {
    [CmdletBinding()]
    param(
        [Parameter(Position = 0)] $VM,
        [string] $VMName
    )
}

function Stop-VM {
    [CmdletBinding()]
    param(
        [Parameter(Position = 0)] $VM,
        [string] $Name,
        [switch] $Force,
        [switch] $TurnOff
    )
}

function Start-VM {
    [CmdletBinding()]
    param(
        [Parameter(Position = 0)] $VM,
        [string] $Name
    )
}

function Remove-VM {
    [CmdletBinding()]
    param(
        [string] $Name,
        [switch] $Force
    )
}

function Get-VMHardDiskDrive {
    [CmdletBinding()]
    param(
        [Parameter(Position = 0)] $VM,
        [string] $VMName,
        [string] $ControllerType,
        [int]    $ControllerNumber,
        [int]    $ControllerLocation
    )
}

function Add-VMHardDiskDrive {
    [CmdletBinding()]
    param(
        [string] $VMName,
        [string] $ControllerType,
        [int]    $ControllerNumber,
        [int]    $ControllerLocation,
        [string] $Path
    )
}

function Remove-VMHardDiskDrive {
    [CmdletBinding()]
    param(
        [string] $VMName,
        [string] $ControllerType,
        [int]    $ControllerNumber,
        [int]    $ControllerLocation
    )
}

function Get-VMNetworkAdapter {
    [CmdletBinding()]
    param(
        [Parameter(Position = 0)] $VM,
        [string] $VMName,
        [string] $Name
    )
}

function Add-VMNetworkAdapter {
    [CmdletBinding()]
    param(
        [string] $VMName,
        [string] $Name,
        [string] $SwitchName,
        [string] $StaticMacAddress
    )
}

function Remove-VMNetworkAdapter {
    [CmdletBinding()]
    param(
        [string] $VMName,
        [string] $Name
    )
}

function Set-VMNetworkAdapterVlan {
    [CmdletBinding()]
    param(
        [string]   $VMName,
        [string]   $VMNetworkAdapterName,
        $VMNetworkAdapter,
        [switch]   $Access,
        [switch]   $Untagged,
        [int]      $VlanId
    )
}

function Get-VMNetworkAdapterVlan {
    [CmdletBinding()]
    param(
        [Parameter(Position = 0)] $VMNetworkAdapter,
        [string] $VMName,
        [string] $VMNetworkAdapterName
    )
}

function Get-VMDvdDrive {
    [CmdletBinding()]
    param(
        [Parameter(Position = 0)] $VM,
        [string] $VMName,
        [string] $ControllerType,
        [int]    $ControllerNumber,
        [int]    $ControllerLocation
    )
}

function Add-VMDvdDrive {
    [CmdletBinding()]
    param(
        [string] $VMName,
        [string] $ControllerType,
        [int]    $ControllerNumber,
        [int]    $ControllerLocation,
        [string] $Path
    )
}

function Remove-VMDvdDrive {
    [CmdletBinding()]
    param(
        [string] $VMName,
        [int]    $ControllerNumber,
        [int]    $ControllerLocation
    )
}

# New-HypervVMSample builds a Get-VM-shaped object for use as the canned
# return value from Mock blocks. Defaults model a typical small gen 2 VM;
# per-test overrides cover gen 1, larger sizing, running state, etc.
function New-HypervVMSample {
    [CmdletBinding()]
    param(
        [string] $Name           = 'sample-vm',
        [string] $Id             = '12345678-1234-5678-1234-567812345678',
        [int]    $Generation     = 2,
        [int]    $ProcessorCount = 2,
        [int64]  $MemoryStartup  = 4294967296,    # 4 GiB
        [int64]  $MemoryAssigned = 4294967296,
        [string] $State          = 'Off',
        [string] $Notes          = '',
        [string] $Path           = 'C:\ProgramData\Microsoft\Windows\Hyper-V\Virtual Machines'
    )
    [pscustomobject]@{
        Name           = $Name
        Id             = [guid] $Id
        Generation     = $Generation
        ProcessorCount = $ProcessorCount
        MemoryStartup  = $MemoryStartup
        MemoryAssigned = $MemoryAssigned
        State          = $State
        Notes          = $Notes
        Path           = $Path
    }
}

# New-HypervVMMemorySample builds a Get-VMMemory-shaped object for use in
# Mock blocks. The read shape pulls DynamicMemoryEnabled / Minimum /
# Maximum off this object; tests that exercise the static-only path can
# leave the defaults (DynamicMemoryEnabled=$false; Hyper-V's default
# legacy Minimum=512MiB / Maximum=1TiB are preserved on the cmdlet but
# ignored by the read-back when DynamicMemoryEnabled is false).
function New-HypervVMMemorySample {
    [CmdletBinding()]
    param(
        [bool]  $DynamicMemoryEnabled = $false,
        [int64] $Startup               = 4294967296,    # 4 GiB
        [int64] $Minimum               = 536870912,     # 512 MiB (Hyper-V default)
        [int64] $Maximum               = 1099511627776  # 1 TiB (Hyper-V default)
    )
    [pscustomobject]@{
        DynamicMemoryEnabled = $DynamicMemoryEnabled
        Startup              = $Startup
        Minimum              = $Minimum
        Maximum              = $Maximum
    }
}

# New-HypervVMFirmwareSample builds a Get-VMFirmware-shaped object for use
# in Mock blocks. SecureBoot and BootOrder are the fields the read shape
# consumes; BootOrder defaults to an empty array (gen 2 with default boot
# order would normally have entries, but tests that don't care about the
# field can leave it empty).
function New-HypervVMFirmwareSample {
    [CmdletBinding()]
    param(
        [string]   $SecureBoot = 'On',  # 'On' | 'Off'
        [object[]] $BootOrder  = @()
    )
    [pscustomobject]@{
        SecureBoot = $SecureBoot
        BootOrder  = $BootOrder
    }
}

# New-HypervVMBootOrderEntrySample builds a VMComponentObject-shaped
# pscustomobject for use in Mock blocks. The DeviceType parameter
# names a CLR type that the production scripts dispatch on (verified
# against Server 2022 + PS 5.1: $entry.BootType is the high-level
# category 'Drive'/'Network', NOT the storage subtype, so Device's
# .GetType().Name is the load-bearing discriminator).
#
# Valid DeviceType values:
#   'HardDiskDrive'    -> emits a Device with ControllerType / Number / Location
#   'DvdDrive'         -> ditto
#   'VMNetworkAdapter' -> emits a Device with Name
#
# The Device's CLR type name is set via PSObject.TypeNames.Insert
# so the script's $entry.Device.GetType().Name pseudo-test matches
# what the real cmdlet emits. (PSObject doesn't actually change the
# CLR type, but PowerShell's switch on .GetType().Name reads the
# inserted type name; the production switch behaves identically.)
function New-HypervVMBootOrderEntrySample {
    [CmdletBinding()]
    param(
        [Parameter(Mandatory)]
        [ValidateSet('HardDiskDrive', 'DvdDrive', 'VMNetworkAdapter')]
        [string] $DeviceType,
        [string] $ControllerType     = 'SCSI',
        [int]    $ControllerNumber   = 0,
        [int]    $ControllerLocation = 0,
        [string] $Name               = 'primary'
    )
    $device = switch ($DeviceType) {
        'HardDiskDrive' {
            New-Object psobject -Property @{
                ControllerType     = $ControllerType
                ControllerNumber   = $ControllerNumber
                ControllerLocation = $ControllerLocation
            }
        }
        'DvdDrive' {
            New-Object psobject -Property @{
                ControllerType     = $ControllerType
                ControllerNumber   = $ControllerNumber
                ControllerLocation = $ControllerLocation
            }
        }
        'VMNetworkAdapter' {
            New-Object psobject -Property @{ Name = $Name }
        }
    }
    # Replace the underlying CLR type name surfaced by GetType().Name
    # with the simulated subtype. The script never inspects the
    # full type name, only .Name -- so this is sufficient.
    $device.PSObject.TypeNames.Insert(0, $DeviceType)
    # Override GetType to return an object whose .Name is the
    # simulated CLR type. PSObject.TypeNames affects -is, not
    # GetType(); production-script does $entry.Device.GetType().Name,
    # so we add a ScriptMethod that shadows it.
    $device | Add-Member -MemberType ScriptMethod -Name GetType -Force -Value ([scriptblock]::Create("[pscustomobject]@{ Name = '$DeviceType' }"))
    [pscustomobject]@{
        BootType = 'Drive'
        Device   = $device
    }
}

# New-HypervVMHardDiskDriveSample builds a Get-VMHardDiskDrive-shaped object
# for use in Mock blocks. ControllerType is a string (the cmdlet emits an
# enum that has a friendly .ToString() of "SCSI" or "IDE"; the test stubs
# can skip the enum machinery by stringifying directly).
function New-HypervVMHardDiskDriveSample {
    [CmdletBinding()]
    param(
        [string] $Path               = 'C:\hyperv\vhds\sample.vhdx',
        [string] $ControllerType     = 'SCSI',
        [int]    $ControllerNumber   = 0,
        [int]    $ControllerLocation = 0
    )
    [pscustomobject]@{
        Path               = $Path
        ControllerType     = $ControllerType
        ControllerNumber   = $ControllerNumber
        ControllerLocation = $ControllerLocation
    }
}

# New-HypervVMNetworkAdapterSample builds a Get-VMNetworkAdapter-shaped
# object for use in Mock blocks. Mirrors what the read script consumes:
# Name + SwitchName for slot identification + binding, IPAddresses for
# the top-level / per-NIC ip_addresses flatten, MacAddress +
# DynamicMacAddressEnabled for the per-NIC mac_address surface.
# Defaults match a typical "fresh NIC, no static MAC, no IPs reported
# yet" -- DynamicMacAddressEnabled = true so the read script emits an
# empty mac_address (which the resource layer translates to null).
function New-HypervVMNetworkAdapterSample {
    [CmdletBinding()]
    param(
        [string]   $Name                       = 'primary',
        [string]   $SwitchName                 = 'lab-internal',
        [string[]] $IPAddresses                = @(),
        [string]   $MacAddress                 = '00155D000000',
        [bool]     $DynamicMacAddressEnabled   = $true
    )
    [pscustomobject]@{
        Name                     = $Name
        SwitchName               = $SwitchName
        IPAddresses              = $IPAddresses
        MacAddress               = $MacAddress
        DynamicMacAddressEnabled = $DynamicMacAddressEnabled
    }
}

# New-HypervVMNetworkAdapterVlanSample builds a
# Get-VMNetworkAdapterVlan-shaped object. OperationMode 'Untagged' /
# AccessVlanId 0 is the "no VLAN" default the read script translates to
# state value null on the resource side.
function New-HypervVMNetworkAdapterVlanSample {
    [CmdletBinding()]
    param(
        [string] $OperationMode = 'Untagged',
        [int]    $AccessVlanId  = 0
    )
    [pscustomobject]@{
        OperationMode = $OperationMode
        AccessVlanId  = $AccessVlanId
    }
}
