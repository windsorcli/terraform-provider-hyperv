# vswitch/remove.ps1 -- delete a virtual switch by name.
#
# Wire contract (locked in by Tests.ps1):
#
#   stdin JSON  : { "name": "<switch-name>", "nat_name": "<string>"? }
#                 nat_name is optional; the Go-side resource Delete passes it
#                 from prior state for NAT-typed resources so the script
#                 tears down NetNat + NetIPAddress before Remove-VMSwitch.
#   stdout      : empty (caller passes dst=nil to runScript).
#   stderr/exit : missing switch -> Write-HypervError envelope with
#                 category=ObjectNotFound + exit 1, mapped to ErrNotFound on
#                 the Go side so Delete can treat already-gone as success.
#
# NAT teardown order is load-bearing: Remove-VMSwitch fails if the NetNat
# instance still references the switch's vNIC, so NetNat first, then
# NetIPAddress (un-IPs the vNIC), then Remove-VMSwitch. Each step tolerates
# its own ObjectNotFound -- best-effort destroy semantics for partial
# out-of-band cleanup.

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

    # NAT pre-steps: Remove-NetNat then Remove-NetIPAddress before
    # Remove-VMSwitch. Each step tolerates ObjectNotFound (best-effort
    # destroy: a previous partial teardown may have already removed
    # individual pieces). The order is load-bearing -- Remove-VMSwitch
    # fails if the NetNat still references the switch's vNIC.
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

# Entry block. Skipped during Pester runs (dot-source sets InvocationName='.').
if ($MyInvocation.InvocationName -ne '.') {
    try {
        $params = [Console]::In.ReadToEnd() | ConvertFrom-Json
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
