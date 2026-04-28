# Running acceptance tests

Acceptance tests for `terraform-provider-hyperv` create real resources on a real Hyper-V host. They are **author-run locally** against a maintainer-controlled workbench — there is no GitHub-hosted runner that can stand up Hyper-V, so the public CI matrix covers Pester (script-level) and Go unit tests only. Acceptance is a release gate the maintainer runs before tagging.

## Workbench prerequisites

You need:

1. A Windows host with the Hyper-V role enabled (Server 2019/2022 or Windows 10/11 Pro/Enterprise).
2. One reachable transport from your dev machine to the bench:
   - **`local`** — run `task test:acc` directly on the Hyper-V host.
   - **`ssh`** — OpenSSH Server enabled on the bench, with PowerShell as the default shell. Key-based auth recommended.
   - **`winrm`** — HTTPS + NTLM (workgroup) or Kerberos (domain). Disabled in this acc-test pass; SSH is the supported transport for v0.1.0-alpha.
3. A test-only directory on the bench where ephemeral VHDs and downloads can land. `C:\hyperv\tfacc` is the convention.
4. (For the `image_file` host_path test) a small pre-placed text file at a known path — the test reads its SHA-256 and tracks drift, but never modifies it. Stage once and forget.

## `.env.local` — your bench credentials

Acceptance test config lives in `.env.local` (gitignored). Copy `.env.example` and fill in the `HYPERV_*` values for your bench:

```sh
cp .env.example .env.local
$EDITOR .env.local
```

Task auto-loads `.env.local` (and `.env`, with `.env.local` taking precedence) into the environment for every task invocation, so `task test:acc` picks up your bench config without further plumbing. See [Taskfile.yaml](../../Taskfile.yaml) line 12 for the loader config.

The acc-test-only block at the bottom of `.env.example` covers the four extra fixtures the per-resource tests gate on:

| Variable | Required by | Purpose |
|---|---|---|
| `HYPERV_TEST_VHD_DIR` | `vhd`, `image_file` (both modes) | Bench directory where ephemeral VHDs/downloads land. |
| `HYPERV_TEST_HOST_FILE` | `image_file` (host_path mode) | Pre-placed file path. Verifies presence and tracks SHA-256 for drift. |

A test that requires an unset env var calls `t.Skip` instead of `t.Fatal`, so a partial bench still runs the subset it can support.

### url-mode is hermetic

`TestAcc_ImageFile_url` does **not** need a public URL or pre-placed checksum. It stands up an `httptest.Server` on the runner's LAN-routable IP (computed via a UDP-dial routing-table lookup against `HYPERV_HOST`) and the bench downloads from there. The fixture bytes live in the test, the SHA-256 is computed in-test against those bytes, so the assertion is exact rather than format-only.

This works as long as the bench can route back to the runner — flat LANs (typical home/office setups) work without configuration. If the bench is behind NAT or on a network that can't reach the runner, the test will hang during `terraform apply` waiting for the bench's `Invoke-WebRequest` to download. macOS may also pop a firewall-allow prompt on the first run.

## Running the tests

```sh
task test:acc                                          # all acc tests
go test -v -timeout 30m -run TestAcc_VirtualSwitch ./...  # one resource
go test -v -timeout 30m -run TestAcc_VHD_dynamic ./...    # one scenario
```

The default timeout in `task test:acc` is 120 minutes — generous for slow benches, network-bound URL fetches, and the full multi-resource flow tests that arrive in later milestones. Per-resource runs are usually <60s.

`TF_ACC=1` is set automatically by the Taskfile target; running `go test` directly requires you to set it yourself, otherwise `resource.Test` skips the body.

## Naming and cleanup

Every acc-created resource has the prefix `tfacc-<scenario>-<random>` — see [`internal/acctest/acc.go`](../../internal/acctest/acc.go) `RandomName`. Each test's `CheckDestroy` (or default destroy step) removes its own resources; orphans only happen when a run is killed mid-step (Ctrl-C, panic, host reboot).

**v0.1.0-alpha does not ship a sweeper.** If a test crashes and leaves orphans on the bench, clean up manually — `Get-VMSwitch | ?{ $_.Name -like 'tfacc-*' } | Remove-VMSwitch -Force`, similar for VHDs and image files. A typed-client-driven sweeper is tracked as a follow-up.

## Pre-release expectations

Before tagging a release, the maintainer must:

1. Run `task test:acc` against the workbench. All acc tests pass.
2. Run `task test:pester` and `task test:unit`. Both green.
3. Paste the acc-test summary (test count and pass/fail) into the release-drafter PR or release notes so the artifact's provenance is traceable.

This isn't enforced by CI — the gate is the maintainer's own pre-flight. The release-drafter PR template should grow a checkbox for it once the v0.1.0-alpha process settles.

## Known limitations of this setup

- **No CI-side acceptance.** Forks can't run acc tests against a maintainer's bench, so PR validation is unit + Pester only. Expect to run `task test:acc` locally on any PR that touches a CRUD path before merging.
- **One bench at a time.** Without a runner pool, two simultaneous `task test:acc` runs on the same bench will conflict. The `tfacc-*` random naming protects against ID collision, but a vswitch named `tfacc-foo` exists on the host even before its test claims it; `CheckDestroy` may flap if two tests target the same resource type concurrently. Serialize.
- **Reduced regression coverage between releases.** A regression in a CRUD path that bypasses Pester (e.g., a Go-side state-marshaling bug) won't be caught by automated CI — only by the maintainer's pre-release `task test:acc`. Catch this by running acc tests after any non-trivial change to `internal/resources/*` or `internal/hyperv/*`, even if the PR doesn't touch CRUD directly.

## Future direction

When a self-hosted runner becomes feasible, the entry point is the same — `task test:acc` — and a thin `acceptance.yaml` workflow can call it on `pull_request_target` gated by an `acc-test` label. Until then, the maintainer's bench is the source of truth.
