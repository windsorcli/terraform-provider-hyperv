# vswitch/remove.ps1 -- delete a virtual switch by name.
#
# Wire contract (locked in by Tests.ps1):
#
#   stdin JSON  : { "name": "<switch-name>", "nat_name": "<string>"? }
#                 nat_name is optional; the Go-side resource Delete passes it
#                 from prior state for NAT-typed resources so the script
#                 tears down NetNat + NetIPAddress before Remove-VMSwitch.
#   stdout      : empty (caller passes dst=nil to runScript).
#   stderr/exit : missing switch -> ObjectNotFound envelope, exit 1 -> Go
#                 maps to ErrNotFound so Delete treats already-gone as
#                 success. A VM that isn't Off with a NIC on the switch ->
#                 ResourceBusy envelope, exit 1, before anything is removed.
#
# Remove-VMSwitch runs last, so a failed NetNat or NetIPAddress step leaves
# the VMSwitch that get.ps1 keys on. Each NAT step tolerates its own
# ObjectNotFound.
#
# lint:allow-long-comment

# Remove-HypervSwitch deletes a switch by name. Missing-switch case throws
# an explicit ObjectNotFound so Delete on the Go side can treat already-gone
# as success. The cmdlet's own missing-switch error is categorized as
# InvalidArgument, which would otherwise surface as ErrPSExecution and
# fail Delete unnecessarily. -Force bypasses confirmation (required in
# non-interactive contexts).
function Remove-HypervSwitch {
    [CmdletBinding()]
    param(
        [Parameter(Mandatory)] [string] $Name,
        [string] $NatName
    )
    # Stop + selective catch instead of SilentlyContinue: a transient WMI
    # fault, permission error, or cluster-connectivity blip would otherwise
    # be indistinguishable from "switch missing", get remapped to ObjectNotFound,
    # and let the Go side drop a still-present switch from state.
    try {
        $sw = Get-VMSwitch -Name $Name -ErrorAction Stop
    }
    catch {
        if ($_.CategoryInfo.Category -ne [System.Management.Automation.ErrorCategory]::ObjectNotFound) {
            throw
        }
        $sw = $null
    }
    if ($null -eq $sw) {
        $exception = [System.Management.Automation.ItemNotFoundException]::new(
            "Hyper-V was unable to find a virtual switch with name '$Name'.")
        $errorRecord = [System.Management.Automation.ErrorRecord]::new(
            $exception, 'VMSwitchNotFound',
            [System.Management.Automation.ErrorCategory]::ObjectNotFound, $Name)
        throw $errorRecord
    }

    Assert-HypervSwitchNotInUse -Name $Name

    # NAT pieces may already be gone after an earlier partial teardown, so each removal is skipped when absent.
    if ($PSBoundParameters.ContainsKey('NatName') -and $NatName -ne '') {
        $existingNat = Get-NetNat -Name $NatName -ErrorAction SilentlyContinue |
            Select-Object -First 1
        if ($null -ne $existingNat) {
            Remove-NetNat -Name $NatName -Confirm:$false -ErrorAction Stop
        }
        $existingIp = Get-NetIPAddress `
            -InterfaceAlias "vEthernet ($Name)" `
            -AddressFamily 'IPv4' `
            -ErrorAction SilentlyContinue |
            Select-Object -First 1
        if ($null -ne $existingIp) {
            Remove-NetIPAddress `
                -InterfaceAlias "vEthernet ($Name)" `
                -IPAddress $existingIp.IPAddress `
                -Confirm:$false `
                -ErrorAction Stop
        }
    }

    # Stop on the action cmdlet so transient WMI faults, busy-resource errors,
    # and permission failures surface to the Go side rather than being
    # swallowed (which would record Delete success and drop the switch from
    # state while leaving it on the host).
    Remove-VMSwitch -Name $Name -Force -ErrorAction Stop
}

# Assert-HypervSwitchNotInUse throws ResourceBusy when a VM that isn't Off
# has a NIC on the switch. Remove-VMSwitch refuses that case itself, but only
# after the NAT steps have already removed the NetNat and host IP.
function Assert-HypervSwitchNotInUse {
    [CmdletBinding()]
    param(
        [Parameter(Mandatory)] [string] $Name
    )
    $activeVMs = @(Get-VM -ErrorAction Stop | Where-Object { $_.State.ToString() -ne 'Off' })
    if ($activeVMs.Count -eq 0) {
        return
    }
    $attached = @($activeVMs | Get-VMNetworkAdapter -ErrorAction Stop |
        Where-Object { $_.SwitchName -eq $Name })
    if ($attached.Count -eq 0) {
        return
    }
    $vmNames = ($attached | ForEach-Object { $_.VMName } | Sort-Object -Unique) -join ', '
    $exception = [System.InvalidOperationException]::new(
        "Virtual switch '$Name' is in use by VMs that are not Off: $vmNames. Stop these VMs or move their network adapters to another switch, then retry the delete.")
    $errorRecord = [System.Management.Automation.ErrorRecord]::new(
        $exception, 'VMSwitchInUse',
        [System.Management.Automation.ErrorCategory]::ResourceBusy, $Name)
    throw $errorRecord
}

# Entry block. Skipped during Pester runs (dot-source sets InvocationName='.').
if ($MyInvocation.InvocationName -ne '.') {
    try {
        $params = Read-HypervStdinPayload
        $callArgs = @{ Name = $params.name }
        if ($params.PSObject.Properties.Name -contains 'nat_name' -and $null -ne $params.nat_name -and $params.nat_name -ne '') {
            $callArgs.NatName = $params.nat_name
        }
        Remove-HypervSwitch @callArgs
    }
    catch {
        Write-HypervError $_
        exit 1
    }
}
