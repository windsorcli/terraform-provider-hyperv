# ics_network/new.ps1 -- create an HCN ICS network with a static IPAM subnet.
#
# Wire contract (locked in by Tests.ps1):
#
#   stdin JSON  : {
#                   "name":           "<string>",
#                   "address_prefix": "<IPv4 CIDR>",
#                   "host_address":   "<first host in address_prefix>"
#                 }
#   stdout JSON : the six-field object get.ps1 emits.
#
# Flags 19 is 3 plus 16. On the bench, 16 put the network in HNS's
# persistent store so it survived a host reboot; with 3 alone it did not.
# If the read after create fails, the script deletes the network so no
# untracked network remains.
#
# lint:allow-long-comment

# New-HypervIcsNetworkDocument builds the settings JSON document HcnCreateNetwork takes.
function New-HypervIcsNetworkDocument {
    [CmdletBinding()]
    param(
        [Parameter(Mandatory)] [string] $Name,
        [Parameter(Mandatory)] [string] $AddressPrefix,
        [Parameter(Mandatory)] [string] $HostAddress
    )
    [ordered]@{
        SchemaVersion = [ordered]@{ Major = 2; Minor = 0 }
        Name          = $Name
        Type          = 'ICS'
        Flags         = 19
        Ipams         = @(
            [ordered]@{
                Type    = 'Static'
                Subnets = @(
                    [ordered]@{
                        IpAddressPrefix = $AddressPrefix
                        Routes          = @([ordered]@{ NextHop = $HostAddress; DestinationPrefix = '0.0.0.0/0' })
                    }
                )
            }
        )
    } | ConvertTo-Json -Depth 10 -Compress
}

# New-HypervIcsNetwork creates the network under a fresh GUID and emits its read result.
function New-HypervIcsNetwork {
    [CmdletBinding()]
    param(
        [Parameter(Mandatory)] [string] $Name,
        [Parameter(Mandatory)] [string] $AddressPrefix,
        [Parameter(Mandatory)] [string] $HostAddress
    )
    $id = [guid]::NewGuid()
    $settings = New-HypervIcsNetworkDocument -Name $Name -AddressPrefix $AddressPrefix -HostAddress $HostAddress
    $call = Invoke-HypervHcnCreate -Id $id -Settings $settings
    if ($call.HResult -ne 0) {
        throw (New-HypervHcnErrorRecord -HResult $call.HResult -Operation 'create' -Target $Name -ErrorRecord $call.ErrorRecord)
    }

    try {
        $network = Read-HypervIcsNetwork -Id $id
    }
    catch {
        [void](Invoke-HypervHcnDelete -Id $id)
        throw
    }
    $network | Write-HypervResult
}

# Entry block. Skipped during Pester runs (dot-source sets InvocationName='.').
if ($MyInvocation.InvocationName -ne '.') {
    try {
        $params = Read-HypervStdinPayload
        New-HypervIcsNetwork `
            -Name $params.name `
            -AddressPrefix $params.address_prefix `
            -HostAddress $params.host_address
    }
    catch {
        Write-HypervError $_
        exit 1
    }
}
