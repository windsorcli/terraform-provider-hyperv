# vm/set-state.ps1 -- transition a VM to a desired power state.
#
# Wire contract (locked in by Tests.ps1):
#
#   stdin JSON  : {
#                   "name":          "<vm-name>",
#                   "desired":       "Off" | "Running",
#                   "shutdown_mode": "turn_off" | "graceful"  (optional, default "turn_off")
#                 }
#   stdout JSON : same fields as get.ps1 (the post-transition VM read).
#   stderr/exit : missing VM -> ObjectNotFound -> ErrNotFound. Other
#                 cmdlet errors propagate with their original category.
#
# Dispatch:
#   - desired=Running: Start-VM, from any non-Running state. ShutdownMode
#     is ignored; Start-VM has no graceful analog.
#   - desired=Off, shutdown_mode=turn_off (default): Stop-VM -TurnOff
#     -Force, matching destroy semantics in remove.ps1.
#   - desired=Off, shutdown_mode=graceful: Stop-VM -Force (no -TurnOff),
#     an ACPI shutdown via integration services. Hangs on a guest
#     without integration services running; the operator opts in.
#
# Start-VM on an already-Running VM, and Stop-VM on an already-Off VM,
# are both cmdlet-level no-ops (second line of defense behind the
# resource layer's own plan-vs-state diff).
#
# lint:allow-long-comment

function Set-HypervVMState {
    [CmdletBinding()]
    param(
        [Parameter(Mandatory)] [string]                              $Name,
        [Parameter(Mandatory)] [ValidateSet('Off', 'Running')]       [string] $Desired,
        [ValidateSet('turn_off', 'graceful')]                        [string] $ShutdownMode = 'turn_off'
    )
    try {
        $vm = Get-VM -Name $Name -ErrorAction Stop
    }
    catch {
        # Mirrors get.ps1's "missing VM" mapping across Hyper-V module versions.
        $isMissing = (
            $_.CategoryInfo.Category -eq [System.Management.Automation.ErrorCategory]::ObjectNotFound
        ) -or (
            $_.FullyQualifiedErrorId -eq 'InvalidParameter,Microsoft.HyperV.PowerShell.Commands.GetVM'
        )
        if (-not $isMissing) {
            throw
        }
        $exception = [System.Management.Automation.ItemNotFoundException]::new(
            "Hyper-V was unable to find a VM with name '$Name'.")
        $errorRecord = [System.Management.Automation.ErrorRecord]::new(
            $exception, 'VMNotFound',
            [System.Management.Automation.ErrorCategory]::ObjectNotFound, $Name)
        throw $errorRecord
    }

    # Relies on the preamble's global $WarningPreference: an already-in-state Start-VM/Stop-VM warning would otherwise corrupt stdout JSON.
    switch ($Desired) {
        'Running' {
            Start-VM -VM $vm -ErrorAction Stop | Out-Null
        }
        'Off' {
            if ($ShutdownMode -eq 'graceful') {
                # No PS-side timeout: the per-call CommandTimeout in connection/ssh.go is the authoritative bound.
                Stop-VM -VM $vm -Force -ErrorAction Stop | Out-Null
            }
            else {
                Stop-VM -VM $vm -TurnOff -Force -ErrorAction Stop | Out-Null
            }
        }
    }

    $vm = Get-VM -Name $Name -ErrorAction Stop
    Read-HypervVMResult -Vm $vm
}

# Entry block. Skipped during Pester runs (dot-source sets InvocationName='.').
if ($MyInvocation.InvocationName -ne '.') {
    try {
        $params = Read-HypervStdinPayload
        # shutdown_mode is optional on the wire; don't bind an empty string to the ValidateSet'd parameter.
        $bind = @{
            Name    = $params.name
            Desired = $params.desired
        }
        if ($params.PSObject.Properties.Match('shutdown_mode').Count -gt 0 -and `
            $null -ne $params.shutdown_mode -and `
            $params.shutdown_mode -ne '') {
            $bind['ShutdownMode'] = $params.shutdown_mode
        }
        Set-HypervVMState @bind
    }
    catch {
        Write-HypervError $_
        exit 1
    }
}
