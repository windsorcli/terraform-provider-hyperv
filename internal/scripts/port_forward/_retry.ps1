# port_forward/_retry.ps1 -- shared transient-retry helper for the
# port_forward verb scripts. Underscore prefix keeps the file out of
# Pester's *.Tests.ps1 discovery glob (same convention
# _test_helpers.ps1 uses); the runtime concatenates this body to the
# top of new.ps1 and set.ps1 via loadPortForwardWithRetry on the Go
# side, parallel to how vm/read-result.ps1 is prepended to the four
# VM read-emitting verbs.
#
# Until 2026-05 each verb script inlined the helper verbatim. Lifting
# the function out of both into this single canonical copy reduces the
# bug surface (two copies could drift on the next backoff tweak) at the
# cost of one extra fs read per RunScript.

# Invoke-WithNetNatRetry retries $Action on the two transient Win32
# error classes Add-NetNatStaticMapping's underlying NetSetup/WMI layer
# surfaces under concurrent pressure on Server 2016+:
#
#   * ERROR_DUP_NAME           (HRESULT 0x80070034, signed int32 -2147024844).
#     Duplicate-name signal from a layer-below misreport, not a real
#     collision. Retry is idempotent on the cmdlet level.
#   * ERROR_SHARING_VIOLATION  (HRESULT 0x80070020, signed int32 -2147024864).
#     "The process cannot access the file because it is being used by
#     another process." Surfaces when several Add-NetNatStaticMapping
#     calls race the same NetNat persistent-store handle (terraform's
#     default parallelism=10 hits this once you have 5+ port forwards).
#     The contended resource releases in tens of milliseconds; retry
#     resolves it cleanly.
#
# Anything not matching either signature re-throws on the first attempt.
# Backoff schedule 250ms, 500ms, 1s caps total wait at ~1.75s before
# bubbling up.
#
# Resolve-NetNatPortConflictMessage rewrites the misleading "the file
# is being used by another process" error that Add-NetNatStaticMapping
# raises when the requested external port falls inside a Windows TCP
# port-exclusion range. The cmdlet surfaces Win32 ERROR_SHARING_VIOLATION
# from the kernel-layer port reservation check; the message has nothing
# to do with files. Operators see "shared file?" and burn hours chasing
# the wrong root cause. This function detects the signature and emits a
# clear, self-diagnostic error pointing at the actual cause (the port is
# in a system-allocated exclusion range, typically grown over uptime by
# HTTP.sys / RPC / Hyper-V VMBus).
#
# Returns the input ErrorRecord unchanged if the signature doesn't match
# (so the helper can be unconditionally invoked from catch blocks).
function Invoke-WithNetNatRetry {
    [CmdletBinding()]
    param(
        [Parameter(Mandatory)] [scriptblock] $Action
    )
    $delays = @(250, 500, 1000)
    for ($attempt = 0; $attempt -le $delays.Length; $attempt++) {
        try {
            return & $Action
        }
        catch {
            $hresult = $_.Exception.HResult
            $message = $_.Exception.Message
            $isTransient = ($hresult -eq -2147024844) -or
                           ($hresult -eq -2147024864) -or
                           ($message -match 'ERROR_DUP_NAME|duplicate name') -or
                           ($message -match 'being used by another process|ERROR_SHARING_VIOLATION')
            if (-not $isTransient -or $attempt -ge $delays.Length) { throw }
            Start-Sleep -Milliseconds $delays[$attempt]
        }
    }
}

