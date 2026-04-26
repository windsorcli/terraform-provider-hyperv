# _test_helpers.ps1 -- shared Pester setup for the vswitch verb scripts.
# Underscore prefix keeps it out of Pester's *.Tests.ps1 discovery glob.
#
# Pester's Mock requires the command being mocked to exist in scope. On a
# Windows host with Hyper-V loaded, Get-VMSwitch / New-VMSwitch / etc. are
# already defined by the Hyper-V module. On a Linux/macOS dev box (where we
# still want fast contract tests) those cmdlets are absent, so we stub them
# with `[CmdletBinding()]` shells that match the real cmdlets' relevant param
# surface. The stub bodies are intentionally empty -- Pester's Mock replaces
# them in each It block.

if (-not (Get-Command -Name Get-VMSwitch -ErrorAction SilentlyContinue)) {
    function Get-VMSwitch {
        [CmdletBinding()]
        param(
            [Parameter(Position = 0)] [string] $Name
        )
    }
}

if (-not (Get-Command -Name New-VMSwitch -ErrorAction SilentlyContinue)) {
    function New-VMSwitch {
        [CmdletBinding()]
        param(
            [string]   $Name,
            [string]   $SwitchType,
            [string[]] $NetAdapterName,
            [bool]     $AllowManagementOS,
            [string]   $Notes
        )
    }
}

if (-not (Get-Command -Name Set-VMSwitch -ErrorAction SilentlyContinue)) {
    function Set-VMSwitch {
        [CmdletBinding()]
        param(
            [string]   $Name,
            [string[]] $NetAdapterName,
            [bool]     $AllowManagementOS,
            [string]   $Notes
        )
    }
}

if (-not (Get-Command -Name Remove-VMSwitch -ErrorAction SilentlyContinue)) {
    function Remove-VMSwitch {
        [CmdletBinding()]
        param(
            [string] $Name,
            [switch] $Force
        )
    }
}

# New-HypervSwitchSample builds a PSCustomObject shaped like a real
# Get-VMSwitch result, used as the canned return value from Mock blocks. The
# shape mirrors what spike #2 captured on Server 2022 + PS 5.1.
function New-HypervSwitchSample {
    [CmdletBinding()]
    param(
        [string] $Name = 'sw0',
        [string] $SwitchType = 'External',
        [bool]   $AllowManagementOS = $true,
        [string] $NetAdapterInterfaceDescription = 'Intel(R) Ethernet I210',
        [string] $Notes = '',
        [string] $Id = '12345678-1234-5678-1234-567812345678'
    )
    [pscustomobject]@{
        Name                           = $Name
        SwitchType                     = $SwitchType
        AllowManagementOS              = $AllowManagementOS
        NetAdapterInterfaceDescription = $NetAdapterInterfaceDescription
        Notes                          = $Notes
        Id                             = [guid]$Id
    }
}
