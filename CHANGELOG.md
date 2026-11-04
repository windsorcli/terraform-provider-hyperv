# Changelog

All notable changes to the windsorcli/hyperv Terraform provider are
documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).
Pre-1.0 minor bumps may include breaking changes; pin to an exact version
in production until `v1.0.0` ships.

Each section is copied from the release-drafter draft in a
`docs(changelog)` pull request that merges before the release tag is
pushed. See [Cutting a release](docs/contributing/releasing.md). The
running draft for the next version is on the
[Releases](https://github.com/windsorcli/terraform-provider-hyperv/releases)
page, along with the notes for releases before `v0.5.0`.

## [0.6.0] - 2026-11-04

### Features

- feat(portproxy): add hyperv\_port\_proxy resource @rmvangun (#438)
- feat(vm): wait for a guest IPv4 address on create @rmvangun (#436)
- feat(vm): make path settable, moved in place with Move-VMStorage @rmvangun (#437)

### Bug Fixes

- fix(portproxy): re-add a rule that has no listener @rmvangun (#449)
- fix(vswitch): refuse delete while a running VM is attached @rmvangun (#450)
- fix(vm): plan ip\_addresses as unknown on any update @rmvangun (#448)
- fix(nat): count a disabled firewall rule as absent @rmvangun (#441)
- fix(connection): wait on winrm commands before reading errors @rmvangun (#435)

### Maintenance

- docs: tighten the v0.6.0 resource docs @rmvangun (#451)
- refactor(firewall): share the firewall\_rule handling @rmvangun (#446)
- chore(renovate): remove the invalid github-tags versioning @rmvangun (#417)

### Dependencies

<details>
<summary>63 changes</summary>

- chore(deps): update anthropics/claude-code-action action to v1.0.267 @[renovate[bot]](https://github.com/apps/renovate) (#447)
- chore(deps): update dependency aquaproj/aqua-registry to v4.581.1 @[renovate[bot]](https://github.com/apps/renovate) (#440)
- chore(deps): update dependency aquaproj/aqua-registry to v4.581.0 @[renovate[bot]](https://github.com/apps/renovate) (#439)
- chore(deps): update dependency aquaproj/aqua-registry to v4.580.0 @[renovate[bot]](https://github.com/apps/renovate) (#434)
- chore(deps): update anthropics/claude-code-action action to v1.0.266 @[renovate[bot]](https://github.com/apps/renovate) (#433)
- chore(deps): update anthropics/claude-code-action action to v1.0.265 @[renovate[bot]](https://github.com/apps/renovate) (#432)
- chore(deps): update dependency hashicorp/terraform to v1.17.1 @[renovate[bot]](https://github.com/apps/renovate) (#431)
- chore(deps): update dependency aquaproj/aqua-registry to v4.579.0 @[renovate[bot]](https://github.com/apps/renovate) (#430)
- chore(deps): update anthropics/claude-code-action action to v1.0.264 @[renovate[bot]](https://github.com/apps/renovate) (#429)
- chore(deps): update anthropics/claude-code-action action to v1.0.263 @[renovate[bot]](https://github.com/apps/renovate) (#428)
- chore(deps): update dependency aquaproj/aqua-registry to v4.578.0 @[renovate[bot]](https://github.com/apps/renovate) (#426)
- chore(deps): update dependency aquaproj/aqua-renovate-config to v2.14.0 @[renovate[bot]](https://github.com/apps/renovate) (#425)
- chore(deps): update dependency aquaproj/aqua-registry to v4.577.0 @[renovate[bot]](https://github.com/apps/renovate) (#424)
- chore(deps): update dependency securego/gosec to v2.30.0 @[renovate[bot]](https://github.com/apps/renovate) (#420)
- chore(deps): update anthropics/claude-code-action action to v1.0.262 @[renovate[bot]](https://github.com/apps/renovate) (#422)
- chore(deps): update dependency goreleaser/goreleaser to v2.19.0 @[renovate[bot]](https://github.com/apps/renovate) (#419)
- chore(deps): update dependency aquaproj/aqua-registry to v4.576.0 @[renovate[bot]](https://github.com/apps/renovate) (#421)
- chore(deps): update stefanzweifel/git-auto-commit-action action to v7.2.1 @[renovate[bot]](https://github.com/apps/renovate) (#418)
- chore(deps): update anthropics/claude-code-action action to v1.0.261 @[renovate[bot]](https://github.com/apps/renovate) (#412)
- chore(deps): update actions/setup-python digest to 6d4fd22 @[renovate[bot]](https://github.com/apps/renovate) (#413)
- chore(deps): update dependency hashicorp/terraform to v1.17.0 @[renovate[bot]](https://github.com/apps/renovate) (#411)
- chore(deps): update anthropics/claude-code-action action to v1.0.256 @[renovate[bot]](https://github.com/apps/renovate) (#409)
- chore(deps): update go toolchain directive to v1.27.3 @[renovate[bot]](https://github.com/apps/renovate) (#408)
- chore(deps): update dependency golang/go to v1.27.3 @[renovate[bot]](https://github.com/apps/renovate) (#407)
- chore(deps): update anthropics/claude-code-action action to v1.0.254 @[renovate[bot]](https://github.com/apps/renovate) (#406)
- chore(deps): update anthropics/claude-code-action action to v1.0.253 @[renovate[bot]](https://github.com/apps/renovate) (#405)
- chore(deps): update anthropics/claude-code-action action to v1.0.252 @[renovate[bot]](https://github.com/apps/renovate) (#403)
- chore(deps): update dependency golangci/golangci-lint to v2.15.0 @[renovate[bot]](https://github.com/apps/renovate) (#401)
- chore(deps): update anthropics/claude-code-action action to v1.0.251 @[renovate[bot]](https://github.com/apps/renovate) (#400)
- chore(deps): update dependency aquaproj/aqua-registry to v4.575.0 @[renovate[bot]](https://github.com/apps/renovate) (#398)
- chore(deps): update anthropics/claude-code-action action to v1.0.250 @[renovate[bot]](https://github.com/apps/renovate) (#399)
- chore(deps): update dependency hashicorp/terraform to v1.16.6 @[renovate[bot]](https://github.com/apps/renovate) (#397)
- chore(deps): update anthropics/claude-code-action action to v1.0.249 @[renovate[bot]](https://github.com/apps/renovate) (#396)
- chore(deps): update goreleaser/goreleaser-action action to v7.3.0 @[renovate[bot]](https://github.com/apps/renovate) (#395)
- chore(deps): update dependency aquaproj/aqua-registry to v4.574.1 @[renovate[bot]](https://github.com/apps/renovate) (#393)
- fix(deps): update module github.com/klauspost/compress to v1.20.3 @[renovate[bot]](https://github.com/apps/renovate) (#394)
- chore(deps): update dependency aquaproj/aqua-registry to v4.574.0 @[renovate[bot]](https://github.com/apps/renovate) (#392)
- chore(deps): update anthropics/claude-code-action action to v1.0.248 @[renovate[bot]](https://github.com/apps/renovate) (#391)
- fix(deps): update module golang.org/x/crypto to v0.58.0 @[renovate[bot]](https://github.com/apps/renovate) (#390)
- chore(deps): update dependency goreleaser/goreleaser to v2.18.3 @[renovate[bot]](https://github.com/apps/renovate) (#389)
- chore(deps): update dependency aquaproj/aqua-registry to v4.573.1 @[renovate[bot]](https://github.com/apps/renovate) (#388)
- chore(deps): update go toolchain directive to v1.27.2 @[renovate[bot]](https://github.com/apps/renovate) (#387)
- chore(deps): update anthropics/claude-code-action action to v1.0.247 @[renovate[bot]](https://github.com/apps/renovate) (#385)
- chore(deps): update dependency golang/go to v1.27.2 @[renovate[bot]](https://github.com/apps/renovate) (#386)
- chore(deps): update release-drafter/release-drafter action to v7.10.0 @[renovate[bot]](https://github.com/apps/renovate) (#383)
- chore(deps): update anthropics/claude-code-action action to v1.0.245 @[renovate[bot]](https://github.com/apps/renovate) (#382)
- chore(deps): update dependency aquaproj/aqua-registry to v4.573.0 @[renovate[bot]](https://github.com/apps/renovate) (#381)
- chore(deps): update anthropics/claude-code-action action to v1.0.244 @[renovate[bot]](https://github.com/apps/renovate) (#380)
- chore(deps): update anthropics/claude-code-action action to v1.0.243 @[renovate[bot]](https://github.com/apps/renovate) (#379)
- chore(deps): update dependency aquaproj/aqua-registry to v4.572.0 @[renovate[bot]](https://github.com/apps/renovate) (#378)
- fix(deps): update github.com/masterzen/winrm digest to 57b3353 @[renovate[bot]](https://github.com/apps/renovate) (#377)
- chore(deps): update anthropics/claude-code-action action to v1.0.241 @[renovate[bot]](https://github.com/apps/renovate) (#376)
- chore(deps): update dependency aquaproj/aqua-registry to v4.571.0 @[renovate[bot]](https://github.com/apps/renovate) (#375)
- chore(deps): update anthropics/claude-code-action action to v1.0.240 @[renovate[bot]](https://github.com/apps/renovate) (#374)
- chore(deps): update dependency hashicorp/terraform to v1.16.5 @[renovate[bot]](https://github.com/apps/renovate) (#373)
- chore(deps): update release-drafter/release-drafter action to v7.9.0 @[renovate[bot]](https://github.com/apps/renovate) (#370)
- chore(deps): update dependency go-task/task to v3.54.0 @[renovate[bot]](https://github.com/apps/renovate) (#372)
- chore(deps): update anthropics/claude-code-action action to v1.0.239 @[renovate[bot]](https://github.com/apps/renovate) (#371)
- chore(deps): update dependency aquaproj/aqua-registry to v4.570.0 @[renovate[bot]](https://github.com/apps/renovate) (#369)
- chore(deps): update anthropics/claude-code-action action to v1.0.238 @[renovate[bot]](https://github.com/apps/renovate) (#368)
- chore(deps): update dependency aquaproj/aqua-registry to v4.569.0 @[renovate[bot]](https://github.com/apps/renovate) (#367)
- chore(deps): update anthropics/claude-code-action action to v1.0.237 @[renovate[bot]](https://github.com/apps/renovate) (#366)
- chore(deps): update dependency mikefarah/yq to v4.54.1 @[renovate[bot]](https://github.com/apps/renovate) (#365)
</details>

## [0.5.0] - 2026-09-29

### Features

- feat(vm): expose vm\_id, Hyper-V's own VM GUID @rmvangun (#364)
- feat(imagefile): skip re-fetch and refuse drifted deletes @rmvangun (#353)

### Bug Fixes

- fix(connection): bound ssh ctx with command timeout @rmvangun (#359)
- fix(vhd): serialize destroy for a shared destination\_path @rmvangun (#356)
- fix(imagefile): serialize destroy for a shared destination\_path @rmvangun (#354)
- fix(imagefile): adopt matching content at destination\_path @rmvangun (#352)
- fix(vm): treat hard\_disk\_drive/network\_adapter as list @rmvangun (#346)
- fix(vm): resolve checkpoint avhdx path to base disk @rmvangun (#348)
- fix(scripts): resolve psscriptanalyzer warnings @rmvangun (#347)

### Maintenance

- docs: tighten registry docs and trim example bloat @rmvangun (#363)
- chore: trim verbose comments to house style @rmvangun (#361)
- chore(skills): fix PR-automation gaps in two skills @rmvangun (#357)
- test(vm): cover variable-driven attachments and checkpoint read @rmvangun (#355)
- ci(review): trigger on-demand review from /review comment @rmvangun (#206)

### Dependencies

<details>
<summary>48 changes</summary>

- chore(deps): update anthropics/claude-code-action action to v1.0.236 @[renovate[bot]](https://github.com/apps/renovate) (#362)
- chore(deps): update dependency aquaproj/aqua-registry to v4.568.0 @[renovate[bot]](https://github.com/apps/renovate) (#351)
- chore(deps): update module github.com/hashicorp/terraform-plugin-docs/cmd/tfplugindocs to v0.25.0 @[renovate[bot]](https://github.com/apps/renovate) (#15)
- fix(deps): update module golang.org/x/crypto to v0.57.0 @[renovate[bot]](https://github.com/apps/renovate) (#336)
- fix(deps): update module github.com/klauspost/compress to v1.20.1 @[renovate[bot]](https://github.com/apps/renovate) (#334)
- chore(deps): update module github.com/golangci/golangci-lint/v2/cmd/golangci-lint to v2.14.0 @[renovate[bot]](https://github.com/apps/renovate) (#350)
- chore(deps): update dependency golangci/golangci-lint to v2.14.0 @[renovate[bot]](https://github.com/apps/renovate) (#349)
- chore(deps): update dependency aquaproj/aqua-registry to v4.567.0 @[renovate[bot]](https://github.com/apps/renovate) (#342)
- chore(deps): update codecov/codecov-action action to v7.1.1 @[renovate[bot]](https://github.com/apps/renovate) (#344)
- fix(deps): update module github.com/ulikunitz/xz to v0.5.17 @[renovate[bot]](https://github.com/apps/renovate) (#345)
- chore(deps): update dependency hashicorp/terraform to v1.16.4 @[renovate[bot]](https://github.com/apps/renovate) (#341)
- chore(deps): update anthropics/claude-code-action action to v1.0.235 @[renovate[bot]](https://github.com/apps/renovate) (#337)
- chore(deps): update dependency goreleaser/goreleaser to v2.18.2 @[renovate[bot]](https://github.com/apps/renovate) (#338)
- chore(deps): update codecov/codecov-action digest to 303a32d @[renovate[bot]](https://github.com/apps/renovate) (#343)
- chore(deps): update anthropics/claude-code-action action to v1.0.215 @[renovate[bot]](https://github.com/apps/renovate) (#335)
- chore(deps): update dependency hashicorp/terraform to v1.16.1 @[renovate[bot]](https://github.com/apps/renovate) (#333)
- chore(deps): update go toolchain directive to v1.27.1 @[renovate[bot]](https://github.com/apps/renovate) (#332)
- chore(deps): update dependency golang/go to v1.27.1 @[renovate[bot]](https://github.com/apps/renovate) (#331)
- chore(deps): update anthropics/claude-code-action action to v1.0.213 @[renovate[bot]](https://github.com/apps/renovate) (#330)
- chore(deps): update dependency aquaproj/aqua-registry to v4.558.1 @[renovate[bot]](https://github.com/apps/renovate) (#329)
- chore(deps): update anthropics/claude-code-action action to v1.0.211 @[renovate[bot]](https://github.com/apps/renovate) (#328)
- chore(deps): update dependency aquaproj/aqua-registry to v4.558.0 @[renovate[bot]](https://github.com/apps/renovate) (#327)
- chore(deps): update dependency aquaproj/aqua-registry to v4.557.0 @[renovate[bot]](https://github.com/apps/renovate) (#326)
- chore(deps): update module github.com/golangci/golangci-lint/v2/cmd/golangci-lint to v2.13.2 @[renovate[bot]](https://github.com/apps/renovate) (#324)
- chore(deps): update anthropics/claude-code-action action to v1.0.210 @[renovate[bot]](https://github.com/apps/renovate) (#325)
- chore(deps): update dependency golangci/golangci-lint to v2.13.2 @[renovate[bot]](https://github.com/apps/renovate) (#323)
- chore(deps): update anthropics/claude-code-action action to v1.0.209 @[renovate[bot]](https://github.com/apps/renovate) (#322)
- chore(deps): update dependency securego/gosec to v2.29.0 @[renovate[bot]](https://github.com/apps/renovate) (#319)
- chore(deps): update dependency aquaproj/aqua-registry to v4.556.0 @[renovate[bot]](https://github.com/apps/renovate) (#321)
- chore(deps): update anthropics/claude-code-action action to v1.0.207 @[renovate[bot]](https://github.com/apps/renovate) (#320)
- chore(deps): update dependency hashicorp/terraform to v1.16.0 @[renovate[bot]](https://github.com/apps/renovate) (#318)
- chore(deps): update anthropics/claude-code-action action to v1.0.206 @[renovate[bot]](https://github.com/apps/renovate) (#315)
- chore(deps): update dependency aquaproj/aqua-registry to v4.555.0 @[renovate[bot]](https://github.com/apps/renovate) (#316)
- chore(deps): update dependency goreleaser/goreleaser to v2.18.0 @[renovate[bot]](https://github.com/apps/renovate) (#314)
- chore(deps): update anthropics/claude-code-action action to v1.0.201 @[renovate[bot]](https://github.com/apps/renovate) (#313)
- chore(deps): update anthropics/claude-code-action action to v1.0.200 @[renovate[bot]](https://github.com/apps/renovate) (#312)
- chore(deps): update anthropics/claude-code-action action to v1.0.199 @[renovate[bot]](https://github.com/apps/renovate) (#311)
- chore(deps): update go toolchain directive to v1.27.0 @[renovate[bot]](https://github.com/apps/renovate) (#305)
- chore(deps): update module github.com/golangci/golangci-lint/v2/cmd/golangci-lint to v2.13.1 @[renovate[bot]](https://github.com/apps/renovate) (#308)
- chore(deps): update dependency golangci/golangci-lint to v2.13.1 @[renovate[bot]](https://github.com/apps/renovate) (#304)
- chore(deps): update anthropics/claude-code-action action to v1.0.198 @[renovate[bot]](https://github.com/apps/renovate) (#310)
- chore(deps): update dependency golang/go to v1.27.0 @[renovate[bot]](https://github.com/apps/renovate) (#303)
- chore(deps): update dependency mikefarah/yq to v4.53.6 @[renovate[bot]](https://github.com/apps/renovate) (#309)
- chore(deps): update dependency aquaproj/aqua-registry to v4.554.0 @[renovate[bot]](https://github.com/apps/renovate) (#307)
- chore(deps): update anthropics/claude-code-action action to v1.0.197 @[renovate[bot]](https://github.com/apps/renovate) (#306)
- chore(deps): update dependency mikefarah/yq to v4.53.4 @[renovate[bot]](https://github.com/apps/renovate) (#301)
- chore(deps): update anthropics/claude-code-action action to v1.0.196 @[renovate[bot]](https://github.com/apps/renovate) (#302)
- chore(deps): update dependency hashicorp/terraform to v1.15.9 @[renovate[bot]](https://github.com/apps/renovate) (#300)
</details>
