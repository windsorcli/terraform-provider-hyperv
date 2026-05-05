# Running acceptance tests

Acceptance tests for `terraform-provider-hyperv` create real resources on a real Hyper-V host. They are **author-run locally** against a maintainer-controlled workbench — there is no GitHub-hosted runner that can stand up Hyper-V, so the public CI matrix covers Pester (script-level) and Go unit tests only. Acceptance is a release gate the maintainer runs before tagging.

## Workbench prerequisites

You need:

1. A Windows host with the Hyper-V role enabled (Server 2019/2022 or Windows 10/11 Pro/Enterprise).
2. One reachable transport from your dev machine to the bench:
   - **`local`** — run `task test:acc` directly on the Hyper-V host.
   - **`ssh`** — OpenSSH Server enabled on the bench, with PowerShell as the default shell. Key-based auth recommended.
   - **`winrm`** — HTTPS listener on 5986 with NTLM (workgroup) or Basic auth. Server 2022 ships with WinRM enabled by default; you only need to add the HTTPS listener and a cert if one isn't already configured. Kerberos auth is also supported in domain environments — see [Running with WinRM + Kerberos](#running-with-winrm--kerberos) below.
3. A test-only directory on the bench where ephemeral VHDs and downloads can land. `C:\hyperv\tfacc` is the convention.
4. (For the `image_file` host_path test) a small pre-placed text file at a known path — the test reads its SHA-256 and tracks drift, but never modifies it. Stage once and forget.
5. (For the `vm` DVD and boot-order tests) a small pre-placed ISO at a known path — any few-KB ISO works; the tests attach it to a VM but never read its contents.

## `.env.local` — your bench credentials

Acceptance test config lives in `.env.local` (gitignored). Copy `.env.example` and fill in the `HYPERV_*` values for your bench:

```sh
cp .env.example .env.local
$EDITOR .env.local
```

Task auto-loads `.env.local` (and `.env`, with `.env.local` taking precedence) into the environment for every task invocation, so `task test:acc` picks up your bench config without further plumbing. See [Taskfile.yaml](../../Taskfile.yaml) line 12 for the loader config.

The acc-test-only block at the bottom of `.env.example` covers the fixture vars the per-resource tests gate on:

| Variable | Required by | Purpose |
|---|---|---|
| `HYPERV_TEST_VHD_DIR` | `vhd`, `image_file` (both modes) | Bench directory where ephemeral VHDs/downloads land. |
| `HYPERV_TEST_HOST_FILE` | `image_file` (host_path mode) | Pre-placed file path. Verifies presence and tracks SHA-256 for drift. |
| `HYPERV_TEST_ISO_FILE` | `vm` (`withDvdDrive`, `withBootOrder`) | Pre-placed ISO path. Any few-KB ISO works. |

A test that requires an unset env var calls `t.Skip` instead of `t.Fatal`, so a partial bench still runs the subset it can support.

**Quote your Windows paths in `.env.local`.** Bash strips backslashes from unquoted assignment values when sourced, so `HYPERV_TEST_VHD_DIR=C:\hyperv\tfacc` becomes `C:hypervtfacc` after `task` loads the file — and the matching tests fail with confusing "file not found" errors deep in the apply phase. Single-quote the value (`HYPERV_TEST_VHD_DIR='C:\hyperv\tfacc'`) or use forward slashes (`C:/hyperv/tfacc`); both are accepted by Hyper-V.

### url-mode is hermetic

`TestAcc_ImageFile_url` does **not** need a public URL or pre-placed checksum. It stands up an `httptest.Server` on the runner's LAN-routable IP (computed via a UDP-dial routing-table lookup against `HYPERV_HOST`) and the bench downloads from there. The fixture bytes live in the test, the SHA-256 is computed in-test against those bytes, so the assertion is exact rather than format-only.

This works as long as the bench can route back to the runner — flat LANs (typical home/office setups) work without configuration. If the bench is behind NAT or on a network that can't reach the runner, the test will hang during `terraform apply` waiting for the bench's `Invoke-WebRequest` to download. macOS may also pop a firewall-allow prompt on the first run.

## Running with WinRM + Kerberos

The acctest bar runs end-to-end against a Kerberos-authenticated WinRM listener as long as the bench is domain-joined and the runner has a valid TGT. The end-to-end Kerberos lab under [`examples/lab/kerberos/`](../../examples/lab/kerberos/) provisions a self-contained `HV.LAB` realm if you don't have an existing AD environment to test against.

**ccache mode is the supported path.** Password-mode Kerberos (`HYPERV_PASSWORD` with `HYPERV_WINRM_AUTH=kerberos`) hits an upstream `masterzen/winrm` bug against AD KDCs — see [docs/contributing/kerberos.md](kerberos.md) for the full diagnostic. Use `HYPERV_KRB5_CCACHE_PATH` pointing at a `kinit`-obtained TGT instead.

A typical run looks like this. Override at the shell rather than editing `.env.local` so the file stays clean for the default NTLM flow:

```sh
# 1. Get a TGT (one-time per ~10h, or however long your KDC's ticket
#    lifetime is). The lab's task lab:client-setup automates this.
kinit Administrator@HV.LAB

# 2. Run the bar with the Kerberos overrides.
set -a; source .env.local; set +a
unset HYPERV_PASSWORD                                # mutually exclusive with ccache
HYPERV_BACKEND=winrm \
HYPERV_HOST=hv-bench-01.hv.lab \                     # FQDN, not IP -- SPN is HTTP/<host>
HYPERV_PORT=5986 \
HYPERV_WINRM_INSECURE=true \                         # if the bench has a self-signed cert
HYPERV_WINRM_AUTH=kerberos \
HYPERV_KRB5_REALM=HV.LAB \
HYPERV_KRB5_CCACHE_PATH=/tmp/krb5cc_$(id -u) \
KRB5_CONFIG=$HOME/.config/krb5.conf \
TF_ACC=1 go test -v -timeout 120m -run '^TestAcc' ./...
```

The TGT must remain valid for the duration of the run; `task test:acc` defaults to a 120-minute timeout, so a freshly-issued ticket with the typical 10-hour AD lifetime is plenty. If your KDC's lifetime is shorter, run `kinit` again before kicking off the bar.

`HYPERV_HOST` must be the FQDN, not the bench's IP — Kerberos negotiates the SPN as `HTTP/<host>`, and only the FQDN is registered in AD. The lab's `task lab:client-setup` writes an `/etc/hosts` entry so the FQDN resolves on the runner.

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
