---
name: powershell-scripter
description: Author and maintain the embedded PowerShell scripts under internal/scripts/. Use when editing any *.ps1 file or the common preamble. Knows the §5 contract this provider locks in — Windows PowerShell 5.1 syntax floor, also tested on 7.4; -EncodedCommand for script body; UTF-8 stdout pin; $ProgressPreference suppression; structured JSON error envelope; ConvertTo-Json -Depth 10 -Compress; CIM cmdlets not WMI; ToString('o') for DateTime. These rules came from spikes #2/#3/#4 and are load-bearing — most are not nice-to-haves. Not for Go code (use provider-author) or tests (use test-engineer, though Pester tests are mentioned here as the contract-locking layer).
paths: internal/scripts/**/*.ps1, internal/scripts/**/*.psd1, internal/scripts/**/*.psm1, internal/scripts/**/*.Tests.ps1
---

# PowerShell Scripter

## Apply when
- Creating or editing any `*.ps1` under `internal/scripts/`.
- Editing `internal/scripts/common/preamble.ps1` or other shared helpers.
- Designing the JSON shape a script emits or consumes.
- Mapping an existing PowerShell pattern from elsewhere into this contract.

## Do not apply when
- Editing Go code in `internal/hyperv/` that wraps these scripts — `provider-author` covers the Go side. (Both skills together is fine when changing the contract.)
- Reviewing changes pre-commit — `review-pr`.

## Hard syntax floor: Windows PowerShell 5.1

Hyper-V hosts ship with PS 5.1 — that's our floor. PS 7+ is opt-in and not commonly installed. Scripts must run unchanged on both 5.1 and 7.4. The PSScriptAnalyzer config (`.psscriptanalyzer.psd1`) enforces this with `PSUseCompatibleSyntax` and `PSUseCompatibleCmdlets` rules targeting both versions.

### Forbidden 7+-only constructs
- `ConvertFrom-Json -AsHashtable` — use the `Properties.Name -contains` pattern below
- `Get-WmiObject`, `Set-WmiInstance`, `Invoke-WmiMethod` — **removed** in PS 7. Use CIM cmdlets.
- Ternary operator `?:`, null-coalescing `??`, pipeline parallelism `ForEach-Object -Parallel`
- `Set-StrictMode -Version Latest` — pin a specific version (`3.0`), `Latest` means different things on 5.1 vs 7+

## The mandatory preamble

Every script gets `internal/scripts/common/preamble.ps1` concatenated at the top at embed time. Its contents:

```powershell
Set-StrictMode -Version 3.0
$ErrorActionPreference = 'Stop'
$ProgressPreference    = 'SilentlyContinue'   # PS 5.1 emits CLIXML progress on stderr otherwise
[Console]::OutputEncoding = [System.Text.Encoding]::UTF8
$OutputEncoding           = [System.Text.Encoding]::UTF8
```

Three load-bearing facts here:

