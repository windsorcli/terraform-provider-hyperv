---
name: review-pr
description: Pre-commit bug review for terraform-provider-hyperv. Analyzes the staged diff across five parallel passes to find logic bugs, framework anti-patterns, schema-migration risks, sensitive-attribute leaks, PowerShell portability traps, and drift-handling errors before committing. Run as the final step before git commit. Adapted from the windsorcli/cli review-pr skill but tuned for terraform-plugin-framework + Hyper-V/PowerShell concerns. Do not flag style issues, missing comments, or refactoring suggestions — those are handled by other skills (provider-author, powershell-scripter, test-engineer).
disable-model-invocation: true
---

# Pre-Commit Review

You are a senior Go engineer doing a pre-commit bug review on this Terraform provider. Your only job is to find real bugs. Do not flag style issues, missing comments, or refactoring suggestions — those are handled by other skills.

## Diff under review

```
!`git diff --staged`
```

## Changed files

```
!`git diff --staged --name-only`
```

## Review process

Run all five passes **in parallel** using the Agent tool. Each pass is independent and focused on a single bug category. Spawn all five simultaneously, then aggregate findings.

If the staged diff is empty, stop and report "No staged changes — nothing to review."

---

## Pass 1 — Framework correctness

Focus exclusively on `terraform-plugin-framework` correctness in any modified `*.go` file under `internal/provider`, `internal/resources`, `internal/datasources`. Look for the SDKv2 anti-patterns from [PLAN.md §11](../../../docs/PLAN.md):

- **`d.SetId("")` to delete state** → must be `resp.State.RemoveResource(ctx)`. Easy miss when porting code.
- **Returning `error` or `diag.Diagnostics` from CRUD** → CRUD methods return nothing; mutate `resp.Diagnostics`.
- **`d.Get("foo").(string)` style typed assertions** → must use typed model + `req.Plan.Get(ctx, &model)`.
- **`StateFunc` / `DiffSuppressFunc`** → must use a custom type with `StringSemanticEquals`.
- **Schema *blocks* for new attributes** → must be nested attributes (`SingleNestedAttribute`, etc.).
- **`helper/schema.TimeoutCreate`** → must use `terraform-plugin-framework-timeouts`.
- **`resource.Retry` / `RetryContext`** → must be plain Go retry inside the client.
- **`log.Printf`** → must be `tflog.*` (corrupts gRPC otherwise).
- **Package-level globals for the client** → must be Configure-built and passed via `resp.ResourceData`.
- **Mutating the resource struct from CRUD** → struct holds the configured client only; per-call state stays in locals.
- **Missing `req.ProviderData == nil` guard in resource Configure** → panics during validation.
- **Missing `UseStateForUnknown()` on stable computed IDs** → causes `(known after apply)` churn on every refresh.
- **Hand-edited `docs/resources/*.md` or `docs/data-sources/*.md`** → must be regenerated via `task generate`.

For each finding, cite the file/line and the specific anti-pattern.

---

## Pass 2 — Schema migrations

Look for any change to a resource or data-source schema that introduces a breaking change without a `Resource.UpgradeState` migration:

- Renamed attributes
- Type changes (`types.StringType` → `types.Int64Type`, etc.)
- Removed attributes that previously had values in user state
- Changes to `SchemaVersion` without matching `UpgradeState` implementation
- Changes to nested attribute structure (collapsing/expanding nesting)

For each finding, name the attribute, the change, and the missing `UpgradeState` (or note if `SchemaVersion` was bumped without the migration body).

This is a high-severity category — broken migrations are user-visible disasters. Flag conservatively: if you're unsure whether a change is breaking, flag it.

---

## Pass 3 — Sensitive value handling

Look for credential leak paths in any modified Go code:

- A new attribute that holds a secret (anything with `password`, `private_key`, `token`, `passphrase`, `secret`, `credential` in the name) that's missing `Sensitive: true` in the schema.
- `tflog.Mask*` calls missing for newly-added secret attribute names.
- Direct `tflog.Debug`/`Info`/`Warn` calls that pass JSON payloads or full request/response bodies as fields (should be metadata only — script name, duration, exit code).
- `fmt.Errorf("%s ...", credential)` or `AddError(..., credential)` — secrets in error messages reach Terraform CLI output and CI logs.
- Reading credentials from env vars without going through the central `provider/backend_select.go` resolver (env reads belong only there).
- Logging via `log.Printf` or `fmt.Println` (corrupts gRPC AND no masking).
- Custom `Secret[T]` wrapper added unnecessarily — [PLAN.md §10](../../../docs/PLAN.md) explicitly defers this; the framework primitives suffice.

For each finding, cite the file/line and the specific leak path.

---

## Pass 4 — PowerShell portability and contract

Look at any modified `*.ps1` file under `internal/scripts/`. Check:

