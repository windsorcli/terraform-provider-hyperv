# vhd/remove.ps1 -- delete a VHD file from the host.
#
# Wire contract (locked in by Tests.ps1):
#
#   stdin JSON  : { "path": "<absolute-path>" }
#   stdout      : empty (caller passes dst=nil to runScript).
#   stderr/exit : missing file -> ObjectNotFound envelope, exit 1 -> Go maps
#                 to ErrNotFound so Delete treats already-gone as success.
#
# No Remove-VHD cmdlet exists; a VHD is just a file, so this uses
# Remove-Item, which errors if the file is attached to a running VM.
#
# lint:allow-long-comment

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
        $params = Read-HypervStdinPayload
        Remove-HypervVHD -Path $params.path
    }
    catch {
        Write-HypervError $_
        exit 1
    }
}
