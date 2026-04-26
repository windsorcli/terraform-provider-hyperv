---
name: test-engineer
description: Write and structure tests across the three tiers this provider uses — Pester (PowerShell scripts standalone), Go unit (with fakeRunner returning canned JSON), and acceptance (TF_ACC=1 against real Hyper-V using terraform-plugin-testing). Use when adding tests for any new resource or data source, expanding coverage, fixing flaky tests, or deciding which tier a given assertion belongs in. Knows the TDD ordering this repo enforces — Pester first to lock the JSON contract, then Go unit, then resource schema, then acceptance, then implementation. Knows the modern terraform-plugin-testing API (statecheck, knownvalue, tfjsonpath, plancheck) and why we don't use the legacy ComposeAggregateTestCheckFunc style.
paths: **/*_test.go, **/*.Tests.ps1, internal/testutil/**/*.go, examples/**/*.tf
---

# Test Engineer

## Apply when
- Writing a new test at any tier — Pester, Go unit, or Go acceptance.
- Expanding coverage on an existing resource or data source.
- Fixing a flaky test or test that asserts the wrong thing.
- Deciding which tier a given assertion belongs in.
- Editing `internal/testutil/` (fake runner, acc helpers, fixtures).

## Do not apply when
- Writing the production code the test exercises — pair with `provider-author` or `powershell-scripter`.
- Pre-commit review — `review-pr`.

## Three-tier model

| Tier | Tool | Where it lives | When it runs | Catches |
|---|---|---|---|---|
| 1. Unit (Go) | `go test`, `fakeRunner` | `internal/**/*_test.go` | Every PR, every platform | Auth/env precedence, JSON marshal shape, schema validation, plan modifiers |
| 2. Acceptance (Go) | `terraform-plugin-testing` | `internal/resources/*/resource_acc_test.go` | Nightly + label-gated PRs on Windows runner | End-to-end CRUD + import + drift |
| 3. Pester | `Invoke-Pester` | `internal/scripts/<r>/*.Tests.ps1` | Same Windows runner, alongside acc | PS script bugs (typos, contract drift, stderr noise) |

A typo in a `.ps1` (`Get-VMM` instead of `Get-VM`) won't fail any Go test until acceptance runs. **Pester catches it in seconds — that's why it goes first** in the TDD order.

## TDD order for a new resource (mandatory)

For `hyperv_virtual_switch` and every resource after it, write tests in this order:

1. **Pester** for `internal/scripts/vswitch/get.ps1` — drives the JSON contract. The structured-error envelope, the success-shape JSON, the parameter-passing convention. Lock this first because changing it later means rippling through every layer.
2. **Go unit** for `hyperv.Client.GetVirtualSwitch` using `fakeRunner` returning JSON Pester just validated. Asserts struct marshal/unmarshal, error mapping (`category = InvalidArgument` → typed Go error), context cancellation propagation.
3. **Go unit** for the resource's Read against a fake `*hyperv.Client`. Asserts the not-found-clears-state path (`resp.State.RemoveResource(ctx)`), drift surfaced correctly.
4. **Schema test** — call `resource.Schema(ctx, req, resp)` directly, assert on `resp.Schema`. Tests validators and plan modifiers in isolation.
5. **Acceptance test** — `resource.Test` create+read+update+import+destroy, plus a drift test (mutate out-of-band, expect plan to show change).
6. **Implementation** — each step's implementation lands *after* its test. The TDD discipline matters most when designing the JSON contract; once that's locked, the Go layers fall into place mechanically.

## Pester tier (tier 3)

```powershell
Describe 'scripts/vswitch/get.ps1' {
    Context 'when the switch exists' {
        It 'returns a JSON object with the expected fields' {
            $input = @{ Name = 'TestSwitch' } | ConvertTo-Json -Compress
            $stdout = $input | & pwsh -NoProfile -Command -EncodedCommand $base64
            $obj = $stdout | ConvertFrom-Json
            $obj.Name | Should -Be 'TestSwitch'
            $obj.SwitchType | Should -BeIn @('External','Internal','Private')
        }
    }

    Context 'when the switch is missing' {
        It 'emits the structured error envelope on stderr and exits 1' {
            $err = & pwsh ... 2>&1
            $LASTEXITCODE | Should -Be 1
            $err | Should -Match '"category":"ObjectNotFound"'
        }
    }
}
```

Three required cases per script: happy path, missing-resource (asserts envelope shape), bad-input (asserts JSON parse errors are caught).

## Go unit tier (tier 1)

`fakeRunner` (in `internal/testutil/fake_runner.go`) is table-driven — keyed by script name, returns canned `(stdout, stderr, exitCode, error)`. Use canned JSON that came directly from a Pester run, not invented.

```go
func TestClient_GetVirtualSwitch_NotFound(t *testing.T) {
    t.Parallel()
    fr := testutil.NewFakeRunner().WithResponse("vswitch/get",
        "", `{"category":"ObjectNotFound","message":"..."}`, 1, nil)
    client := hyperv.NewClient(fr)

    _, err := client.GetVirtualSwitch(context.Background(), "missing")

    if !errors.Is(err, hyperv.ErrNotFound) {
        t.Errorf("got %v, want ErrNotFound", err)
    }
}
```

Coverage targets: 80% for `internal/connection` and `internal/hyperv`; 60% elsewhere. Don't chase 100%.

## Schema tests

Call `resource.Schema()` directly:

