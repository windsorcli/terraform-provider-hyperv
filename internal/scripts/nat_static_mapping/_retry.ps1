# nat_static_mapping/_retry.ps1 -- shared transient-retry helper for the
# nat_static_mapping verb scripts. Underscore prefix keeps the file out of
# Pester's *.Tests.ps1 discovery glob (same convention
# _test_helpers.ps1 uses); the runtime concatenates this body to the
# top of new.ps1 and set.ps1 via loadNatStaticMappingWithRetry on the Go
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
            # Short-circuit on port-exclusion-range failures: the FQEId
            # "Windows System Error 32" is the WMI port-reservation
            # signature and is deterministic -- no amount of retry will
            # succeed against a port that's in a reserved range. Re-
            # throw so the outer catch in new.ps1 / set.ps1 routes the
            # error through Resolve-NetNatPortConflictMessage for the
            # clearer diagnostic. Symmetric with that translator's
            # FQEId-only signature -- the same signal gates retry-no
            # here and translate-yes there. Without this short-circuit
            # the retry helper burns the full 250+500+1000ms cycle on
            # deterministic failures before the diagnostic surfaces.
            if ($_.FullyQualifiedErrorId -match 'Windows System Error 32') { throw }
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
    # surfaced by Add-NetNatStaticMapping. The WMI/CIM port-reservation
    # path sets FullyQualifiedErrorId to "Windows System Error 32" --
    # 32 is the Win32 ERROR_SHARING_VIOLATION code, which this layer
    # reuses to mean "the port can't be reserved because it's in an
    # exclusion range." That FQEId is the discriminating signal.
    #
    # Why FQEId-only and not also HResult / message-substring: the
    # HResult (-2147024864) and the message text "being used by
    # another process" both fire for the SAME Win32 error code surfaced
    # by raw file-handle contention -- a real concern when the Go-side
    # netNatMu has exhausted its retries against a process invoking
    # NetNat outside the mutex's reach. Including those legs would
    # mis-tag a genuine concurrent-access exhaustion as "port in
    # exclusion range" and send operators down the wrong diagnostic
    # path. Locale degradation (FQEId is in the system language;
    # non-English hosts skip the translation and see the cmdlet's bare
    # error) is the lesser harm: missing the translation just leaves
    # the original misleading message intact, whereas mis-tagging
    # actively misleads.
    $fqeid = $ErrorRecord.FullyQualifiedErrorId
    if ($fqeid -notmatch 'Windows System Error 32') {
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
    # InvalidOperationException, not InvalidDataException: the port being
    # in an OS-reserved exclusion range is an operational precondition
    # failure, not malformed data. The Go-side error mapper reads only
    # the message text today (no practical impact from the base type),
    # but the type is a contract for any future typed-catch logic and
    # for a human inspecting the record in a debugger.
    $exception = [System.InvalidOperationException]::new($clearMessage)
    $newRecord = [System.Management.Automation.ErrorRecord]::new(
        $exception,
        'NatStaticMappingPortExcluded',
        $ErrorRecord.CategoryInfo.Category,
        $ErrorRecord.TargetObject
    )
    return $newRecord
}
