# vhd/remove.ps1 -- delete a VHD file from the host.
#
# Wire contract (locked in by Tests.ps1):
#
#   stdin JSON  : { "path": "<absolute-path>" }
#   stdout      : empty (caller passes dst=nil to runScript).
#   stderr/exit : missing file -> Write-HypervError envelope with
#                 category=ObjectNotFound + exit 1, mapped to ErrNotFound on
#                 the Go side so Delete can treat already-gone as success.
#
# There's no Remove-VHD cmdlet -- a VHD is just a file. Remove-Item is what
# Hyper-V tooling itself uses. The cmdlet errors loudly if the file is
# attached to a running VM (open file handle), which surfaces as a
# transport-level error and bubbles up via the catch block.

# Remove-HypervVHD deletes the VHD file. Same Test-Path-first pattern as
# get/set: missing file returns $false (no error) so the missing branch
# sidesteps the SilentlyContinue trap. Permission/IO/in-use errors from
# Test-Path or Remove-Item propagate via $ErrorActionPreference='Stop'.
function Remove-HypervVHD {
    [CmdletBinding()]
    param(
        [Parameter(Mandatory)] [string] $Path
    )
    if (-not (Test-Path -LiteralPath $Path -PathType Leaf)) {
        $exception = [System.Management.Automation.ItemNotFoundException]::new(
            "VHD not found at path '$Path'.")
        $errorRecord = [System.Management.Automation.ErrorRecord]::new(
            $exception, 'VHDNotFound',
            [System.Management.Automation.ErrorCategory]::ObjectNotFound, $Path)
        throw $errorRecord
    }
    Remove-Item -LiteralPath $Path -Force -ErrorAction Stop
}

# Entry block. Skipped during Pester runs (dot-source sets InvocationName='.').
if ($MyInvocation.InvocationName -ne '.') {
    try {
        $params = Read-HypervStdinParams
        Remove-HypervVHD -Path $params.path
    }
    catch {
        Write-HypervError $_
        exit 1
    }
}
