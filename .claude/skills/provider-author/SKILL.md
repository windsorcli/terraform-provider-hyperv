---
name: provider-author
description: Author terraform-provider-hyperv resources, data sources, the typed Hyper-V client, and connection backends. Use when editing files under internal/provider, internal/resources, internal/datasources, internal/hyperv, internal/connection, internal/types, or when changing schema, plan modifiers, validators, or provider configuration. Knows the terraform-plugin-framework idioms this repo locks in (PLAN.md §8, §11), how MarkdownDescription drives Registry docs (§15), and the SDKv2 anti-patterns to avoid (§11). Not for PowerShell scripts (use powershell-scripter), tests (use test-engineer), or release plumbing.
paths: internal/provider/**/*.go, internal/resources/**/*.go, internal/datasources/**/*.go, internal/hyperv/**/*.go, internal/connection/**/*.go, internal/types/**/*.go, examples/**/*.tf, templates/**
---

# Provider Author

## Apply when
- Editing any `*.go` under `internal/provider`, `internal/resources`, `internal/datasources`, `internal/hyperv`, `internal/connection`, or `internal/types`.
- Adding or modifying a resource, data source, or provider attribute.
- Changing schema attributes, plan modifiers, validators, or `Configure` flow.
- Writing or updating `MarkdownDescription` strings (they generate the Registry docs).
- Editing `examples/**/*.tf` or `templates/**` (these are part of the doc-generation contract).

## Do not apply when
- Editing `.ps1` files under `internal/scripts/` — that's `powershell-scripter`.
- Writing tests primarily — `test-engineer`. (You may need both skills when a test exercises new schema.)
- Pre-commit bug review — `review-pr`.

## Core framework rules

This provider uses `terraform-plugin-framework` (not SDKv2). The `.golangci.yml` `depguard` rule blocks `terraform-plugin-sdk/v2` imports — there's no muxing. Every resource and data source declares compile-time interface checks:

```go
var _ resource.Resource = (*FooResource)(nil)
var _ resource.ResourceWithImportState = (*FooResource)(nil)
var _ resource.ResourceWithConfigure = (*FooResource)(nil)
```

CRUD methods return nothing — mutate `resp.Diagnostics`:

```go
resp.Diagnostics.Append(req.Plan.Get(ctx, &model)...)
if resp.Diagnostics.HasError() { return }
```

Resource not found during Read → `resp.State.RemoveResource(ctx)`, **never** error. Drift detection on next plan handles the rest.

## Schema patterns

- **Nested attributes, not blocks.** `SingleNestedAttribute`, `ListNestedAttribute`, `MapNestedAttribute`, `SetNestedAttribute`. Blocks remain only for legacy HCL ergonomics; they cannot be `Required` or have `Default`.
- **`MarkdownDescription`** (not `Description`). `tfplugindocs` renders Markdown into Registry docs. Single-line summary first; document units (`memory_bytes`, not `memory`); mark defaults inline (`Default: "vhdx".`); cross-link with relative Markdown links.
- **`Sensitive: true`** on every credential attribute (`password`, `ssh.private_key`, `ssh.passphrase`).
- **Plan modifiers** from `resource/schema/<type>planmodifier`:
  - `UseStateForUnknown()` on every `Computed` ID, ARN, or path that doesn't change after creation. Without this, you get spurious `(known after apply)` diffs on every refresh.
  - `RequiresReplace()` / `RequiresReplaceIfConfigured()` on immutable fields like `generation`.
- **Validators** from `terraform-plugin-framework-validators` — `stringvalidator.OneOf`, `RegexMatches`, `int64validator.Between`. Don't roll your own.

## Custom types over `StateFunc`/`DiffSuppressFunc`

For domain types with semantic equality (Windows file paths, JSON, durations), implement `basetypes.StringTypable` + `StringSemanticEquals`. Path attributes on `hyperv_vhd` and `hyperv_image_file` use a custom type that normalizes `\\` ↔ `/` and case-insensitive drive letters — see [PLAN.md §8](../../../docs/PLAN.md) and [spike #3 finding 4](../../../docs/spikes/03-differencing-paths.md). This eliminates whole classes of "diff because case changed" bugs.

## Diagnostics

- `resp.Diagnostics.AddError(summary, detail)` — generic API/exec error.
- `resp.Diagnostics.AddAttributeError(path.Root("foo"), summary, detail)` — config-level error. CLI points at the right line. Use whenever the failure is config-related, including `ErrInvalidParentPath` from the typed client.
- `resp.Diagnostics.AddWarning(...)` — deprecations, soft issues. Don't overuse.

Do not return `error` from CRUD. Don't `fmt.Errorf` for user-facing messages — wrap internally for logs, surface via `AddError`/`AddAttributeError`.

## Configure pattern

Provider `Configure` resolves attribute → env var → error/zero. Build the typed `*hyperv.Client` once, stash on **both** `resp.ResourceData` and `resp.DataSourceData`. Each resource's `Configure` type-asserts from `req.ProviderData` with a `nil` early-return — validation passes call Configure with `nil` ProviderData and missing this guard panics. See [PLAN.md §6](../../../docs/PLAN.md) for the env var precedence rules.

```go
func (r *FooResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
    if req.ProviderData == nil {
        return
    }
    client, ok := req.ProviderData.(*hyperv.Client)
    if !ok {
        resp.Diagnostics.AddError("Unexpected provider data", fmt.Sprintf("got %T", req.ProviderData))
        return
    }
    r.client = client
}
```

## Logging

- Mask once at provider Configure: `ctx = tflog.MaskFieldValuesWithFieldKeys(ctx, "password", "private_key", "passphrase")` and `ctx = tflog.OmitLogWithFieldKeys(ctx, "stdin_json")`. Don't log raw JSON payloads.
- `tflog.Debug(ctx, "ran ps script", map[string]any{"script": name, "duration_ms": ms, "exit_code": ec})` — metadata only.
- `tflog.Trace` for full I/O — opt-in via `TF_LOG_PROVIDER=TRACE`.
- **Never `log.Printf`** — corrupts the gRPC protocol.

## Timeouts

Use `terraform-plugin-framework-timeouts`. Add a `timeouts` nested attribute on resources that need them (notably `hyperv_vm`, `hyperv_vm_state`):

```go
"timeouts": timeouts.Attributes(ctx, timeouts.Opts{Create: true, Update: true, Delete: true}),
```

Pull the timeout in CRUD, `context.WithTimeout`, `defer cancel()`. The framework does not enforce per-resource timeouts the way SDKv2 did.

## Hyper-V client (`internal/hyperv/`)

`hyperv/script.go` is the single chokepoint between Go DTOs and PS JSON. Three rules:

1. Marshal Go inputs with `omitempty` so absent fields are absent from JSON, not present-and-null. PS scripts use `$obj.PSObject.Properties.Name -contains 'foo'` to distinguish ([spike #2](../../../docs/spikes/02-json-contract.md)).
2. Unmarshal PS output into typed DTOs in `hyperv/types.go`. Use `[]T` for collection fields, `*T` for nullable scalars (e.g., `IovSupportReasons *string`).
3. Map `Write-HypervError` envelopes to typed Go errors in `hyperv/errors.go`. The `category` + `fullyQualifiedErrorId` pair disambiguates — see [PLAN.md §5](../../../docs/PLAN.md) error categorization.

## Anti-patterns to avoid

- ❌ `d.SetId("")` to delete state → ✅ `resp.State.RemoveResource(ctx)`
- ❌ Returning `diag.Diagnostics` from CRUD → ✅ mutate `resp.Diagnostics`
- ❌ `d.Get("foo").(string)` typed assertion → ✅ typed model + `req.Plan.Get(ctx, &model)`
- ❌ `StateFunc` / `DiffSuppressFunc` → ✅ custom type with `StringSemanticEquals`
- ❌ Schema *blocks* for new attributes → ✅ nested attributes
- ❌ `helper/schema.TimeoutCreate` → ✅ `terraform-plugin-framework-timeouts`
- ❌ `resource.Retry` / `RetryContext` → ✅ plain Go retry inside the client
- ❌ Package-level globals for the API client → ✅ Configure builds it; passed via ResourceData
- ❌ Mutating the resource struct from CRUD methods → struct holds the client; per-call state in locals
- ❌ Hand-edited `docs/resources/*.md` → regenerated by `task generate`
- ❌ Forgetting `UseStateForUnknown()` on stable computed IDs → drift on every refresh
- ❌ Missing `req.ProviderData == nil` guard → panic during validation

## Registry docs are coupled to schema

`MarkdownDescription` strings ARE the Registry docs after `tfplugindocs generate`. When editing schema:

1. Edit `MarkdownDescription`.
2. Edit/add `examples/resources/<name>/resource.tf` and `import.sh`.
3. Run `task generate`. Confirm `git diff docs/` matches your intent.
4. CI's `docs-drift` job catches forgotten regen.

Subcategory pinning ([PLAN.md §15.2](../../../docs/PLAN.md)): `Compute` (vm, vm_state, vm_checkpoint), `Networking` (virtual_switch, vm_network_adapter), `Storage` (vhd, image_file, vm_hard_disk_drive, vm_dvd_drive), `Host` (host data source). Don't drift these — Registry sidebar links break.

## Schema migrations

Any post-v1 attribute rename or type change requires `Resource.UpgradeState` with a `SchemaVersion` bump. Don't defer — broken state migrations are user-visible disasters. See [PLAN.md §10](../../../docs/PLAN.md).

## References
- [PLAN.md §4 Connection interface](../../../docs/PLAN.md)
- [PLAN.md §5 PS script contract & error envelope](../../../docs/PLAN.md)
- [PLAN.md §8 Framework patterns](../../../docs/PLAN.md)
- [PLAN.md §11 Anti-patterns](../../../docs/PLAN.md)
- [PLAN.md §15 Registry docs conventions](../../../docs/PLAN.md)
- [Spike findings under docs/spikes/](../../../docs/spikes/)
