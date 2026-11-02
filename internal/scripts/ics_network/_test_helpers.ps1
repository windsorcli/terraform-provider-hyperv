# _test_helpers.ps1 -- shared Pester setup for the ics_network verb scripts.
# Stubs the Windows-only cmdlets so the suites run on Linux pwsh as well.

function Get-VMSwitch {
    [CmdletBinding()]
    param(
        [string] $Id
    )
}

# HcnQueryNetworkProperties output for an ICS network as the bench returned it, trimmed to the fields read.
$script:HypervIcsPropertiesJson = '{"ID":"753900d5-83fd-4c4d-83b6-7c438c6ff4fe","Name":"tfdev-ics","Health":{"Extra":{"Resources":{"Allocators":[{"IsolationId":1,"SwitchId":"EDF5D73E-D390-472B-968F-4E60AC3EE1BE","Tag":"Host Vnic"},{"Prefix":24,"SubnetIPAddress":"172.30.97.0","Tag":"ICS"}]}}},"SchemaVersion":{"Major":2,"Minor":0},"Type":"ICS","Ipams":[{"Subnets":[{"IpAddressPrefix":"172.30.97.0/24","Policies":[{"Type":"VLAN","Settings":{"IsolationId":1}}],"Routes":[{"NextHop":"172.30.97.1","DestinationPrefix":"0.0.0.0/0"}]}]}],"Flags":11}'

# Error records as HCN returned them on the bench.
$script:HypervHcnNotFoundError = '{"Success":false,"Error":"The network was not found. ","ErrorCode":2151350273}'
$script:HypervHcnDuplicateNameError = '{"Success":false,"Error":"A network with this name already exists. ","ErrorCode":2151350288}'

# PowerShell parses these hex literals as the negative Int32 values the P/Invoke calls return.
$script:HypervHrNetworkNotFound = 0x803B0001
$script:HypervHrElementNotFound = 0x80070490
$script:HypervHrDuplicateName = 0x803B0010
$script:HypervHrServiceMissing = 0x800706D9

# New-HypervHcnQueryResult models Invoke-HypervHcnQuery's return value.
function New-HypervHcnQueryResult {
    [CmdletBinding()]
    param(
        [int]    $HResult = 0,
        [string] $Operation = 'query',
        [string] $Result = $script:HypervIcsPropertiesJson,
        [string] $ErrorRecord = $null
    )
    [pscustomobject]@{ HResult = $HResult; Operation = $Operation; Result = $Result; ErrorRecord = $ErrorRecord }
}

# New-HypervHcnCallResult models the return value of Invoke-HypervHcnCreate and Invoke-HypervHcnDelete.
function New-HypervHcnCallResult {
    [CmdletBinding()]
    param(
        [int]    $HResult = 0,
        [string] $ErrorRecord = $null
    )
    [pscustomobject]@{ HResult = $HResult; ErrorRecord = $ErrorRecord }
}
