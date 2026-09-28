# image_file/sweep.ps1 -- find and remove orphan image files in a
# directory whose name matches a prefix. Acceptance-test sweeper for
# tfacc-* files left behind by a crashed run.
#
# Wire contract (locked by Tests.ps1):
#
#   stdin JSON  : { "parent_dir": "<absolute-path>", "name_prefix": "<string>" }
#   stdout JSON : { "removed": [ "<path>", ... ] } -- always a JSON object
#                 with `removed` array, even on zero matches.
#   stderr/exit : 0 on success. Missing parent_dir is an empty result
#                 (fresh bench), not an error.
#
# Excludes VHD-family extensions (.vhd, .vhdx, .avhd, .avhdx), which are
# the vhd sweeper's territory; this script owns everything else under
# the prefix. A Remove-Item failure on one file logs and continues.
#
# lint:allow-long-comment

function Invoke-HypervImageFileSweep {
    [CmdletBinding()]
    param(
        # ValidateNotNullOrEmpty mirrors netnat/sweep.ps1: blocks the
        # empty-string footgun that would expand $pattern to "*" and
        # sweep every file in the directory.
        [Parameter(Mandatory)] [ValidateNotNullOrEmpty()] [string] $ParentDir,
        [Parameter(Mandatory)] [ValidateNotNullOrEmpty()] [string] $NamePrefix
    )

    # [string[]] typing keeps a single match from unboxing to a scalar in the JSON output (see netnat/sweep.ps1).
    [string[]]$removed = @()

    if (-not (Test-Path -LiteralPath $ParentDir -PathType Container)) {
        $result = [pscustomobject]@{ removed = $removed }
        ConvertTo-Json -InputObject $result -Depth 10 -Compress
        return
    }

    $pattern = "${NamePrefix}*"
    $vhdExtensions = @('.vhd', '.vhdx', '.avhd', '.avhdx')
    $candidates = @(Get-ChildItem -LiteralPath $ParentDir -Filter $pattern -ErrorAction Stop |
        Where-Object { $_ -and -not $_.PSIsContainer -and $vhdExtensions -notcontains $_.Extension.ToLowerInvariant() })

    foreach ($f in $candidates) {
        try {
            Remove-Item -LiteralPath $f.FullName -Force -ErrorAction Stop
            $removed += $f.FullName
        }
        catch {
            Write-Warning ("Remove-Item failed for '{0}': {1}" -f $f.FullName, $_.Exception.Message)
        }
    }

    $result = [pscustomobject]@{ removed = $removed }
    ConvertTo-Json -InputObject $result -Depth 10 -Compress
}

# Entry block. Skipped during Pester runs (dot-source sets InvocationName='.').
if ($MyInvocation.InvocationName -ne '.') {
    try {
        $params = Read-HypervStdinPayload
        Invoke-HypervImageFileSweep -ParentDir $params.parent_dir -NamePrefix $params.name_prefix
    }
    catch {
        Write-HypervError $_
        exit 1
    }
}
