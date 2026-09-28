# image_file/new.ps1 -- place a file on the host, or attest to one already there.
#
# Wire contract (locked in by Tests.ps1):
#
#   stdin JSON  : {
#                   "destination_path": "<absolute-path>",          # required
#                   "source_mode":      "url"|"host_path"|"local_path"|"source_path", # required
#                   "url":              "<string>",                 # url mode
#                   "expected_sha256":  "<hex>",                    # url + local_path + source_path
#                   "staging_path":     "<absolute-path>",          # local_path
#                   "source_path":      "<absolute-path>"           # source_path
#                 }
#   stdout JSON : same fields as get.ps1 (Path, SizeBytes, Sha256).
#
# Mode semantics:
#   url         - download to a sibling .part in the destination dir,
#                 verify SHA-256, atomic-rename into place.
#   host_path   - verify-only: user attests the file already exists;
#                 no copy. Missing file is ObjectNotFound, same as Read.
#   local_path  - the Go side has already streamed bytes to staging_path;
#                 this verifies SHA-256 (transport-corruption check) and
#                 atomic-renames staging_path to destination_path.
#   source_path - host-side Copy-Item from source_path to a sibling
#                 .part, verify SHA-256, atomic-rename. Never crosses
#                 the wire; both endpoints are host-local.
#
# HttpWebRequest, not HttpClient/BITS/Invoke-WebRequest: HttpClient on
# .NET Framework 4.x fails TLS handshake against some HTTPS endpoints
# on WS2019; Start-BitsTransfer needs an interactive session (fails over
# SSH/WinRM); Invoke-WebRequest -OutFile buffers the whole response in
# memory on PS 5.1, which OOMs on multi-GB VHDX images.
#
# lint:allow-long-comment

# Save-HypervHttpFile downloads $Url to $OutFile via HttpWebRequest with a
# streamed response copy. GetResponseStream() returns the body as a stream
# so CopyTo writes to disk incrementally without buffering. Non-2xx responses
# raise a WebException so transport failures surface through the catch in the
# entry block.
function Save-HypervHttpFile {
    [CmdletBinding()]
    param(
        [Parameter(Mandatory)] [string] $Url,
        [Parameter(Mandatory)] [string] $OutFile
    )
    # Explicitly pin TLS 1.2 via ServicePointManager - WS2019 defaults can
    # include TLS 1.0/1.1 which modern servers reject.
    [System.Net.ServicePointManager]::SecurityProtocol = [System.Net.SecurityProtocolType]::Tls12
    $request = [System.Net.HttpWebRequest]::Create($Url)
    $request.Method = 'GET'
    $response = $request.GetResponse()
    try {
        $stream = $response.GetResponseStream()
        try {
            $file = [System.IO.File]::Create($OutFile)
            try   { $stream.CopyTo($file) }
            finally { $file.Dispose() }
        } finally { $stream.Dispose() }
    } finally { $response.Dispose() }
}

# Move-HypervImageFileIntoPlace renames $StagingPath to $DestinationPath.
# Move-Item -Force can fail with "already exists" whenever the destination
# is already populated -- a second resource pointed at the same path, a
# recreate after keep_on_destroy, or a genuine concurrent write. On that
# failure, adopt the destination if its hash matches $StagingPath (the
# documented SHA-skip no-op); otherwise re-throw as a real conflict.
function Move-HypervImageFileIntoPlace {
    [CmdletBinding()]
    param(
        [Parameter(Mandatory)] [string] $StagingPath,
        [Parameter(Mandatory)] [string] $DestinationPath
    )
    try {
        Move-Item -LiteralPath $StagingPath -Destination $DestinationPath -Force -ErrorAction Stop
    }
    catch {
        if ((Test-Path -LiteralPath $StagingPath -PathType Leaf) -and
            (Test-Path -LiteralPath $DestinationPath -PathType Leaf)) {
            $stagedHash = (Get-FileHash -LiteralPath $StagingPath     -Algorithm SHA256).Hash.ToLowerInvariant()
            $destHash   = (Get-FileHash -LiteralPath $DestinationPath -Algorithm SHA256).Hash.ToLowerInvariant()
            if ($stagedHash -eq $destHash) {
                return
            }
        }
        throw
    }
}

