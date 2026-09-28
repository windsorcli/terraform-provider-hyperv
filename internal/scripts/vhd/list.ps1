# vhd/list.ps1 -- enumerate VHD/VHDX files in a directory matching a prefix.
#
# Wire contract (locked in by Tests.ps1):
#
#   stdin JSON  : { "parent_dir": "<absolute-path>", "name_prefix": "<string>" }
#   stdout JSON : [ { "path": "<absolute-path>" }, ... ] -- always a JSON
#                 array, even on zero or one match.
#   stderr/exit : 0 on success. A missing parent_dir is a normal empty
#                 result (the acctest fixture directory may not exist
#                 on a fresh bench), not an error.
#
# Used by the acceptance-test sweeper to find tfacc-* VHDs left behind
# after a crashed run. Filters by extension family (.vhd, .vhdx, .avhd,
# .avhdx) so it doesn't collide with the image_file sweeper's tfacc-*
# files. Get-ChildItem, not Get-VHD, since Get-VHD needs a known path
# and can't enumerate.
#
# lint:allow-long-comment

# Get-HypervVHDByPrefix returns the paths of VHD/VHDX files under
# $ParentDir whose name starts with $NamePrefix.
function Get-HypervVHDByPrefix {
    [CmdletBinding()]
    param(
        [Parameter(Mandatory)] [string] $ParentDir,
        [Parameter(Mandatory)] [string] $NamePrefix
    )

    # Missing parent dir is a normal empty result, not an error (fresh bench may lack it).
    if (-not (Test-Path -LiteralPath $ParentDir -PathType Container)) {
        ConvertTo-Json -InputObject @() -Depth 10 -Compress
        return
    }

    # No -File on Get-ChildItem: Pester's Mock can't bind it; the extension filter excludes directories anyway.
    $pattern = "${NamePrefix}*"
    $vhdExtensions = @('.vhd', '.vhdx', '.avhd', '.avhdx')
    $results = @(Get-ChildItem -LiteralPath $ParentDir -Filter $pattern -ErrorAction Stop |
        Where-Object { $vhdExtensions -contains $_.Extension.ToLowerInvariant() } |
        ForEach-Object { [pscustomobject]@{ Path = $_.FullName } })

    # -InputObject keeps the output array-typed even for zero/one match (see vm/list.ps1).
    ConvertTo-Json -InputObject $results -Depth 10 -Compress
}

# Entry block. Skipped during Pester runs (dot-source sets InvocationName='.').
if ($MyInvocation.InvocationName -ne '.') {
    try {
        $params = Read-HypervStdinPayload
        Get-HypervVHDByPrefix -ParentDir $params.parent_dir -NamePrefix $params.name_prefix
    }
    catch {
        Write-HypervError $_
        exit 1
    }
}
