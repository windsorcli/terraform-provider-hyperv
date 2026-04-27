# image_file/get.ps1 -- read metadata for a file at a path on the host.
#
# Wire contract (locked in by Tests.ps1):
#
#   stdin JSON  : { "path": "<absolute-path>" }
#   stdout JSON : {
#                   "Path":      "<string>",          # canonical FullName
#                   "SizeBytes": <int64>,
#                   "Sha256":    "<lowercase-hex>"
#                 }
#   stderr/exit : missing file -> Write-HypervError envelope with
#                 category=ObjectNotFound + exit 1, mapped to ErrNotFound on
#                 the Go side so resource Read calls RemoveResource.
#
# SHA-256 is recomputed on every Read by design (PLAN.md S7 drift detection).
# Image-heavy refreshes are slow as a result; documented in the resource.

# Get-HypervImageFile reads file metadata + SHA-256. Test-Path returns $false
# for non-existent paths (no error), so the missing branch sidesteps the
# SilentlyContinue trap that bit vswitch -- a non-terminating error is not
# the failure mode here, $false is. Permission/IO errors from Test-Path
# itself propagate via $ErrorActionPreference='Stop' from the preamble.
function Get-HypervImageFile {
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
    $item = Get-Item -LiteralPath $Path
    $hash = (Get-FileHash -LiteralPath $Path -Algorithm SHA256).Hash.ToLowerInvariant()
    [pscustomobject]@{
        Path      = $item.FullName
        SizeBytes = [int64] $item.Length
        Sha256    = $hash
    } | Write-HypervResult
}

# Entry block. Skipped during Pester runs (dot-source sets InvocationName='.').
if ($MyInvocation.InvocationName -ne '.') {
    try {
        $params = [Console]::In.ReadToEnd() | ConvertFrom-Json
        Get-HypervImageFile -Path $params.path
    }
    catch {
        Write-HypervError $_
        exit 1
    }
}
