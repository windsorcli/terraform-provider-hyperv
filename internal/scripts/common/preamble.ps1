# Concatenated to the top of every resource script at runtime. Pins
# $ProgressPreference (PS 5.1 otherwise leaks CLIXML progress envelopes
# to stderr) and console encoding to UTF-8 (5.1 defaults stdout to the
# system codepage, corrupting non-ASCII VM/switch names) and StrictMode
# to a fixed version (3.0 behaves the same on 5.1 and 7+; 'Latest' does not).

Set-StrictMode -Version 3.0
$ErrorActionPreference = 'Stop'
$ProgressPreference    = 'SilentlyContinue'
# Global suppression: Stop-VM/Start-VM no-op warnings land on stdout via
# the SSH transport and corrupt the JSON the Go-side decoder expects.
$WarningPreference     = 'SilentlyContinue'
[Console]::OutputEncoding = [System.Text.Encoding]::UTF8
[Console]::InputEncoding  = [System.Text.Encoding]::UTF8
$OutputEncoding           = [System.Text.Encoding]::UTF8

# Write-HypervError emits the structured error envelope on stderr. The
# Go side (internal/hyperv/errors.go) maps fields to typed errors:
#
#   category=ObjectNotFound                      -> ErrNotFound
#   category=ResourceUnavailable                 -> ErrUnavailable
#   category=PermissionDenied                    -> ErrUnauthorized
#   category=InvalidArgument, fullyQualifiedErrorId
#     starting "InvalidParameter,Microsoft.Vhd.*" -> ErrInvalidParentPath
#   everything else                              -> ErrPSExecution
#
# Callers pair this with `exit 1`; the exit code is the primary signal,
# the JSON envelope is the detail.
#
# lint:allow-long-comment
function Write-HypervError {
    [CmdletBinding()]
    param(
        [Parameter(Mandatory = $true, Position = 0, ValueFromPipeline = $true)]
        $ErrorRecord
    )
    # process block runs per pipeline item; without it only the last piped record would emit.
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

# Read-HypervStdinPayload reads and parses the JSON payload delivered on stdin.
# Returns a PSCustomObject with the script's input fields. Throws with a
# descriptive error when stdin is empty (WinRM transient delivery failure)
# rather than letting strict-mode 3.0 surface a misleading
# "The property 'X' cannot be found on this object" when the caller accesses
# any field on the null result of `ConvertFrom-Json ""`.
function Read-HypervStdinPayload {
    $raw = [Console]::In.ReadToEnd()
    if ([string]::IsNullOrWhiteSpace($raw)) {
        throw "hyperv: stdin was empty; WinRM may have failed to deliver the script input"
    }
    return $raw | ConvertFrom-Json
}
