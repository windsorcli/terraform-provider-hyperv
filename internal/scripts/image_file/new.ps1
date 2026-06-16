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
#   url        - download via HttpWebRequest to a sibling .part file in the
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
# Why HttpWebRequest (not HttpClient, BITS, or Invoke-WebRequest):
#   - HttpClient on .NET Framework 4.x (PS 5.1 / Server 2019) fails TLS
#     handshake with "Could not create SSL/TLS secure channel" against some
#     HTTPS endpoints (confirmed against factory.talos.dev on WS2019).
#     HttpWebRequest uses the same Schannel path as Invoke-WebRequest and
#     works correctly on both PS 5.1 and PS 7.
#   - Start-BitsTransfer requires an interactive user session (HRESULT
#     0x800704DD over SSH/WinRM "Network" logon).
#   - Invoke-WebRequest -OutFile on PS 5.1 buffers the response body in
#     memory before writing to disk -- fine for small files, OOMs on the
#     multi-GB VHDX images this resource exists to fetch.

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
    # Explicitly pin TLS 1.2 via ServicePointManager — WS2019 defaults can
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

# Invoke-HypervDvdSafeReplace replaces $DestinationPath with the bytes at
# $StagingPath when the destination may currently be locked by a Hyper-V
# DVD attachment on a running VM. `Move-Item -Force` does delete-then-
# rename; on a destination with an exclusive open handle the delete is
# pended and the rename surfaces "Cannot create a file when that file
# already exists." Hyper-V supports DVD media hot-swap on running VMs
# (Set-VMDvdDrive -Path <new>), so we use a swap-via-pivot dance:
#
#   1. Move staging from $StagingPath to a sibling pivot file
#      ($DestinationPath.swap-<guid>). Rename within the directory is
#      lock-free here because no VM has the staging path mounted.
#   2. Re-target every matching DVD slot from $DestinationPath to the
#      pivot. Hyper-V atomically releases its open handle on the old
#      destination as the new media takes effect.
#   3. With $DestinationPath now unlocked, copy the pivot bytes to it.
#      Copy-Item reads the pivot through Hyper-V's FILE_SHARE_READ and
#      writes a fresh file at the destination -- no rename of the locked
#      pivot needed.
#   4. Re-target every slot back to $DestinationPath. Hyper-V picks up
#      the new bytes there and releases the lock on the pivot.
#   5. Remove the pivot.
#
# Why not the more obvious detach-via-Set-VMDvdDrive-Path-null? On the
# tested benches (Server 2022, Hyper-V module shipped with PS 5.1), a
# Path=$null call clears the media on the slot, but a subsequent
# Set-VMDvdDrive at the same (VMName, ControllerNumber, ControllerLocation)
# tuple surfaces "the object was not found" -- empirically the slot's
# resolution machinery breaks when the drive transiently has no media,
# even though Get-VMDvdDrive still reports the drive as present at the
# slot. Swap-via-pivot keeps every Set-VMDvdDrive call pointed at a
# real existing file, sidestepping the issue entirely.
#
# All cleanup happens in a finally block so a partial failure (Copy-Item
# disk-full, Hyper-V error mid-swap) restores the slots to $DestinationPath
# rather than leaving the VM mounting a soon-deleted pivot. The pivot is
# best-effort cleaned -- a leak is a sweepable artifact, not a corrupt
# state.
function Invoke-HypervDvdSafeReplace {
    [CmdletBinding()]
    param(
        [Parameter(Mandatory)] [string] $StagingPath,
        [Parameter(Mandatory)] [string] $DestinationPath
    )
    $normalizedDest = ($DestinationPath -replace '/', '\')

    # Walk Get-VM and Get-VMDvdDrive per-VM rather than the more concise
    # Get-VMDvdDrive -VMName '*' wildcard form. The wildcard form on PS
    # 5.1 / older Hyper-V module versions returns VMDvdDrive objects with
    # the VMName field unpopulated (the .VM parent is set, but the
    # .VMName scalar that Set-VMDvdDrive's parameter set keys on lands as
    # an empty string), so a downstream Set-VMDvdDrive -VMName $dvd.VMName
    # dispatches to "" and surfaces the cmdlet's stock "object not found"
    # error. The set-boot-order.ps1 path uses the same per-VM enumeration
    # shape -- this matches an existing in-repo pattern known to work
    # against the bench.
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
        # No VM holds the lock -- straight Move-Item -Force, same shape
        # as the non-dvd-aware path.
        Move-Item -LiteralPath $StagingPath -Destination $DestinationPath -Force -ErrorAction Stop
        return
    }

    # Pivot is a sibling so the rename in step 1 stays on the same NTFS
    # volume (atomic) and Copy-Item in step 3 stays in the same directory
    # (no cross-volume slow path). The .swap- prefix groups all in-flight
    # pivots under a single sweep pattern if a future cleanup tool ever
    # needs one. The trailing .iso is required: Set-VMDvdDrive validates
    # the path's extension (rejects "The specified path for the drive is
    # not valid" otherwise) -- the cmdlet enforces .iso even though the
    # iso_volume schema MarkdownDescription notes Hyper-V "doesn't
    # require" the suffix at the storage layer; it is the cmdlet
    # parameter validator that does.
    $pivotPath = "$normalizedDest.swap-$([guid]::NewGuid().ToString('n')).iso"

    Move-Item -LiteralPath $StagingPath -Destination $pivotPath -ErrorAction Stop

    try {
        # Step 2: re-point each slot at the pivot. Each Set-VMDvdDrive is
        # an atomic media swap -- Hyper-V's open handle on $DestinationPath
        # is released as the call returns. Backslash-normalized path
        # because Hyper-V's storage layer canonicalizes; passing the
        # user-form (often forward-slash from HCL) has been observed to
        # land as an empty Path on the drive.
        foreach ($dvd in $attached) {
            Set-VMDvdDrive `
                -VMName             $dvd.VMName `
                -ControllerNumber   $dvd.ControllerNumber `
                -ControllerLocation $dvd.ControllerLocation `
                -Path               $pivotPath `
                -ErrorAction Stop
        }

        # Step 3: $DestinationPath is now unlocked. Copy-Item reads the
        # pivot through Hyper-V's FILE_SHARE_READ open mode (verified
        # against bench at the time of writing) and writes a fresh file
        # at the destination. -Force overwrites any leftover bytes.
        Copy-Item -LiteralPath $pivotPath -Destination $DestinationPath -Force -ErrorAction Stop
    }
    finally {
        # Step 4: restore each slot to $DestinationPath. Best-effort
        # SilentlyContinue here: if step 3 failed and the destination is
        # missing, we still try to put the slot back; if it succeeds the
        # VM keeps its DVD link intact across the failed apply. ErrorAction
        # Stop here would mask the original copy-failure error with a
        # secondary "missing destination" error, which is less useful for
        # diagnosis. The slot is left pointing at the pivot in the worst
        # case -- the Read shape will surface that on the next refresh.
        foreach ($dvd in $attached) {
            Set-VMDvdDrive `
                -VMName             $dvd.VMName `
                -ControllerNumber   $dvd.ControllerNumber `
                -ControllerLocation $dvd.ControllerLocation `
                -Path               $normalizedDest `
                -ErrorAction SilentlyContinue
        }

        # Step 5: clean up the pivot. SilentlyContinue because a failure
        # here (e.g. step 4 didn't actually re-target some slot, so the
        # pivot is still locked) is recoverable on the next apply or via
        # manual sweep -- not worth shadowing the primary outcome with.
        # No Test-Path guard: -ErrorAction SilentlyContinue handles the
        # missing-file case directly and the guard adds a TOCTOU window
        # without protective value.
        Remove-Item -LiteralPath $pivotPath -Force -ErrorAction SilentlyContinue
    }
}

# New-HypervImageFileFromLocalPath verifies a file the Go-side StreamFile
# primitive has just deposited at staging_path, then atomic-renames it to
# destination_path on hash match. Mirrors the url-mode shape: the only
# difference is where the staged bytes came from (Go-side stream vs
# HttpClient download). Same .part-in-destination-dir layout keeps the
# Move-Item atomic on NTFS, same finally-block cleanup keeps a half-baked
# staging file from lingering across a failed apply.
#
# ReplaceWhileMounted opts the Move-Item step into the
# detach-write-attach dance via Invoke-HypervDvdSafeReplace. Callers that
# place files which may be mounted as a Hyper-V DVD on a running VM
# (currently only iso_volume seeds; image_file's vhdx workloads don't
# hot-replace under a VM's HardDiskController) set this flag.
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
            Move-Item -LiteralPath $StagingPath -Destination $DestinationPath -Force -ErrorAction Stop
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
