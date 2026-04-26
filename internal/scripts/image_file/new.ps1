# image_file/new.ps1 -- place a file on the host, or attest to one already there.
#
# Wire contract (locked in by Tests.ps1):
#
#   stdin JSON  : {
#                   "destination_path": "<absolute-path>",   # required
#                   "source_mode":      "url"|"host_path",   # required
#                   "url":              "<string>",          # url mode
#                   "expected_sha256":  "<hex>"              # url mode
#                 }
#   stdout JSON : same shape as get.ps1 (Path, SizeBytes, Sha256).
#
# Mode semantics:
#   url       - download via Start-BitsTransfer to a sibling .part file in
#               the destination directory, verify SHA-256 against
#               expected_sha256, then atomic-rename (Move-Item) to
#               destination_path. NTFS rename within a volume is atomic; the
#               .part-in-destination-dir layout keeps it that way.
#   host_path - verify-only: the user attests the file already exists at
#               destination_path. No copy. Missing-file surfaces as
#               ObjectNotFound -> ErrNotFound, same as Read.

# Read-HypervImageFileResult emits the canonical three-field result shape.
# Inline duplicate of get.ps1's tail because the runtime concatenates only
# preamble + a single verb script per call (no cross-script helpers).
function Read-HypervImageFileResult {
    [CmdletBinding()]
    param(
        [Parameter(Mandatory)] [string] $Path
    )
    $item = Get-Item -LiteralPath $Path
    $hash = (Get-FileHash -LiteralPath $Path -Algorithm SHA256).Hash.ToLowerInvariant()
    [pscustomobject]@{
        Path      = $item.FullName
        SizeBytes = [int64] $item.Length
        Sha256    = $hash
    } | Write-HypervResult
}

# New-HypervImageFileFromUrl downloads via BITS to a sibling .part file in
# the destination directory, verifies the hash, and atomic-renames into place.
# A finally-block removes the .part on any failure path (BITS error, hash
# mismatch, rename failure) so a half-baked file never lingers under the
# canonical name or as a stale .part.
function New-HypervImageFileFromUrl {
    [CmdletBinding()]
    param(
        [Parameter(Mandatory)] [string] $DestinationPath,
        [Parameter(Mandatory)] [string] $Url,
        [Parameter(Mandatory)] [string] $ExpectedSha256
    )
    $tempPath = "$DestinationPath.part-$([guid]::NewGuid().ToString('n'))"
    try {
        Start-BitsTransfer -Source $Url -Destination $tempPath -ErrorAction Stop
        $actualHash   = (Get-FileHash -LiteralPath $tempPath -Algorithm SHA256).Hash.ToLowerInvariant()
        $expectedHash = $ExpectedSha256.ToLowerInvariant()
        if ($actualHash -ne $expectedHash) {
            $exception = [System.IO.InvalidDataException]::new(
                "Checksum mismatch for '$Url': expected sha256=$expectedHash, got sha256=$actualHash.")
            $errorRecord = [System.Management.Automation.ErrorRecord]::new(
                $exception, 'ImageFileChecksumMismatch',
                [System.Management.Automation.ErrorCategory]::InvalidData, $Url)
            throw $errorRecord
        }
        Move-Item -LiteralPath $tempPath -Destination $DestinationPath -Force -ErrorAction Stop
    }
    finally {
        # Cleanup is best-effort: a failure to remove the .part should not
        # mask the original error (or supersede a successful Move-Item, which
        # already consumed the file).
        if (Test-Path -LiteralPath $tempPath) {
            Remove-Item -LiteralPath $tempPath -Force -ErrorAction SilentlyContinue
        }
    }
    Read-HypervImageFileResult -Path $DestinationPath
}

# New-HypervImageFileFromHostPath verifies the user-asserted file exists
# at destination_path and returns its metadata. No copy, no fetch -- the
# user told us the bytes are already where they belong.
function New-HypervImageFileFromHostPath {
    [CmdletBinding()]
    param(
        [Parameter(Mandatory)] [string] $DestinationPath
    )
    if (-not (Test-Path -LiteralPath $DestinationPath -PathType Leaf)) {
        $exception = [System.Management.Automation.ItemNotFoundException]::new(
            "Image file not found at path '$DestinationPath'.")
        $errorRecord = [System.Management.Automation.ErrorRecord]::new(
            $exception, 'ImageFileNotFound',
            [System.Management.Automation.ErrorCategory]::ObjectNotFound, $DestinationPath)
        throw $errorRecord
    }
    Read-HypervImageFileResult -Path $DestinationPath
}

# Entry block. Skipped during Pester runs (dot-source sets InvocationName='.').
if ($MyInvocation.InvocationName -ne '.') {
    try {
        $params = [Console]::In.ReadToEnd() | ConvertFrom-Json

        switch ($params.source_mode) {
            'url' {
                New-HypervImageFileFromUrl `
                    -DestinationPath $params.destination_path `
                    -Url             $params.url `
                    -ExpectedSha256  $params.expected_sha256
            }
            'host_path' {
                New-HypervImageFileFromHostPath `
                    -DestinationPath $params.destination_path
            }
            default {
                throw "Unknown source_mode '$($params.source_mode)'; expected 'url' or 'host_path'."
            }
        }
    }
    catch {
        Write-HypervError $_
        exit 1
    }
}
