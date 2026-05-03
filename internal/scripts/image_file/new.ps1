# image_file/new.ps1 -- place a file on the host, or attest to one already there.
#
# Wire contract (locked in by Tests.ps1):
#
#   stdin JSON  : {
#                   "destination_path": "<absolute-path>",          # required
#                   "source_mode":      "url"|"host_path"|"local_path", # required
#                   "url":              "<string>",                 # url mode
#                   "expected_sha256":  "<hex>",                    # url + local_path
#                   "staging_path":     "<absolute-path>"           # local_path
#                 }
#   stdout JSON : same shape as get.ps1 (Path, SizeBytes, Sha256).
#
# Mode semantics:
#   url        - download via HttpClient to a sibling .part file in the
#                destination directory, verify SHA-256 against
#                expected_sha256, then atomic-rename (Move-Item) to
#                destination_path. NTFS rename within a volume is atomic;
#                the .part-in-destination-dir layout keeps it that way.
#   host_path  - verify-only: the user attests the file already exists at
#                destination_path. No copy. Missing-file surfaces as
#                ObjectNotFound -> ErrNotFound, same as Read.
#   local_path - the Go side has streamed bytes from the runner to
#                staging_path on the host (via Connection.StreamFile).
#                This script verifies the staged file's SHA-256 against
#                the runner-computed expected_sha256 (transport-corruption
#                check) and atomic-renames the staging file to
#                destination_path. Same .part-in-destination-dir,
#                same Move-Item-is-atomic guarantee as url mode.
#
# Why HttpClient (not BITS or Invoke-WebRequest):
#   - Start-BitsTransfer requires an interactive user session (HRESULT
#     0x800704DD over SSH/WinRM "Network" logon).
#   - Invoke-WebRequest -OutFile on PS 5.1 buffers the response body in
#     memory before writing to disk -- fine for small files, OOMs on the
#     multi-GB VHDX images this resource exists to fetch.
#   - HttpClient streams via CopyToAsync, runs in any session type, and is
#     Microsoft's recommended primitive in 2026 (WebClient is [Obsolete]
#     since .NET 6 even though still functional on .NET Framework).

# Save-HypervHttpFile downloads $Url to $OutFile via System.Net.Http.HttpClient
# with a streamed response copy. ResponseHeadersRead returns as soon as the
# headers arrive so the body is consumed via the stream without buffering;
# CopyTo writes to disk incrementally. EnsureSuccessStatusCode raises on any
# non-2xx so transport-level failures surface through the catch in the
# entry block.
#
# TLS 1.2 is OR'd into the protocol set as a floor because PS 5.1 / .NET
# Framework 4.7.2 on older Server 2019 builds can default to TLS 1.0/1.1,
# which most modern HTTPS endpoints reject. -bor (rather than =) preserves
# TLS 1.3 on .NET 4.8+ hosts where the enum already includes it, so we
# raise the floor without capping the ceiling. The assignment is process-
# global, but each verb script runs in a fresh PowerShell process per
# -EncodedCommand, so leakage to unrelated calls is bounded to this one
# invocation.
function Save-HypervHttpFile {
    [CmdletBinding()]
    param(
        [Parameter(Mandatory)] [string] $Url,
        [Parameter(Mandatory)] [string] $OutFile
    )
    # System.Net.Http isn't auto-loaded on Windows PowerShell 5.1 (the
    # Server 2019 floor); Add-Type with -AssemblyName is a no-op when the
    # assembly is already loaded (PS 7+) so it's safe to call unconditionally.
    Add-Type -AssemblyName System.Net.Http
    [System.Net.ServicePointManager]::SecurityProtocol = `
        [System.Net.ServicePointManager]::SecurityProtocol -bor `
        [System.Net.SecurityProtocolType]::Tls12
    $client = [System.Net.Http.HttpClient]::new()
    try {
        $response = $client.GetAsync(
            $Url, [System.Net.Http.HttpCompletionOption]::ResponseHeadersRead
        ).GetAwaiter().GetResult()
        try {
            $response.EnsureSuccessStatusCode() | Out-Null
            $stream = $response.Content.ReadAsStreamAsync().GetAwaiter().GetResult()
            try {
                $file = [System.IO.File]::Create($OutFile)
                try   { $stream.CopyTo($file) }
                finally { $file.Dispose() }
            } finally { $stream.Dispose() }
        } finally { $response.Dispose() }
    } finally { $client.Dispose() }
}

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

