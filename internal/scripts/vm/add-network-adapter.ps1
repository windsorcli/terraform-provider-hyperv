# vm/add-network-adapter.ps1 -- attach a new NIC to a VM and bind it to
# a virtual switch. Optionally pin a static MAC address, set an
# access-mode VLAN tag, or set a VLAN isolation ID.
#
# Wire contract (locked in by Tests.ps1):
#
#   stdin JSON  : {
#                   "name":        "<adapter-display-name>",
#                   "vm_name":     "<vm-name>",
#                   "switch_name": "<vswitch-name>",
#                   "mac_address": "<MAC>" | "" | absent,
#                   "vlan_id":     <1-4094> | 0 | absent,
#                   "isolation_id": <int >= 1> | 0 | absent
#                 }
#   stdout JSON : {} on success.
#   stderr/exit : missing VM   -> ObjectNotFound -> ErrNotFound
#                 missing switch -> InvalidArgument (Hyper-V's "switch
#                 not found" surfaces here as part of the Add cmdlet's
#                 input validation) -> ErrPSExecution.
#
# The display name is the user's slot key for diff/reconciliation,
# unique within a VM's NIC list at the schema layer. VlanID 0/absent
# means "leave NIC untagged", and IsolationID 0/absent leaves isolation
# off. A NIC on a hyperv_ics_network switch gets a DHCP lease only when
# its isolation ID matches the network's.
#
# lint:allow-long-comment

function Add-HypervVMNetworkAdapter {
    [CmdletBinding()]
    param(
        [Parameter(Mandatory)] [string] $Name,
        [Parameter(Mandatory)] [string] $VMName,
        [Parameter(Mandatory)] [string] $SwitchName,
        [string] $MacAddress = '',
        [int]    $VlanID      = 0,
        [int]    $IsolationID = 0
    )
    # Splat lets us conditionally include -StaticMacAddress -- Hyper-V
    # treats the cmdlet's absence as "use dynamic MAC pool", which is
    # what we want when the user didn't pin one.
    $addArgs = @{
        VMName      = $VMName
        Name        = $Name
        SwitchName  = $SwitchName
        ErrorAction = 'Stop'
    }
    if ($MacAddress -ne '') {
        $addArgs.StaticMacAddress = $MacAddress
    }
    Add-VMNetworkAdapter @addArgs | Out-Null

    # VLAN: only call Set-VMNetworkAdapterVlan when the user explicitly
    # set vlan_id. Untagged is Hyper-V's default for new NICs, so a
    # zero VlanID means "leave it alone."
    if ($VlanID -gt 0) {
        Set-VMNetworkAdapterVlan `
            -VMName $VMName `
            -VMNetworkAdapterName $Name `
            -Access `
            -VlanId $VlanID `
            -ErrorAction Stop | Out-Null
    }
    if ($IsolationID -gt 0) {
        Set-VMNetworkAdapterIsolation `
            -VMName $VMName `
            -VMNetworkAdapterName $Name `
            -IsolationMode Vlan `
            -DefaultIsolationID $IsolationID `
            -AllowUntaggedTraffic $true `
            -ErrorAction Stop | Out-Null
    }
    @{} | Write-HypervResult
}

# Entry block. Skipped during Pester runs (dot-source sets InvocationName='.').
if ($MyInvocation.InvocationName -ne '.') {
    try {
        $params = Read-HypervStdinPayload
        $invokeArgs = @{
            Name       = $params.name
            VMName     = $params.vm_name
            SwitchName = $params.switch_name
        }
        # PSCustomObject: a missing JSON key surfaces as a NoteProperty
        # not present, so guard with PSObject.Properties before reading
        # to avoid Set-StrictMode 3.0 throwing on the absent member.
        if ($params.PSObject.Properties.Name -contains 'mac_address' -and $params.mac_address) {
            $invokeArgs.MacAddress = [string] $params.mac_address
        }
        if ($params.PSObject.Properties.Name -contains 'vlan_id' -and $params.vlan_id) {
            $invokeArgs.VlanID = [int] $params.vlan_id
        }
        if ($params.PSObject.Properties.Name -contains 'isolation_id' -and $params.isolation_id) {
            $invokeArgs.IsolationID = [int] $params.isolation_id
        }
        Add-HypervVMNetworkAdapter @invokeArgs
    }
    catch {
        Write-HypervError $_
        exit 1
    }
}