```go
func TestVirtualSwitchResource_Schema(t *testing.T) {
    t.Parallel()
    r := vswitch.NewResource()
    resp := &resource.SchemaResponse{}
    r.Schema(context.Background(), resource.SchemaRequest{}, resp)
    if resp.Diagnostics.HasError() {
        t.Fatalf("schema diagnostics: %v", resp.Diagnostics)
    }
    // Assert specific attributes, validators, plan modifiers.
}
```

Or use the framework's `schema.Schema{}.ValidateImplementation(ctx)` (1.13+) for a one-shot sanity check.

## Acceptance tier (tier 2)

**Use `terraform-plugin-testing`, never SDKv2 helpers.** `.golangci.yml` `depguard` blocks the SDKv2 imports outside of muxing.

Use modern `ConfigStateChecks` + `statecheck` + `knownvalue` + `tfjsonpath` — **not** the legacy `Check: ComposeAggregateTestCheckFunc(...)` style. New code in 2024+ HashiCorp providers uses these exclusively.

```go
var testAccProtoV6ProviderFactories = map[string]func() (tfprotov6.ProviderServer, error){
    "hyperv": providerserver.NewProtocol6WithError(provider.New("test")()),
}

func TestAcc_VirtualSwitch_basic(t *testing.T) {
    resource.Test(t, resource.TestCase{
        PreCheck:                 func() { testutil.PreCheck(t) },
        ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
        CheckDestroy:             testutil.CheckSwitchDestroy,
        Steps: []resource.TestStep{
            {
                Config: testAccVirtualSwitchConfig("internal-1"),
                ConfigStateChecks: []statecheck.StateCheck{
                    statecheck.ExpectKnownValue(
                        "hyperv_virtual_switch.test",
                        tfjsonpath.New("name"),
                        knownvalue.StringExact("internal-1"),
                    ),
                    statecheck.ExpectKnownValue(
                        "hyperv_virtual_switch.test",
                        tfjsonpath.New("switch_type"),
                        knownvalue.StringExact("Internal"),
                    ),
                },
            },
            {
                ResourceName:      "hyperv_virtual_switch.test",
                ImportState:       true,
                ImportStateVerify: true,
            },
            {
                Config: testAccVirtualSwitchConfig_renamed("internal-2"),
                // Update tested implicitly by config change + state check
            },
        },
    })
}
```

**`CheckDestroy` is mandatory.** Without it, leaked resources fill the host disk between runs. Each resource gets a `*_acc_test.go` with at minimum:

- `TestAcc_<Name>_basic` — create + read + destroy
- `TestAcc_<Name>_update` — change attributes, expect in-place
- `TestAcc_<Name>_import` — `ImportState: true, ImportStateVerify: true`
- `TestAcc_<Name>_drift` — mutate out-of-band, expect plan to show change

`t.Parallel()` is safe within a single test. Just name resources with `acctest.RandomWithPrefix` so concurrent tests don't collide on host-side names.

## Sweepers

`resource.AddTestSweepers` cleans up orphans named with the test prefix. Run via `task sweep`. The Windows runners also have a scheduled hourly sweep as defense-in-depth — see [PLAN.md §16.8](../../../docs/PLAN.md).

## Plan checks

Use `plancheck.ExpectResourceAction(addr, plancheck.ResourceActionUpdate)` to assert *what* the plan does, not just what state ends up looking like. Useful for catching unexpected `RequiresReplace` triggers.

## Test naming convention

`TestAcc_<ResourceShortName>_<scenario>` where scenario is one of: `basic`, `import`, `drift`, `update`, `destroy`, or a feature flag like `external_bindNIC`, `differencing`, `cloud_init_seed`.

Unit tests follow Go convention: `TestClient_GetVirtualSwitch_NotFound`, `TestSchema_TimeoutsAttribute`.

## Comment discipline (in test files too)

Default to no comments. When you do write one, it states a hidden constraint or non-obvious WHY in one line. Specifically:

- ❌ Don't write narrative test comments justifying what the test *doesn't* assert, or referencing past iterations ("regression test for the bug where we used to drop _x lines").
- ❌ Don't echo what test names already say. The test name is `TestStripCLIXML_PreservesXPrefix` — it doesn't need a paragraph above it explaining what that means.
- ✅ Test name + table-driven `name:` field carry the intent. If you need a comment, one short line stating the *invariant* the test pins.

Tests are documentation. The *test* is the artifact future readers find when grepping; the comment is a footnote.

## What NOT to do
- ❌ Write tests after the implementation. The Pester-first ordering is non-negotiable for new resources — the contract design is the load-bearing decision.
- ❌ Use `helper/resource` from SDKv2. Always `terraform-plugin-testing/helper/resource`.
- ❌ Use `Check: resource.ComposeAggregateTestCheckFunc(...)` — legacy style. Use `ConfigStateChecks` + `statecheck`.
- ❌ Mock the connection in acceptance tests. Acceptance is end-to-end against a real host.
- ❌ Use `testify`, `ginkgo`, `gomega`. Standard `testing` package only.
- ❌ Skip `CheckDestroy`. Leaks fill disks.
- ❌ Hardcode VM names. Use `acctest.RandomWithPrefix("tf-acc-")` so parallel tests don't collide.
- ❌ Write acceptance tests that depend on out-of-band setup (a switch existing already, etc.). Each test creates and destroys its own world.

## References
- [PLAN.md §9 TDD strategy](../../../docs/PLAN.md)
- [PLAN.md §11 anti-patterns (test side)](../../../docs/PLAN.md)
- [PLAN.md §16.2 acceptance.yaml gating](../../../docs/PLAN.md)
- HashiCorp scaffolding template `_test.go` files for reference patterns