# New-HypervImageFileFromUrl downloads via Save-HypervHttpFile to a sibling
# .part file in the destination directory, verifies the hash, and atomic-
# renames into place. A finally-block removes the .part on any failure path
# (transport error, hash mismatch, rename failure) so a half-baked file
# never lingers under the canonical name or as a stale .part.
function New-HypervImageFileFromUrl {
    [CmdletBinding()]
    param(
        [Parameter(Mandatory)] [string] $DestinationPath,
        [Parameter(Mandatory)] [string] $Url,
        [Parameter(Mandatory)] [string] $ExpectedSha256
    )
    $tempPath = "$DestinationPath.part-$([guid]::NewGuid().ToString('n'))"
    try {
        Save-HypervHttpFile -Url $Url -OutFile $tempPath
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

# New-HypervImageFileFromLocalPath verifies a file the Go-side StreamFile
# primitive has just deposited at staging_path, then atomic-renames it to
# destination_path on hash match. Mirrors the url-mode shape: the only
# difference is where the staged bytes came from (Go-side stream vs
# HttpClient download). Same .part-in-destination-dir layout keeps the
# Move-Item atomic on NTFS, same finally-block cleanup keeps a half-baked
# staging file from lingering across a failed apply.
function New-HypervImageFileFromLocalPath {
    [CmdletBinding()]
    param(
        [Parameter(Mandatory)] [string] $DestinationPath,
        [Parameter(Mandatory)] [string] $StagingPath,
        [Parameter(Mandatory)] [string] $ExpectedSha256
    )
    try {
        if (-not (Test-Path -LiteralPath $StagingPath -PathType Leaf)) {
            $exception = [System.Management.Automation.ItemNotFoundException]::new(
                "Image file staging path not found at '$StagingPath'.")
            $errorRecord = [System.Management.Automation.ErrorRecord]::new(
                $exception, 'ImageFileStagingNotFound',
                [System.Management.Automation.ErrorCategory]::ObjectNotFound, $StagingPath)
            throw $errorRecord
        }
        $actualHash   = (Get-FileHash -LiteralPath $StagingPath -Algorithm SHA256).Hash.ToLowerInvariant()
        $expectedHash = $ExpectedSha256.ToLowerInvariant()
        if ($actualHash -ne $expectedHash) {
            $exception = [System.IO.InvalidDataException]::new(
                "Checksum mismatch for staged file '$StagingPath': expected sha256=$expectedHash, got sha256=$actualHash.")
            $errorRecord = [System.Management.Automation.ErrorRecord]::new(
                $exception, 'ImageFileChecksumMismatch',
                [System.Management.Automation.ErrorCategory]::InvalidData, $StagingPath)
            throw $errorRecord
        }
        Move-Item -LiteralPath $StagingPath -Destination $DestinationPath -Force -ErrorAction Stop
    }
    finally {
        # Cleanup is best-effort: a successful Move-Item already consumed
        # the staging file, so the Test-Path skips the Remove. On any
        # failure path (missing file, hash mismatch, Move-Item error) the
        # Remove keeps a stale .part from accumulating across applies.
        if (Test-Path -LiteralPath $StagingPath) {
            Remove-Item -LiteralPath $StagingPath -Force -ErrorAction SilentlyContinue
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
            'local_path' {
                New-HypervImageFileFromLocalPath `
                    -DestinationPath $params.destination_path `
                    -StagingPath     $params.staging_path `
                    -ExpectedSha256  $params.expected_sha256
            }
            default {
                throw "Unknown source_mode '$($params.source_mode)'; expected 'url', 'host_path', or 'local_path'."
            }
        }
    }
    catch {
        Write-HypervError $_
        exit 1
    }
}
