# Terraform Provider for Hyper-V

Manage the lifecycle of Microsoft Hyper-V virtual machines, switches, disks, and images from Terraform — with a provider binary that runs on Linux, macOS, or Windows and talks to Hyper-V hosts over local PowerShell, SSH, or WinRM.

[![CI](https://github.com/windsorcli/terraform-provider-hyperv/actions/workflows/ci.yaml/badge.svg)](https://github.com/windsorcli/terraform-provider-hyperv/actions/workflows/ci.yaml)
[![Go Report Card](https://goreportcard.com/badge/github.com/windsorcli/terraform-provider-hyperv)](https://goreportcard.com/report/github.com/windsorcli/terraform-provider-hyperv)
[![Latest Release](https://img.shields.io/github/v/release/windsorcli/terraform-provider-hyperv?include_prereleases&sort=semver)](https://github.com/windsorcli/terraform-provider-hyperv/releases)
[![License: MPL-2.0](https://img.shields.io/badge/License-MPL--2.0-blue.svg)](LICENSE)

> [!IMPORTANT]
> This provider is pre-1.0. Schema, attribute names, and behavior may change between minor versions until `v1.0.0` ships. Pin to an exact version in production.

## Background

[`taliesins/terraform-provider-hyperv`](https://github.com/taliesins/terraform-provider-hyperv) is the existing community Hyper-V provider. It is built on `terraform-plugin-sdk/v2` and reaches Hyper-V hosts over WinRM (HTTP or HTTPS). Like this provider, the binary itself runs on Linux, macOS, or Windows.

This provider is a clean-room reimplementation on the modern [`terraform-plugin-framework`](https://developer.hashicorp.com/terraform/plugin/framework). Differences worth knowing about:

- **Pluggable execution backends.** `local` (provider already on the host), `ssh` (key- or password-auth into the host's OpenSSH), or `winrm` (HTTP/HTTPS, NTLM/Basic/Kerberos). The taliesins provider supports WinRM only.
- **Plugin Framework idioms.** Strict typed schemas, plan modifiers, validators, custom semantic-equality types, and Terraform protocol v6.
- **Embedded PowerShell with a JSON contract.** Each operation ships an embedded `.ps1` through the chosen transport and round-trips JSON via stdin/stdout. Scripts are independently testable with [Pester](https://pester.dev/).

This is a clean break — env var names and resource attribute names do not match `taliesins/`. A migration guide ships under [`docs/guides/migrating-from-taliesins.md`](docs/guides/migrating-from-taliesins.md).

## Supported resources and data sources

| Resource | Subcategory | Notes |
|---|---|---|
| `hyperv_virtual_switch` | Networking | External / Internal / Private switches; NIC team binding; management OS share toggle. |
| `hyperv_vm_network_adapter` | Networking | Per-VM NICs with VLAN, MAC, and bandwidth limits. |
| `hyperv_image_file` | Storage | Place a VHDX or ISO on the host. Modes: `url`, `host_path`, `local_path`, `content`, `cloud_init` (NoCloud seed-ISO synthesis), `unattend` (Windows answer-file ISO synthesis). |
| `hyperv_vhd` | Storage | Fixed / dynamic / differencing VHD or VHDX. Resizable for dynamic. |
| `hyperv_vm_hard_disk_drive` | Storage | Attaches an existing `hyperv_vhd` to a VM at a controller location. |
| `hyperv_vm_dvd_drive` | Storage | ISO attachment with eject-on-destroy semantics for appliance-OS workflows. |
| `hyperv_vm` | Compute | Generation 1/2, CPU, memory (static or dynamic), Secure Boot, integration services, boot order, automatic start/stop. |
| `hyperv_vm_state` | Compute | Operational state (`running`/`off`/`saved`/`paused`); optionally waits for an IP and exposes `ip_addresses`. |
| `hyperv_vm_checkpoint` | Compute | Production or standard checkpoints. |

| Data source | Subcategory |
|---|---|
| `hyperv_host` | Host |
| `hyperv_vm` | Compute |
| `hyperv_virtual_switch` | Networking |

Out of scope for v1: replication, live migration, SR-IOV, GPU partitioning, shielded VMs, image *creation* (build golden images with Packer or DISM and reference them via `hyperv_image_file`).

## Requirements

### Runtime

- **Terraform** >= 1.5
- **A Hyper-V host** running Windows Server 2019+ or Windows 10/11 Pro/Enterprise with the Hyper-V role enabled.
- **Windows PowerShell 5.1** on the host (ships with Windows). PowerShell 7.4+ is supported and tested but not required.
- **One reachable transport** to the host: a local PowerShell installation, OpenSSH, or WinRM (HTTPS recommended).

### Development

- **Go** matching the toolchain in [`go.mod`](go.mod) (currently 1.25+).
- **[Task](https://taskfile.dev)** for the build, lint, test, and docs targets.
- **[aqua](https://aquaproj.github.io/)** to provision pinned versions of `terraform`, `goreleaser`, `gosec`, etc. (see [`aqua.yaml`](aqua.yaml)).
- **PowerShell 7+** to run the [Pester](https://pester.dev/) script-level tests.
- A reachable Hyper-V host for acceptance tests (`task test:acc`); unit tests run anywhere.

## Quickstart

```hcl
terraform {
  required_providers {
    hyperv = {
      source  = "windsorcli/hyperv"
      version = "~> 0.1"
    }
  }
}

# Configuration is environment-driven by default; see "Configuration" below.
provider "hyperv" {}

resource "hyperv_virtual_switch" "lab" {
  name        = "lab-internal"
  switch_type = "Internal"
}

resource "hyperv_image_file" "ubuntu" {
  destination_path = "C:\\hyperv\\images\\ubuntu-22.04.vhdx"
  url = {
    url      = "https://cloud-images.ubuntu.com/jammy/current/jammy-server-cloudimg-amd64.vhdx"
    checksum = "sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
  }
}

resource "hyperv_vhd" "vm01_root" {
  path        = "C:\\hyperv\\vhds\\vm01-root.vhdx"
  vhd_type    = "differencing"
  parent_path = hyperv_image_file.ubuntu.destination_path
}
```

A complete end-to-end example (URL fetch → differencing VHD → cloud-init seed ISO → VM → power on → wait for IP) lives under [`examples/`](examples/) and on the [Terraform Registry](https://registry.terraform.io/providers/windsorcli/hyperv/latest).

## Configuration

Every provider attribute has a corresponding `HYPERV_*` environment variable. **Precedence: provider attribute > env var > zero/error.** Omitting the provider block entirely makes env vars the sole source — useful for shared modules where each user supplies their own host.

### Local backend (provider runs on the Hyper-V host)

```hcl
provider "hyperv" {
  backend = "local"
}
```

### SSH backend

```hcl
provider "hyperv" {
  backend  = "ssh"
  host     = "hv01.lab"
  username = "Administrator"
  ssh = {
    private_key_path = "~/.ssh/id_ed25519"
  }
}
```

The host needs OpenSSH Server enabled with PowerShell as the default shell. See [`docs/guides/host-setup.md`](docs/guides/host-setup.md).

### WinRM backend

```hcl
provider "hyperv" {
  backend  = "winrm"
  host     = "hv01.lab"
  username = "Administrator"
  password = var.hv_password
  winrm = {
    use_https = true
    auth      = "ntlm"
  }
}
```

WinRM HTTPS with NTLM is the recommended configuration for workgroup hosts; Kerberos is supported in domain environments. See [`docs/guides/host-setup.md`](docs/guides/host-setup.md) for the host-side WSMan configuration.

### Environment variables

| Variable | Attribute | Notes |
|---|---|---|
| `HYPERV_BACKEND` | `backend` | `local` \| `ssh` \| `winrm` |
| `HYPERV_HOST` | `host` | Required for `ssh` / `winrm` |
| `HYPERV_PORT` | `port` | Defaults: 22 (ssh), 5986 (winrm) |
| `HYPERV_USERNAME` | `username` | Required for `ssh` / `winrm` |
| `HYPERV_PASSWORD` | `password` | Sensitive |
| `HYPERV_TIMEOUT` | `timeout` | Per-call PS execution timeout (Go duration) |
| `HYPERV_SSH_PRIVATE_KEY` | `ssh.private_key` | Sensitive; key contents |
| `HYPERV_SSH_PRIVATE_KEY_PATH` | `ssh.private_key_path` | Path alternative |
| `HYPERV_SSH_PASSPHRASE` | `ssh.passphrase` | Sensitive |
| `HYPERV_SSH_KNOWN_HOSTS_PATH` | `ssh.known_hosts_path` | Defaults to `~/.ssh/known_hosts` |
| `HYPERV_WINRM_USE_HTTPS` | `winrm.use_https` | Defaults to `true` |
| `HYPERV_WINRM_INSECURE` | `winrm.insecure` | Skip TLS verify |
| `HYPERV_WINRM_AUTH` | `winrm.auth` | `basic` \| `ntlm` \| `kerberos` |
| `HYPERV_WINRM_CACERT` | `winrm.cacert` | Path to a CA bundle |
| `HYPERV_PWSH_PATH` | `local.pwsh_path` | Override PowerShell binary discovery |

A complete `.env.example` is committed at the repository root.

## Documentation

- **Registry**: [registry.terraform.io/providers/windsorcli/hyperv/latest/docs](https://registry.terraform.io/providers/windsorcli/hyperv/latest/docs) (canonical, generated)
- **Repo**: [`docs/`](docs/) — same content; useful when reading the source on a branch
- **Guides**:
  - [Getting started](docs/guides/getting-started.md) — Flow B walkthrough (cloud image → VM → cloud-init)
  - [Configuring backends](docs/guides/backends.md) — local / SSH / WinRM in depth
  - [Hyper-V host setup](docs/guides/host-setup.md) — enabling SSH or WinRM on Server 2019/2022
  - [PowerShell version notes](docs/guides/powershell-versions.md) — 5.1 vs 7.4 behavior
  - [Migrating from taliesins/terraform-provider-hyperv](docs/guides/migrating-from-taliesins.md)

## Building from source

```sh
git clone https://github.com/windsorcli/terraform-provider-hyperv.git
cd terraform-provider-hyperv
task tools          # install pinned dev tools
task                # default: lint + unit tests
task build          # build the provider binary for the current platform
task install        # install to ~/.terraform.d/plugins/ for local Terraform use
```

To use a locally built provider in a Terraform configuration without publishing, add a `dev_overrides` block to `~/.terraformrc`:

```hcl
provider_installation {
  dev_overrides {
    "windsorcli/hyperv" = "/Users/<you>/go/bin"
  }
  direct {}
}
```

`task install` writes the binary to `~/.terraform.d/plugins/registry.terraform.io/windsorcli/hyperv/0.0.0-dev/<os>_<arch>/`; with the override above pointing at `$GOPATH/bin`, run `go install` to drop the binary there directly.

## Testing

The provider is exercised in three tiers, each runnable independently:

```sh
task test:unit      # Go unit tests, no Hyper-V required (fakes the PS runner)
task test:pester    # Pester tests for the embedded .ps1 scripts (PowerShell 7+)
task test:acc       # acceptance tests against a real Hyper-V host
```

> [!CAUTION]
> `task test:acc` creates real Hyper-V resources — virtual switches, VMs, disks. Run only against a host you own. Sweepers (`task sweep`) clean up orphaned resources prefixed with the test name. Tests are gated on `TF_ACC=1`.

Acceptance test configuration lives in `.env.local` (gitignored); copy `.env.example` and fill in `HYPERV_*` values for the backend you want to exercise. The CI matrix runs acceptance against Server 2019 (PS 5.1 only) and Server 2022 (PS 7.4 alongside 5.1) on each of the three backends.

## Debugging

- `TF_LOG=DEBUG` — standard Terraform log level; surfaces provider-level messages.
- `TF_LOG_PROVIDER=DEBUG` — provider-only logs; quieter than `TF_LOG`.
- `TF_LOG_PROVIDER_HYPERV_CONNECTION=DEBUG` — connection-subsystem logs (transport, auth, pooling) without the resource-CRUD chatter.
- `TF_LOG_PROVIDER=TRACE` — full PS stdin/stdout/stderr per call. **Sensitive values are masked**, but enable only when debugging.

To attach a debugger, build with `task build` and run the provider with `-debug`; Terraform will print a `TF_REATTACH_PROVIDERS` env var to set in the shell that runs `terraform plan` or `terraform apply`.

## Known limitations

- **No image creation.** Use Packer or DISM to build golden images and reference them via `hyperv_image_file`.
- **PowerShell startup latency.** Each operation pays the cost of a `pwsh`/`powershell.exe` invocation: ~1.4s per call over SSH, ~2.0s over WinRM, ~0.5–0.8s locally. Terraform's default 10-way parallelism absorbs this for typical fleets; persistent-runspace mode is on the v1 stretch-goal list for >100-resource deployments.
- **WinRM `local_path` transfers cap at 100 MB.** Use `url` (host-side BITS fetch) or `host_path` for larger files.
- **Cancel-mid-cmdlet may leave partial state.** A `New-VM` interrupted after disk creation but before VM registration will require either a re-apply or a manual cleanup.
- **Differencing parent paths surface errors at apply time, not plan time.** `New-VHD` validates the parent path in ~400ms; the provider maps the cmdlet error to an attribute-level diagnostic.

See [`docs/PLAN.md`](docs/PLAN.md) §11.5 for the full list and rationale.

## Contributing

Contributions are welcome. For non-trivial changes — new resources, schema changes, new backends — please open an issue first to align on shape before writing code. Bug fixes and documentation improvements can go straight to a PR.

The repository follows strict TDD: PowerShell scripts get Pester tests first to lock the JSON contract, then Go unit tests with a fake runner, then resource schema tests, then acceptance tests, then implementation. See [`docs/PLAN.md`](docs/PLAN.md) §9 for the full strategy.

PRs require:

- A release-drafter label (`feature`, `fix`, `chore`, `documentation`, `dependencies`, `breaking`, etc.) — enforced in CI.
- All `task lint`, `task test:unit`, `task validate:examples`, `task docs:check`, and `task docs:validate` jobs green.
- Acceptance tests passing on at least one backend (the `acc-test` label gates the self-hosted runner pool).

## License

This provider is distributed under the [Mozilla Public License 2.0](LICENSE).
