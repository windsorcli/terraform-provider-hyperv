# vm/set-state.ps1 -- transition a VM to a desired power state.
#
# Wire contract (locked in by Tests.ps1):
#
#   stdin JSON  : {
#                   "name":    "<vm-name>",
#                   "desired": "Off" | "Running"
#                 }
#   stdout JSON : same shape as get.ps1 (the post-transition VM read).
#   stderr/exit : missing VM -> ObjectNotFound -> ErrNotFound. Other
#                 cmdlet errors (Start-VM on a VM with no boot media
#                 doesn't error -- the VM enters Running and hangs at
#                 a "no boot device" prompt, which is fine for this
#                 layer) propagate with their original category.
#
# Dispatch:
#   - desired=Running: Start-VM. Works from any non-Running state
#     (Off boots; Saved resumes; Paused isn't supported in this
#     slice but the cmdlet errors clearly).
#   - desired=Off: Stop-VM -TurnOff -Force. Hard power-off,
#     matching destroy semantics in remove.ps1. Graceful shutdown
#     (Stop-VM -Force without -TurnOff) is a future option once
#     `state` grows a `shutdown_mode` attribute -- it requires
#     integration services in the guest, which our acc-test
#     fixtures don't have.
#
# Idempotency: Start-VM on an already-Running VM is a no-op (cmdlet
# emits a warning we silence via -ErrorAction). Same for Stop-VM on
# an already-Off VM. The resource layer's plan-vs-state diff filters
# the redundant call out anyway, but the cmdlet-level idempotence is
# the second line of defense.


function Set-HypervVMState {
    [CmdletBinding()]
    param(
        [Parameter(Mandatory)] [string]                       $Name,
        [Parameter(Mandatory)] [ValidateSet('Off', 'Running')] [string] $Desired
    )
    try {
        $vm = Get-VM -Name $Name -ErrorAction Stop
    }
    catch {
        # Mirror get.ps1's "missing VM" mapping: cmdlet may surface
        # ObjectNotFound or InvalidArgument + the GetVM FQId
        # depending on Hyper-V module version.
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

    # Both branches rely on the global $WarningPreference =
    # 'SilentlyContinue' from preamble.ps1: Start-VM on a Running VM and
    # Stop-VM on an Off VM both emit a "VM is already in the specified
    # state" warning that the SSH transport merges onto stdout, breaking
    # JSON parsing on the Go side. A narrower `-WarningAction
    # SilentlyContinue` per call would work *here* but the global pin
    # keeps the contract uniform across every script -- see the
    # preamble's $WarningPreference comment for the cost trade-off.
    switch ($Desired) {
        'Running' {
            Start-VM -VM $vm -ErrorAction Stop | Out-Null
        }
        'Off' {
            Stop-VM -VM $vm -TurnOff -Force -ErrorAction Stop | Out-Null
        }
    }

    $vm = Get-VM -Name $Name -ErrorAction Stop
    Read-HypervVMResult -Vm $vm
}

# Entry block. Skipped during Pester runs (dot-source sets InvocationName='.').
if ($MyInvocation.InvocationName -ne '.') {
    try {
        $params = [Console]::In.ReadToEnd() | ConvertFrom-Json
        Set-HypervVMState -Name $params.name -Desired $params.desired
    }
    catch {
        Write-HypervError $_
        exit 1
    }
}
