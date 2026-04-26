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
    # SilentlyContinue closes the TOCTOU window between the existence check
    # above and the actual removal: if another agent (Hyper-V Manager, cluster
    # failover) deletes the switch between these two calls, the cmdlet's own
    # InvalidArgument-categorized error would otherwise surface as
    # ErrPSExecution and fail Delete. We've already verified existence; a
    # silent failure here is the right semantic ("already gone is success").
    Remove-VMSwitch -Name $Name -Force -ErrorAction SilentlyContinue
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
