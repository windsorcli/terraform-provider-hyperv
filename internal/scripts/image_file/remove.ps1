# image_file/remove.ps1 -- delete a file from the host.
#
# Wire contract (locked in by Tests.ps1):
#
#   stdin JSON  : { "path": "<absolute-path>" }
#   stdout      : empty (caller passes dst=nil to runScript).
#   stderr/exit : missing file -> Write-HypervError envelope with
#                 category=ObjectNotFound + exit 1, mapped to ErrNotFound on
#                 the Go side so Delete can treat already-gone as success.
#
# Delete is gated on the Go side: only invoked when the source mode placed
# the file (source_mode=url). For host_path mode, Delete is a no-op in Go --
# the user did not ask the provider to put the file there, so removing it on
# destroy would surprise them.

# Remove-HypervImageFile deletes a file at the given path. Test-Path returns
# $false (no error) for non-existent paths, so the missing branch sidesteps
# the SilentlyContinue trap. Permission/IO errors from Test-Path or
# Remove-Item propagate via $ErrorActionPreference='Stop' from the preamble.
function Remove-HypervImageFile {
    [CmdletBinding()]
    param(
        [Parameter(Mandatory)] [string] $Path
    )
    if (-not (Test-Path -LiteralPath $Path -PathType Leaf)) {
        $exception = [System.Management.Automation.ItemNotFoundException]::new(
            "Image file not found at path '$Path'.")
        $errorRecord = [System.Management.Automation.ErrorRecord]::new(
            $exception, 'ImageFileNotFound',
            [System.Management.Automation.ErrorCategory]::ObjectNotFound, $Path)
        throw $errorRecord
    }
    Remove-Item -LiteralPath $Path -Force -ErrorAction Stop
}

# Entry block. Skipped during Pester runs (dot-source sets InvocationName='.').
if ($MyInvocation.InvocationName -ne '.') {
    try {
        $params = [Console]::In.ReadToEnd() | ConvertFrom-Json
        Remove-HypervImageFile -Path $params.path
    }
    catch {
        Write-HypervError $_
        exit 1
    }
}
