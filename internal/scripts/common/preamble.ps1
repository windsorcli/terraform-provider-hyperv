# common/preamble.ps1 -- concatenated to the top of every resource script at
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
# Global warning suppression. Stop-VM (already-Off) and Start-VM
# (already-Running) emit a "VM is already in the specified state"
# WARNING that lands on stdout via the SSH transport's stream-merging,
# corrupting the JSON output the Go-side decoder expects.
#
# The cost: every cmdlet in every script runs with warnings silently
# dropped, not just the two cmdlets that motivated this. Deprecation
# notices, "this parameter is ignored," "operation completed with
# residue," etc. are all swallowed. We accept that cost because:
#
#   1. Every script's contract is "JSON on stdout or nothing." A leaked
#      warning anywhere breaks the decoder, not just on the well-known
#      Stop/Start-VM paths -- so a narrower `-WarningAction
#      SilentlyContinue` per call site is a leaky defense.
#   2. The error path is well-served by Write-HypervError on stderr,
#      which is where any genuinely actionable signal belongs anyway.
#   3. Writing to PS warning stream from inside this provider is rare
#      (we don't call Write-Warning ourselves), so we're mostly
#      suppressing third-party module noise we don't need.
#
# If a future cmdlet emits a warning we *do* want to surface, escalate
# it explicitly: capture with `-WarningVariable +w`, inspect, and
# convert to a Write-HypervError or a structured field on the result.
$WarningPreference     = 'SilentlyContinue'
[Console]::OutputEncoding = [System.Text.Encoding]::UTF8
[Console]::InputEncoding  = [System.Text.Encoding]::UTF8
$OutputEncoding           = [System.Text.Encoding]::UTF8

# Write-HypervError emits the structured error envelope on stderr. The
# Go-side hyperv/errors.go maps fields to typed errors per PLAN.md section 5:
#
#   category=ObjectNotFound                          -> ErrNotFound
#   category=ResourceUnavailable                     -> ErrUnavailable
#   category=PermissionDenied                        -> ErrUnauthorized
#   category=InvalidArgument
#     and fullyQualifiedErrorId starts with
#     "InvalidParameter,Microsoft.Vhd.*"             -> ErrInvalidParentPath
#   everything else                                  -> ErrPSExecution
#
# ErrNotFound vs ErrUnavailable is load-bearing on the Go side: ErrNotFound
# triggers RemoveResource (resource is gone), ErrUnavailable surfaces a
# transient error so a vmms restart doesn't cause destroy-and-recreate.
#
# Always pair with `exit 1` so the connection layer sees a non-zero exit
# code as the primary signal; the JSON envelope provides detail.
function Write-HypervError {
    [CmdletBinding()]
    param(
        [Parameter(Mandatory = $true, Position = 0, ValueFromPipeline = $true)]
        $ErrorRecord
    )
    # process block is required for ValueFromPipeline parameters: without it, only the
    # last piped record would be emitted because the function body runs once after the
    # pipeline drains, with $ErrorRecord overwritten on each iteration.
    process {
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
}

# Write-HypervResult is sugar for the standard result emit. The terminal
# `ConvertTo-Json -Depth 10 -Compress` is non-negotiable: ConvertTo-Json's
# default depth=2 silently truncates nested objects to literal strings.
function Write-HypervResult {
    [CmdletBinding()]
    param(
        [Parameter(Mandatory = $true, Position = 0, ValueFromPipeline = $true)]
        $Object
    )
    # Single-object contract: piping multiple items emits multiple top-level
    # JSON values, which Go's json.Unmarshal rejects. For collections, call
    # ConvertTo-Json directly on the array.
    process {
        $Object | ConvertTo-Json -Depth 10 -Compress
    }
}