# Read-HypervImageFileResult emits the canonical three-field result format.
# Inline duplicate of get.ps1's tail: the runtime concatenates only
# preamble plus a single verb script per call.
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
        [Parameter()]          [string] $ExpectedSha256 = ''
    )
    # Create the destination directory if absent. New-Item -Force is a no-op when
    # the directory already exists; -ErrorAction Stop surfaces a permission failure
    # as a terminating error rather than a silent skip. The $dir guard skips the
    # call when Split-Path returns '' for a bare filename, avoiding a confusing
    # ParameterBindingValidationException before the download even starts.
    $dir = Split-Path -LiteralPath $DestinationPath
    if ($dir) {
        New-Item -ItemType Directory -Force -Path $dir -ErrorAction Stop | Out-Null
    }
    $tempPath = "$DestinationPath.part-$([guid]::NewGuid().ToString('n'))"
    try {
        Save-HypervHttpFile -Url $Url -OutFile $tempPath
        # ExpectedSha256 may be empty when the caller didn't supply a publisher
        # checksum (TLS-only trust). Skip verification in that case; the on-disk
        # SHA computed by Read-HypervImageFileResult still surfaces as the
        # `sha256` computed attribute for drift detection.
        if ($ExpectedSha256) {
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
        }
        Move-HypervImageFileIntoPlace -StagingPath $tempPath -DestinationPath $DestinationPath
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

# Invoke-HypervDvdSafeReplace replaces $DestinationPath with the bytes at
# $StagingPath when the destination may be locked by a Hyper-V DVD
# attachment on a running VM (Move-Item -Force's delete-then-rename
# fails with "file already exists" against an open handle). Swap-via-
# pivot dance, using Hyper-V's DVD media hot-swap (Set-VMDvdDrive -Path):
#
#   1. Move staging to a sibling pivot ($DestinationPath.swap-<guid>).
#   2. Re-target every matching DVD slot from destination to pivot,
#      which releases Hyper-V's open handle on the destination.
#   3. Copy the now-unlocked pivot bytes to destination.
#   4. Re-target every slot back to destination.
#   5. Remove the pivot.
#
# Not a simpler Set-VMDvdDrive -Path $null detach: on tested benches
# that clears the slot's media, but a subsequent Set-VMDvdDrive at the
# same slot then fails with "object not found," since the slot's
# resolution breaks while its media is transiently empty. Every step
# here keeps Set-VMDvdDrive pointed at a real file.
#
# Cleanup runs in a finally block so a partial failure restores the
# slots to $DestinationPath; the pivot itself is best-effort cleaned
# (a leak is sweepable, not corrupt state).
#
# lint:allow-long-comment
function Invoke-HypervDvdSafeReplace {
    [CmdletBinding()]
    param(
        [Parameter(Mandatory)] [string] $StagingPath,
        [Parameter(Mandatory)] [string] $DestinationPath
    )
    $normalizedDest = ($DestinationPath -replace '/', '\')

    # Per-VM Get-VMDvdDrive, not the -VMName '*' wildcard form: on PS 5.1 / older Hyper-V modules the wildcard form leaves VMName unpopulated, which breaks the downstream Set-VMDvdDrive -VMName lookup.
    $attached = @()
    foreach ($vm in (Get-VM -ErrorAction Stop)) {
        foreach ($dvd in (Get-VMDvdDrive -VMName $vm.Name -ErrorAction Stop)) {
            $p = $dvd.Path
            if ($p -and [string]::Equals(
                    ($p -replace '/', '\'),
                    $normalizedDest,
                    [System.StringComparison]::OrdinalIgnoreCase)) {
                $attached += [pscustomobject]@{
                    VMName             = $vm.Name
                    ControllerNumber   = [int] $dvd.ControllerNumber
                    ControllerLocation = [int] $dvd.ControllerLocation
                }
            }
        }
    }

    if ($attached.Count -eq 0) {
        # No VM holds the lock: same straight-move path as the non-dvd-aware caller.
        Move-HypervImageFileIntoPlace -StagingPath $StagingPath -DestinationPath $DestinationPath
        return
    }

    # Pivot stays a sibling for an atomic same-volume rename/copy; the trailing .iso is required by Set-VMDvdDrive's own path validator.
    $pivotPath = "$normalizedDest.swap-$([guid]::NewGuid().ToString('n')).iso"

    Move-Item -LiteralPath $StagingPath -Destination $pivotPath -ErrorAction Stop

    try {
        # Backslash-normalized path: a forward-slash form (common from HCL) has landed as an empty Path on the drive.
        foreach ($dvd in $attached) {
            Set-VMDvdDrive `
                -VMName             $dvd.VMName `
                -ControllerNumber   $dvd.ControllerNumber `
                -ControllerLocation $dvd.ControllerLocation `
                -Path               $pivotPath `
                -ErrorAction Stop
        }

        # Copy-Item reads the pivot through Hyper-V's FILE_SHARE_READ open mode; -Force overwrites leftover bytes.
        Copy-Item -LiteralPath $pivotPath -Destination $DestinationPath -Force -ErrorAction Stop
    }
    finally {
        # SilentlyContinue: a failed restore leaves the slot on the pivot, which the next Read surfaces as drift.
        foreach ($dvd in $attached) {
            Set-VMDvdDrive `
                -VMName             $dvd.VMName `
                -ControllerNumber   $dvd.ControllerNumber `
                -ControllerLocation $dvd.ControllerLocation `
                -Path               $normalizedDest `
                -ErrorAction SilentlyContinue
        }

        # Best-effort: an unremovable pivot (still locked) is recoverable on the next apply or a manual sweep.
        Remove-Item -LiteralPath $pivotPath -Force -ErrorAction SilentlyContinue
    }
}

# New-HypervImageFileFromLocalPath verifies a file the Go-side StreamFile
# primitive has just deposited at staging_path, then atomic-renames it to
# destination_path on hash match (same verify-then-rename structure as
# url mode, only the origin of the staged bytes differs). ReplaceWhileMounted
# opts the Move-Item step into the detach-write-attach dance via
# Invoke-HypervDvdSafeReplace, for callers placing files that may be
# mounted as a Hyper-V DVD on a running VM.
function New-HypervImageFileFromLocalPath {
    [CmdletBinding()]
    param(
        [Parameter(Mandatory)] [string] $DestinationPath,
        [Parameter(Mandatory)] [string] $StagingPath,
        [Parameter(Mandatory)] [string] $ExpectedSha256,
        [switch]                        $ReplaceWhileMounted
    )
    # Create the destination directory if absent. Same -Force/-ErrorAction Stop
    # pattern as url-mode: idempotent when the directory already exists, explicit
    # failure on permission errors rather than a confusing DirectoryNotFoundException
    # from the downstream Move-Item. The $dir guard skips the call when Split-Path
    # returns '' for a bare filename.
    $dir = Split-Path -LiteralPath $DestinationPath
    if ($dir) {
        New-Item -ItemType Directory -Force -Path $dir -ErrorAction Stop | Out-Null
    }
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
        if ($ReplaceWhileMounted) {
            Invoke-HypervDvdSafeReplace -StagingPath $StagingPath -DestinationPath $DestinationPath
        }
        else {
            Move-HypervImageFileIntoPlace -StagingPath $StagingPath -DestinationPath $DestinationPath
        }
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

# New-HypervImageFileFromSourcePath copies a host-local file at $SourcePath
# into $DestinationPath. Staging through a sibling .part keeps the final
# Move-Item on one NTFS volume (atomic) even when $SourcePath is on another.
# Verify-and-rename delegates to New-HypervImageFileFromLocalPath -- once
# the bytes are staged the two modes are the same operation. $ExpectedSha256
# is the hash the Go side read from $SourcePath at plan time, so a mismatch
# means the source changed between plan and apply.
function New-HypervImageFileFromSourcePath {
    [CmdletBinding()]
    param(
        [Parameter(Mandatory)] [string] $DestinationPath,
        [Parameter(Mandatory)] [string] $SourcePath,
        [Parameter()]          [string] $ExpectedSha256 = '',
        [switch]                        $ReplaceWhileMounted
    )
    if (-not (Test-Path -LiteralPath $SourcePath -PathType Leaf)) {
        $exception = [System.Management.Automation.ItemNotFoundException]::new(
            "Image file source not found at path '$SourcePath'.")
        $errorRecord = [System.Management.Automation.ErrorRecord]::new(
            $exception, 'ImageFileSourceNotFound',
            [System.Management.Automation.ErrorCategory]::ObjectNotFound, $SourcePath)
        throw $errorRecord
    }
    # An absent expectation means the caller could not hash the source at
    # plan time -- it did not exist yet, because the same apply is creating
    # it. Hash it here instead so the copy is still verified rather than
    # trusted; the delegate's comparison is the only integrity check the
    # bytes get.
    if (-not $ExpectedSha256) {
        $ExpectedSha256 = (Get-FileHash -LiteralPath $SourcePath -Algorithm SHA256).Hash.ToLowerInvariant()
    }
    $dir = Split-Path -LiteralPath $DestinationPath
    if ($dir) {
        New-Item -ItemType Directory -Force -Path $dir -ErrorAction Stop | Out-Null
    }
    $stagingPath = "$DestinationPath.part-$([guid]::NewGuid().ToString('n'))"
    try {
        Copy-Item -LiteralPath $SourcePath -Destination $stagingPath -Force -ErrorAction Stop
    }
    catch {
        # The delegate's finally-block never runs here, so a partial .part
        # from a failed Copy-Item has to be cleaned up on this path.
        if (Test-Path -LiteralPath $stagingPath) {
            Remove-Item -LiteralPath $stagingPath -Force -ErrorAction SilentlyContinue
        }
        throw
    }
    New-HypervImageFileFromLocalPath `
        -DestinationPath      $DestinationPath `
        -StagingPath          $stagingPath `
        -ExpectedSha256       $ExpectedSha256 `
        -ReplaceWhileMounted:$ReplaceWhileMounted
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
        $params = Read-HypervStdinPayload

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
                # replace_while_mounted defaults to absent (false).
                # ConvertFrom-Json silently emits $null for missing keys under
                # StrictMode 3, so the explicit PSObject.Properties probe avoids
                # a property-not-found at parse time and keeps the flag opt-in
                # for callers that don't set it (currently: image_file's url
                # and local_path direct uses; only iso_volume sets it true).
                $detachFlag = $false
                if ($params.PSObject.Properties.Name -contains 'replace_while_mounted') {
                    $detachFlag = [bool] $params.replace_while_mounted
                }
                New-HypervImageFileFromLocalPath `
                    -DestinationPath                 $params.destination_path `
                    -StagingPath                     $params.staging_path `
                    -ExpectedSha256                  $params.expected_sha256 `
                    -ReplaceWhileMounted:$detachFlag
            }
            'source_path' {
                $detachFlag = $false
                if ($params.PSObject.Properties.Name -contains 'replace_while_mounted') {
                    $detachFlag = [bool] $params.replace_while_mounted
                }
                New-HypervImageFileFromSourcePath `
                    -DestinationPath                 $params.destination_path `
                    -SourcePath                      $params.source_path `
                    -ExpectedSha256                  $params.expected_sha256 `
                    -ReplaceWhileMounted:$detachFlag
            }
            default {
                throw "Unknown source_mode '$($params.source_mode)'; expected 'url', 'host_path', 'local_path', or 'source_path'."
            }
        }
    }
    catch {
        Write-HypervError $_
        exit 1
    }
}
