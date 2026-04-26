# vswitch/remove.ps1 -- delete a virtual switch by name.
#
# Wire contract (locked in by Tests.ps1):
#
#   stdin JSON  : { "name": "<switch-name>" }
#   stdout      : empty (caller passes dst=nil to runScript).
#   stderr/exit : missing switch -> Write-HypervError envelope with
#                 category=ObjectNotFound + exit 1, mapped to ErrNotFound on
#                 the Go side so Delete can treat already-gone as success.

# Remove-HypervSwitch deletes a switch by name. Missing-switch case throws
# an explicit ObjectNotFound so Delete on the Go side can treat already-gone
# as success. The cmdlet's own missing-switch error is categorized as
# InvalidArgument, which would otherwise surface as ErrPSExecution and
# fail Delete unnecessarily. -Force bypasses confirmation (required in
# non-interactive contexts).
function Remove-HypervSwitch {
    [CmdletBinding()]
    param(
        [Parameter(Mandatory)] [string] $Name
    )
    $sw = Get-VMSwitch -Name $Name -ErrorAction SilentlyContinue
    if ($null -eq $sw) {
        $exception = [System.Management.Automation.ItemNotFoundException]::new(
            "Hyper-V was unable to find a virtual switch with name '$Name'.")
        $errorRecord = [System.Management.Automation.ErrorRecord]::new(
            $exception, 'VMSwitchNotFound',
            [System.Management.Automation.ErrorCategory]::ObjectNotFound, $Name)
        throw $errorRecord
    }
    # Symmetric with set.ps1: pre-check uses SilentlyContinue, the action
    # cmdlet uses Stop so transient WMI faults, busy-resource errors, and
    # permission failures surface to the Go side instead of being swallowed
    # (which would record Delete success and drop the switch from state
    # while leaving it on the host).
    Remove-VMSwitch -Name $Name -Force -ErrorAction Stop
}

# Entry block. Skipped during Pester runs (dot-source sets InvocationName='.').
if ($MyInvocation.InvocationName -ne '.') {
    try {
        $params = [Console]::In.ReadToEnd() | ConvertFrom-Json
        Remove-HypervSwitch -Name $params.name
    }
    catch {
        Write-HypervError $_
        exit 1
    }
}
