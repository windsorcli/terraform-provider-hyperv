# nat_static_mapping/_retry.ps1 -- shared transient-retry helper for the
# nat_static_mapping verb scripts. Underscore prefix keeps the file out
# of Pester's *.Tests.ps1 discovery glob; the Go side concatenates this
# body to the top of new.ps1 and set.ps1.

# Invoke-WithNetNatRetry retries $Action on the two transient Win32
# error classes Add-NetNatStaticMapping's NetSetup/WMI layer surfaces
# under concurrent pressure: ERROR_DUP_NAME (0x80070034/-2147024844, a
# layer-below misreport, not a real collision) and
# ERROR_SHARING_VIOLATION (0x80070020/-2147024864, from concurrent
# applies racing the same NetNat persistent-store handle). Anything
# else re-throws immediately. Backoff 250ms/500ms/1s caps total wait
# at ~1.75s.
#
# lint:allow-long-comment

# Resolve-NetNatPortConflictMessage rewrites Add-NetNatStaticMapping's
# misleading "file is being used by another process" error into a clear
# diagnostic when the real cause is a Windows TCP port-exclusion range
# collision (same Win32 ERROR_SHARING_VIOLATION signature, unrelated to
# files). Returns the input ErrorRecord unchanged when the signature
# doesn't match, so it's safe to call unconditionally from a catch block.
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
            # Port-exclusion failures are deterministic: skip the retry cycle and let the outer catch translate.
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
        # Lets Pester inject mock netsh output; production callers omit it and the function invokes netsh directly.
        [Parameter()] [string[]] $ExcludedRangesText
    )

    # FQEId-only match: "Windows System Error 32" is how the WMI/CIM
    # port-reservation path reuses the ERROR_SHARING_VIOLATION code to
    # mean "port is in an exclusion range." HResult and message text
    # also fire for real file-handle contention, so matching on those
    # too would mis-tag a genuine concurrency failure as a port
    # conflict. A non-English host with different FQEId text simply
    # skips translation, which is the lesser harm.
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
