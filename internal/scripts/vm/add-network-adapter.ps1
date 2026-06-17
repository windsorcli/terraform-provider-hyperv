# vm/add-network-adapter.ps1 -- attach a new NIC to a VM and bind it to
# a virtual switch. Optionally pin a static MAC address and/or set
# an access-mode VLAN tag.
#
# Wire contract (locked in by Tests.ps1):
#
#   stdin JSON  : {
#                   "name":        "<adapter-display-name>",
#                   "vm_name":     "<vm-name>",
#                   "switch_name": "<vswitch-name>",
#                   "mac_address": "<MAC>" | "" | absent,
#                   "vlan_id":     <1-4094> | 0 | absent
#                 }
#   stdout JSON : {} on success.
#   stderr/exit : missing VM   -> ObjectNotFound -> ErrNotFound
#                 missing switch -> InvalidArgument (Hyper-V's "switch
#                 not found" surfaces here as part of the Add cmdlet's
#                 input validation) -> ErrPSExecution.
#
# The display name is the user's slot key for diff/reconciliation; the
# Go-side resource layer enforces uniqueness within a VM's NIC list at
# plan time. MacAddress format is pre-validated at the schema layer
# (colon, hyphen, or unsigned-12-hex). VlanID is pre-validated to
# 1-4094 by the schema; 0 / absent here means "leave NIC untagged".

function Add-HypervVMNetworkAdapter {
    [CmdletBinding()]
    param(
        [Parameter(Mandatory)] [string] $Name,
        [Parameter(Mandatory)] [string] $VMName,
        [Parameter(Mandatory)] [string] $SwitchName,
        [string] $MacAddress = '',
        [int]    $VlanID     = 0
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
    @{} | Write-HypervResult
}

# Entry block. Skipped during Pester runs (dot-source sets InvocationName='.').
if ($MyInvocation.InvocationName -ne '.') {
    try {
        $params = Read-HypervStdinParams
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
        Add-HypervVMNetworkAdapter @invokeArgs
    }
    catch {
        Write-HypervError $_
        exit 1
    }
}
