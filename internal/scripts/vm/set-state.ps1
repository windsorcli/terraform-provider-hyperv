# vm/set-state.ps1 -- transition a VM to a desired power state.
#
# Wire contract (locked in by Tests.ps1):
#
#   stdin JSON  : {
#                   "name":          "<vm-name>",
#                   "desired":       "Off" | "Running",
#                   "shutdown_mode": "turn_off" | "graceful"  (optional, default "turn_off")
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
#     slice but the cmdlet errors clearly). ShutdownMode is ignored
#     for the start path -- Start-VM has no graceful analog.
#   - desired=Off, shutdown_mode=turn_off (default): Stop-VM
#     -TurnOff -Force. Hard power-off, matching destroy semantics in
#     remove.ps1. Safe on guests without integration services.
#   - desired=Off, shutdown_mode=graceful: Stop-VM -Force (no
#     -TurnOff). Sends an ACPI shutdown signal via Hyper-V
#     integration services and waits for the guest to ack. Hangs
#     on guests without integration services running -- documented
#     at the schema layer; the operator opts in by writing
#     shutdown_mode = "graceful".
#
# Idempotency: Start-VM on an already-Running VM is a no-op (cmdlet
# emits a warning we silence via -ErrorAction). Same for Stop-VM on
# an already-Off VM. The resource layer's plan-vs-state diff filters
# the redundant call out anyway, but the cmdlet-level idempotence is
# the second line of defense.


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
            if ($ShutdownMode -eq 'graceful') {
                # Graceful: ACPI shutdown via integration services. The
                # cmdlet returns once the guest acknowledges OR after
                # Hyper-V's internal timeout. We don't add a PS-side
                # timeout because the per-call CommandTimeout in
                # connection/ssh.go is the authoritative bound -- a
                # double-timeout would just race.
                Stop-VM -VM $vm -Force -ErrorAction Stop | Out-Null
            }
            else {
                # turn_off (default): hard power-off, matches
                # `terraform destroy` semantics. Always safe -- no
                # integration-services dependency.
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
        $params = [Console]::In.ReadToEnd() | ConvertFrom-Json
        # shutdown_mode is optional on the wire; absent -> turn_off
        # default. The Go side always emits the field once it's set on
        # the resource, but a stale typed client (older Go binary
        # against an updated script) would omit it -- so don't bind
        # an empty string to the ValidateSet'd parameter.
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