1. **`$ProgressPreference = 'SilentlyContinue'`** — without it, every PS 5.1 invocation pollutes stderr with a `#< CLIXML <Objs ...>` envelope. The Go-side stderr parser still strips these defensively (some cmdlets bypass the preference), but the preamble eliminates the bulk. ([Spike #2](../../../docs/spikes/02-json-contract.md))
2. **UTF-8 console encoding** — without it, paths with accented characters or any Unicode VM/switch name corrupt to `?` on the wire. Default 5.1 stdout is the system codepage. ([Spike #2 finding 4](../../../docs/spikes/02-json-contract.md))
3. **`Set-StrictMode -Version 3.0`** — catches uninitialized variables and array-index typos that 5.1's lax mode silently swallows.

## Script body contract

```powershell
# (preamble already concatenated above)
try {
    $obj = $input_json | ConvertFrom-Json    # NO -AsHashtable — PS 7+ only

    # Distinguish present-null from absent:
    if ($obj.PSObject.Properties.Name -contains 'memory_bytes') {
        # user explicitly set memory_bytes (possibly to null)
    }

    # ... do work ...

    $result | ConvertTo-Json -Depth 10 -Compress    # ALWAYS -Depth 10 -Compress
} catch {
    Write-HypervError $_
    exit 1
}
```

### Why `-Depth 10`
Default is `2`. With a 4-deep nested object, the default emits `"level3":"System.Collections.Hashtable"` — silently truncating to a literal string. ([Spike #2 finding 1](../../../docs/spikes/02-json-contract.md))

### Input parsing
The Go-side `RunScript` wrapper passes input data via an `$input_json = '...'` prelude (assigned before the script body). Use `ConvertFrom-Json` (PSCustomObject) — never `-AsHashtable`. Combined with Go-side `omitempty` discipline (absent fields are absent from JSON, not present-and-null), this gives null-vs-missing semantics on PS 5.1 without raising the floor.

### Script bodies arrive via `-EncodedCommand`
The Go connection layer encodes the entire script as UTF-16LE base64 and runs `powershell.exe -EncodedCommand <base64>`. Stdin is reserved for input data only. **Never** assume the script is on disk or readable as a file path — it's an argument blob. Don't reference the script's own filename inside the script.

## Structured error envelope

`Write-HypervError` (defined in the preamble) inspects `$_.CategoryInfo.Category` and emits this JSON to stderr:

```powershell
function Write-HypervError {
    param($ErrorRecord)
    $payload = @{
        message  = $ErrorRecord.Exception.Message
        category = $ErrorRecord.CategoryInfo.Category.ToString()
        fullyQualifiedErrorId = $ErrorRecord.FullyQualifiedErrorId
        cmdlet   = $ErrorRecord.CategoryInfo.Activity
        targetObject = $ErrorRecord.CategoryInfo.TargetName
    }
    $json = $payload | ConvertTo-Json -Compress
    [Console]::Error.WriteLine($json)
}
```

The Go side maps:
- `category = ObjectNotFound` or `ResourceUnavailable` → `ErrNotFound` → Read calls `resp.State.RemoveResource(ctx)`
- `category = PermissionDenied` → `ErrUnauthorized`
- `category = InvalidArgument` AND `fullyQualifiedErrorId` starts with `InvalidParameter,Microsoft.Vhd.*` → `ErrInvalidParentPath` ([spike #3](../../../docs/spikes/03-differencing-paths.md))
- everything else → `ErrPSExecution`

Always `exit 1` after `Write-HypervError`. The exit code is the primary signal; the JSON envelope is the detail.

## Portability rules (5.1 ↔ 7.4)

- **CIM, never WMI:** `Get-CimInstance`, `Invoke-CimMethod`. Both exist on 5.1.
- **DateTime → ISO-8601:** always format explicitly: `(Get-Date).ToString('o')`. Default `ConvertTo-Json` of `[DateTime]` on 5.1 emits `/Date(1777123800000)/` — JS-style epoch ms — which Go's `time.Time` won't parse. ([Spike #2 finding 3](../../../docs/spikes/02-json-contract.md))
- **`Invoke-WebRequest -UseBasicParsing`** — required on 5.1, no-op on 7+. Prefer `Start-BitsTransfer` for large transfers.
- **File I/O:** `-Encoding utf8` on `Get-Content`, `Out-File`, `Set-Content`. Defaults differ across versions.
- **Hashtable enumeration order:** use `[ordered]` if order matters in the JSON output.

## Idempotency

Hyper-V cmdlets are mostly **not** idempotent. `New-VM` on an existing name throws; `Remove-VM` on a missing name throws. **Don't** make scripts globally idempotent — the Go layer already knows from Terraform state whether the resource exists. Each script accepts an `IfExists`/`IfMissing` mode parameter where the cmdlet needs it, and the Go caller picks.

Exception: `Set-*` cmdlets that already no-op when value matches (`Set-VMProcessor -Count 4` on a VM at 4 cores) are fine to call unconditionally.

## When NOT to do a `Test-Path` pre-check

Per [spike #3](../../../docs/spikes/03-differencing-paths.md), `New-VHD -Differencing -ParentPath ...` validates parent existence in ~400 ms with a clear `InvalidArgument` / `InvalidParameter,Microsoft.Vhd.*` error. **Don't** add a `Test-Path` pre-check — it costs ~187 ms per successful plan to save ~213 ms in the rare failure case. Wrong direction. Let the cmdlet fail naturally; map the error category to the right Go error.

## Pester tests live alongside scripts

`internal/scripts/<resource>/*.Tests.ps1` runs under `Invoke-Pester` against real Hyper-V on the self-hosted Windows runner. Each script gets:

- Happy path test
- Missing-resource test (asserts the structured-error envelope)
- Bad-input test (asserts JSON parse errors are caught)

Pester tests run **independently of the Go provider** — they pipe input JSON to `pwsh -Command -` and assert on the stdout/stderr/exit shape. This is the contract-locking layer per [PLAN.md §9 tier 3](../../../docs/PLAN.md). When you change the JSON shape a script emits, update its Pester tests in the same commit.

## Comment discipline

Default to no comments. When you do write one, it states a hidden constraint or non-obvious WHY in one line. Specifically:

- ❌ Don't justify what the script *doesn't* do, or narrate history ("we deliberately don't use X", "changed from Y after spike #N").
- ❌ Don't echo what cmdlet names already say. `# Get-VMHost returns the host` is noise.
- ✅ One short sentence stating the load-bearing fact: a 5.1/7+ portability gotcha, a cmdlet quirk, a documented workaround.

The PR description and commit message own narrative. Pester tests pin behavior. Comments should be terse.

Same length caps as [CLAUDE.md "Code comments"](../../../CLAUDE.md) apply here. `task lint:comments` checks `*.ps1` files too — a wire-contract header (stdin/stdout JSON shape) is the one exemption, and only when marked `lint:allow-long-comment`.

## What NOT to do
- ❌ String-concatenate PowerShell from Go — script bodies always pass through `-EncodedCommand`
- ❌ Reference `$args` as a hashtable — that's the auto-variable for unbound function arguments. Use `$obj` or `$data` for parsed input
- ❌ Emit non-JSON to stdout — the Go side parses stdout as JSON; mixed output corrupts everything
- ❌ Use `Write-Host` — it bypasses the output stream and breaks captures. Use `Write-Verbose` (visible only at TF_LOG_PROVIDER=TRACE) or stream to file
- ❌ Add custom progress reporting — `$ProgressPreference = 'SilentlyContinue'` is set globally; respect it
- ❌ Leave `Write-Output` calls outside the final `ConvertTo-Json` — they pollute stdout

## References
- [PLAN.md §5 PS script contract](../../../docs/PLAN.md)
- [Spike #2 JSON contract findings](../../../docs/spikes/02-json-contract.md) — null-vs-missing, depth, encoding, DateTime, stderr filtering
- [Spike #3 differencing paths](../../../docs/spikes/03-differencing-paths.md) — error envelope shape, why no Test-Path pre-check
- `.psscriptanalyzer.psd1` — compatibility rules enforced in CI