function Resolve-NetNatPortConflictMessage {
    [CmdletBinding()]
    param(
        [Parameter(Mandatory)] $ErrorRecord,
        [Parameter(Mandatory)] [string] $Protocol,
        [Parameter(Mandatory)] [int]    $ExternalPort,
        # Optional injection point so Pester tests can supply mock netsh
        # output without depending on netsh existing on the test runner.
        # Production callers omit; the function falls back to invoking
        # netsh directly. The injection is plumbed via parameter rather
        # than Pester's Mock because Pester 5's mocking can't reach
        # dot-sourced cross-function calls reliably.
        [Parameter()] [string[]] $ExcludedRangesText
    )

    # Signature match: Win32 ERROR_SHARING_VIOLATION (HRESULT 0x80070020,
    # signed -2147024864) surfaced by Add-NetNatStaticMapping. The
    # FullyQualifiedErrorId carries "Windows System Error 32" -- 32 is the
    # canonical Win32 ERROR_SHARING_VIOLATION code, which the WMI/CIM
    # layer reuses to mean "this port can't be reserved because it's
    # already in an exclusion range." Detect with belt-and-suspenders
    # checks: HResult, FQEId, and message-substring all point at the
    # same underlying error class.
    $hresult = $ErrorRecord.Exception.HResult
    $fqeid   = $ErrorRecord.FullyQualifiedErrorId
    $message = $ErrorRecord.Exception.Message
    # Don't name this $matches -- that's a PowerShell automatic variable
    # populated by every -match operator, and the right-hand side here
    # uses -match three times. Writing to the auto var while the same
    # expression is mutating it is a Set-StrictMode 3.0 footgun (the
    # final value isn't the boolean -or chain you intended). Of the
    # three checks, only the HResult path is locale-safe; the FQEId
    # and message strings localize on non-English Windows hosts. We
    # accept that as a degradation rather than a regression -- the
    # HResult leg always fires for the canonical case.
    $isPortConflict = ($hresult -eq -2147024864) -or
                      ($fqeid -match 'Windows System Error 32') -or
                      ($message -match 'being used by another process')
    if (-not $isPortConflict) {
        return $ErrorRecord
    }

    # Look up the conflicting exclusion range. netsh's output is fixed-
    # column-width text; skip the header rows and parse remaining rows
    # as "<start> <end>". A non-numeric or short row is silently skipped
    # (defensive against future netsh format tweaks).
    $protoLower = $Protocol.ToLowerInvariant()
    $rangeText = ''
    try {
        if ($null -eq $ExcludedRangesText) {
            $ExcludedRangesText = & netsh interface ipv4 show excludedportrange protocol=$protoLower 2>&1 | ForEach-Object { $_.ToString() }
        }
        foreach ($line in $ExcludedRangesText) {
            $parts = $line -split '\s+' | Where-Object { $_ -ne '' }
            if ($parts.Count -lt 2) { continue }
            $start = 0; $end = 0
            if (-not [int]::TryParse($parts[0], [ref]$start)) { continue }
            if (-not [int]::TryParse($parts[1], [ref]$end))   { continue }
            if ($ExternalPort -ge $start -and $ExternalPort -le $end) {
                $rangeText = " (exclusion range $start-$end)"
                break
            }
        }
    } catch {
        # Best-effort lookup. If netsh fails for any reason, fall through
        # and emit the generic-but-still-clearer message without a range.
        $rangeText = ''
    }

    $clearMessage = (
        "external_port $ExternalPort is in a Windows $($Protocol.ToUpper()) " +
        "exclusion range$rangeText, so Add-NetNatStaticMapping cannot bind it. " +
        "Windows dynamically grows these ranges over uptime as services " +
        "(HTTP.sys, RPC, Hyper-V VMBus, etc.) request port pools from the " +
        "dynamic-port range. Either choose an external port below the " +
        "dynamic-port floor (default 49152, check 'netsh interface ipv4 " +
        "show dynamicportrange tcp') or run 'netsh interface ipv4 show " +
        "excludedportrange $protoLower' to see all conflicting ranges. " +
        "The original error was misleadingly surfaced by the WMI layer as " +
        "'The process cannot access the file because it is being used by " +
        "another process.' -- there is no file lock; the port is reserved."
    )

    # Synthesize a new ErrorRecord carrying the clearer message but
    # preserving the category info (NotSpecified) and FQEId tail so
    # downstream Go-side error mapping behaves identically.
    $exception = [System.IO.InvalidDataException]::new($clearMessage)
    $newRecord = [System.Management.Automation.ErrorRecord]::new(
        $exception,
        'PortForwardPortExcluded',
        $ErrorRecord.CategoryInfo.Category,
        $ErrorRecord.TargetObject
    )
    return $newRecord
}
