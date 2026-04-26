# vswitch/remove.ps1 -- delete a virtual switch by name.
#
# Wire contract (locked in by Tests.ps1):
#
#   stdin JSON  : { "name": "<switch-name>" }
#   stdout      : empty (caller passes dst=nil to runScript).
#   stderr/exit : missing switch -> Write-HypervError envelope with
#                 category=ObjectNotFound + exit 1, mapped to ErrNotFound on
#                 the Go side so Delete can treat already-gone as success.

# Remove-HypervSwitch wraps Remove-VMSwitch with -Force (bypass confirmation,
# required since the PLAN.md S5 preamble runs non-interactively) and
# -ErrorAction Stop so the missing-switch case raises a terminating error the
# entry block can convert into the PLAN.md S5 envelope.
function Remove-HypervSwitch {
    [CmdletBinding()]
    param(
        [Parameter(Mandatory)] [string] $Name
    )
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
