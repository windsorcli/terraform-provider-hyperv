# common/preamble.ps1 — concatenated to the top of every resource script at
# runtime.
#
# Three load-bearing facts here:
#
#   1. $ProgressPreference = 'SilentlyContinue'
#      Without it, every PS 5.1 invocation pollutes stderr with a
#      "#< CLIXML <Objs ...>" envelope. The Go-side stripCLIXML defends
#      against cmdlets that bypass this preference, but suppressing at
#      source eliminates the bulk.
#
#   2. UTF-8 console encoding
#      PS 5.1 defaults stdout to the system codepage. Without this pin,
#      paths with accented characters or any Unicode VM/switch name
#      corrupt to '?' on the wire.
#
#   3. Set-StrictMode -Version 3.0  (specific version, not 'Latest')
#      Catches uninitialized variables and array-index typos that 5.1's
#      lax mode silently swallows. 'Latest' means different things on
#      5.1 vs 7+, so we pin to 3.0 for portability.

Set-StrictMode -Version 3.0
$ErrorActionPreference = 'Stop'
$ProgressPreference    = 'SilentlyContinue'
[Console]::OutputEncoding = [System.Text.Encoding]::UTF8
$OutputEncoding           = [System.Text.Encoding]::UTF8

# Write-HypervError emits the structured error envelope on stderr. The
# Go-side hyperv/errors.go maps fields to typed errors per §5:
#
#   category=ObjectNotFound|ResourceUnavailable      -> ErrNotFound
#   category=PermissionDenied                        -> ErrUnauthorized
#   category=InvalidArgument
#     and fullyQualifiedErrorId starts with
#     "InvalidParameter,Microsoft.Vhd.*"             -> ErrInvalidParentPath
#   everything else                                  -> ErrPSExecution
#
# Always pair with `exit 1` so the connection layer sees a non-zero exit
# code as the primary signal; the JSON envelope provides detail.
function Write-HypervError {
    [CmdletBinding()]
    param(
        [Parameter(Mandatory = $true, Position = 0, ValueFromPipeline = $true)]
        $ErrorRecord
    )
    $payload = [ordered]@{
        message               = $ErrorRecord.Exception.Message
        category              = $ErrorRecord.CategoryInfo.Category.ToString()
        fullyQualifiedErrorId = $ErrorRecord.FullyQualifiedErrorId
        cmdlet                = $ErrorRecord.CategoryInfo.Activity
        targetObject          = $ErrorRecord.CategoryInfo.TargetName
    }
    $json = $payload | ConvertTo-Json -Depth 5 -Compress
    [Console]::Error.WriteLine($json)
}

# Write-HypervResult is sugar for the standard result emit. The terminal
# `ConvertTo-Json -Depth 10 -Compress` pattern is locked in by spike #2 —
# default depth=2 silently truncates nested objects to literal strings.
function Write-HypervResult {
    [CmdletBinding()]
    param(
        [Parameter(Mandatory = $true, Position = 0, ValueFromPipeline = $true)]
        $Object
    )
    $Object | ConvertTo-Json -Depth 10 -Compress
}
