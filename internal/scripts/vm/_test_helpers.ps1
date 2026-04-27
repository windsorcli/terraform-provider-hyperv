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
        [int64]  $StartupBytes
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
        [string] $VMName,
        [string] $EnableSecureBoot
    )
}

function Get-VMFirmware {
    [CmdletBinding()]
    param(
        [Parameter(Position = 0)] $VM
    )
}

function Stop-VM {
    [CmdletBinding()]
    param(
        [string] $Name,
        [switch] $Force,
        [switch] $TurnOff
    )
}

function Remove-VM {
    [CmdletBinding()]
    param(
        [string] $Name,
        [switch] $Force
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

# New-HypervVMFirmwareSample builds a Get-VMFirmware-shaped object for use
# in Mock blocks. SecureBoot is the only field the read shape consumes.
function New-HypervVMFirmwareSample {
    [CmdletBinding()]
    param(
        [string] $SecureBoot = 'On'   # 'On' | 'Off'
    )
    [pscustomobject]@{
        SecureBoot = $SecureBoot
    }
}