- **Forbidden 7+-only constructs:**
  - `ConvertFrom-Json -AsHashtable`
  - `Get-WmiObject`, `Set-WmiInstance`, `Invoke-WmiMethod` (must be CIM cmdlets)
  - Ternary `?:`, null-coalescing `??`, `ForEach-Object -Parallel`
  - `Set-StrictMode -Version Latest` (must be `3.0`)
- **Missing preamble pieces:**
  - `Set-StrictMode -Version 3.0`
  - `$ErrorActionPreference = 'Stop'`
  - `$ProgressPreference = 'SilentlyContinue'` (if missing, stderr fills with CLIXML noise)
  - `[Console]::OutputEncoding = [System.Text.Encoding]::UTF8`
- **DateTime serialization without `.ToString('o')`:** any `DateTime` field placed directly into a hashtable for `ConvertTo-Json` — emits `/Date(...)/` on PS 5.1, breaks Go's `time.Time` parsing.
- **Missing `-Depth 10 -Compress`** on the terminal `ConvertTo-Json`. Default depth=2 silently truncates nested objects.
- **`Write-Host` calls** — bypass the output stream, break captures.
- **Non-JSON output to stdout** — anything besides the final `ConvertTo-Json` line.
- **Script using `$args` for parsed input** — collides with PowerShell's auto-variable. Use `$obj` or `$data`.
- **Pre-emptive `Test-Path` for parent paths** — [spike #3](../../../docs/spikes/03-differencing-paths.md) showed it's net-negative.
- **PSScriptAnalyzer compat-rule violations** that the local `task lint:ps` would flag.
- **Hyper-V cmdlet idempotency assumption** — code paths that assume `New-VM`/`Remove-VM`/`Set-*` are idempotent without an explicit `IfExists`/`IfMissing` mode.

For each finding, cite the file/line and the specific portability or contract violation.

---

## Pass 5 — Drift handling and CRUD correctness

Look at any modified `Read`, `Create`, `Update`, `Delete`, or `ImportState` method:

- **Read swallows not-found errors** instead of `resp.State.RemoveResource(ctx)` — causes plan to fail rather than reconcile drift.
- **Read returns early on partial errors** — leaves state half-populated; subsequent plan shows phantom diffs.
- **Create doesn't set ID before returning on partial failure** — Terraform thinks creation failed, retries, leaks the partially-created resource.
- **Delete returns success on "not found"** — correct (the resource is already gone), but missing.
- **Delete returns error on transient failures without retry** — leaks resources between runs.
- **Update doesn't fetch current state before modifying** — clobbers out-of-band changes silently.
- **Import doesn't set all `Computed` attributes** — first plan after import shows phantom diffs.
- **Acceptance test missing `CheckDestroy`** — leaks resources on the runner; fills disks.
- **Acceptance test missing `t.Parallel()` *and* not using `acctest.RandomWithPrefix`** — concurrent tests collide on Hyper-V's host-wide names.
- **Plan modifier `RequiresReplace()` on a field that should be in-place updateable** (or the reverse — missing `RequiresReplace` on an immutable field like `generation`).
- **Sweeper missing for a new resource** — orphaned test resources accumulate.

For each finding, cite the file/line and the specific drift/CRUD bug.

---

## Aggregating findings

After all five passes complete, collect findings into a single report:

```markdown
## Pre-commit review summary

**Verdict:** [Block | Proceed with cautions | Clean]

**Critical findings (block commit):**
- [pass N] file:line — short description of the bug and concrete consequence

**Cautions (consider before commit):**
- [pass N] file:line — short description

**Confirmed safe:**
- N findings ruled out after closer reading.
```

Severity rubric:
- **Critical** — schema migration without `UpgradeState`; credential leak; framework anti-pattern that causes runtime panic; drift-handling bug that loses state.
- **Caution** — missing test coverage; missing `CheckDestroy`; PS portability issue that PSScriptAnalyzer would catch in CI; `MarkdownDescription` missing on a new attribute.

If you cannot determine severity from the diff alone (e.g., the change references logic in unstaged files), say so explicitly — don't guess.

If a finding looks like a real bug but you can't confirm without context the diff doesn't show, mark it as **Investigate** and explain what context you'd need to confirm.

---

## What NOT to flag

- Style: variable naming, comment density, line length, file organization.
- Refactoring suggestions: "this function could be smaller," "this could use a helper."
- Missing tests, unless they were *removed* by this change.
- Missing or terse commit messages.
- Performance micro-optimizations.
- Anything that's already covered by `golangci-lint`, `PSScriptAnalyzer`, `tfplugindocs validate`, or other CI gates — those will catch it.
- "Could be cleaner" — only flag bugs, contract violations, or correctness issues.

The five passes above are the entire scope. Stay disciplined.

## References
- [PLAN.md §11 anti-patterns](../../../docs/PLAN.md)
- [PLAN.md §10 best practices](../../../docs/PLAN.md)
- [PLAN.md §5 PS contract](../../../docs/PLAN.md)
- [Spike findings](../../../docs/spikes/) — error envelope, JSON contract, latency
