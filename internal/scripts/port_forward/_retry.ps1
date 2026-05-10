# port_forward/_retry.ps1 -- shared dup-name retry helper for the
# port_forward verb scripts. Underscore prefix keeps the file out of
# Pester's *.Tests.ps1 discovery glob (same convention
# _test_helpers.ps1 uses); the runtime concatenates this body to the
# top of new.ps1 and set.ps1 via loadPortForwardWithRetry on the Go
# side, parallel to how vm/read-result.ps1 is prepended to the four
# VM read-emitting verbs.
#
# Until 2026-05 each verb script inlined Invoke-WithDupNameRetry
# verbatim. Lifting the function out of both into this single
# canonical copy reduces the bug surface (two copies could drift on
# the next backoff tweak) at the cost of one extra fs read per RunScript.

# Invoke-WithDupNameRetry retries $Action on the transient Win32
# ERROR_DUP_NAME (HRESULT 0x80070034) that Add-NetNatStaticMapping's
# underlying NetSetup/WMI layer occasionally surfaces under concurrent
# pressure on Server 2016+. The cmdlet is idempotent on retry -- the
# duplicate-name signal is layer-below misreporting, not a real
# collision. Backoff schedule 250ms, 500ms, 1s caps total wait at
# ~1.75s before bubbling up. Anything not matching the signature
# re-throws on the first attempt.
function Invoke-WithDupNameRetry {
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
            $isTransient = ($_.Exception.HResult -eq -2147024844) -or
                           ($_.Exception.Message -match 'ERROR_DUP_NAME|duplicate name')
            if (-not $isTransient -or $attempt -ge $delays.Length) { throw }
            Start-Sleep -Milliseconds $delays[$attempt]
        }
    }
}
