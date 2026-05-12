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
# after a crashed run. Filters by both name prefix AND extension family
# (.vhd, .vhdx, .avhd, .avhdx -- the four VHD variants Hyper-V uses) so
# the sweeper doesn't trip over non-VHD tfacc-* files (e.g. seed ISOs
# or fixture text files), which the image_file sweeper owns.
#
# Why path-based enumeration instead of Get-VHD: Get-VHD requires a path
# argument and doesn't enumerate. Get-ChildItem on the parent dir is
# the only way to find files we don't know the names of yet. The
# acctest convention is HYPERV_TEST_VHD_DIR/tfacc-*.vhdx; the sweeper
# threads that env var as parent_dir on the caller side.

# Get-HypervVHDByPrefix returns the paths of VHD/VHDX files under
# $ParentDir whose name starts with $NamePrefix.
function Get-HypervVHDByPrefix {
    [CmdletBinding()]
    param(
        [Parameter(Mandatory)] [string] $ParentDir,
        [Parameter(Mandatory)] [string] $NamePrefix
    )

    # Missing parent dir = empty result, not an error. The acctest
    # fixture dir is created lazily by the first test run that uses
    # it, so a fresh bench legitimately has nothing here.
    if (-not (Test-Path -LiteralPath $ParentDir -PathType Container)) {
        ConvertTo-Json -InputObject @() -Depth 10 -Compress
        return
    }

    # Two filters: name prefix and VHD extension family. The extension
    # filter is what keeps the VHD sweeper from stomping on the
    # image_file sweeper's territory; .vhd / .vhdx are the persistent
    # disk formats, .avhd / .avhdx are the auto-managed differencing
    # disks Hyper-V creates for checkpoints.
    # Two filters: name prefix and VHD extension family. We DON'T pass
    # -File to Get-ChildItem because Pester's Mock loses non-positional
    # parameter signatures and tests can't bind through it; the
    # extension filter naturally excludes directories anyway (a folder
    # named "tfacc-foo" has no .vhd* extension).
    $pattern = "${NamePrefix}*"
    $vhdExtensions = @('.vhd', '.vhdx', '.avhd', '.avhdx')
    $results = @(Get-ChildItem -LiteralPath $ParentDir -Filter $pattern -ErrorAction Stop |
        Where-Object { $vhdExtensions -contains $_.Extension.ToLowerInvariant() } |
        ForEach-Object { [pscustomobject]@{ Path = $_.FullName } })

    # -InputObject keeps the shape array-typed -- see vm/list.ps1 for
    # the full rationale.
    ConvertTo-Json -InputObject $results -Depth 10 -Compress
}

# Entry block. Skipped during Pester runs (dot-source sets InvocationName='.').
if ($MyInvocation.InvocationName -ne '.') {
    try {
        $params = [Console]::In.ReadToEnd() | ConvertFrom-Json
        Get-HypervVHDByPrefix -ParentDir $params.parent_dir -NamePrefix $params.name_prefix
    }
    catch {
        Write-HypervError $_
        exit 1
    }
}
