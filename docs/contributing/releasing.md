# Cutting a release

Releases of `terraform-provider-hyperv` are published as immutable GitHub Releases — once a tag is published, the binaries, checksums, and signature are byte-frozen and cannot be retroactively swapped. This is a hard requirement for the regulated environments we ship into; the release pipeline is built around it.

This doc covers the maintainer release loop: labelling PRs so release notes draft themselves, cutting a tag, watching the workflow, and recovering when something goes wrong.

## Versioning and PR labels

Release-drafter watches `main` and keeps a tagless draft release with notes auto-curated from merged PR titles. The draft also resolves the *next* version number from PR labels, so the version you tag is whatever the draft is pointing at. The label-to-bump mapping lives in [`.github/release-drafter.yml`](../../.github/release-drafter.yml):

| Label | Effect on next version |
|---|---|
| `major`, `breaking` | Major bump (`1.2.3` → `2.0.0`) |
| `feature`, `enhancement` | Minor bump (`1.2.3` → `1.3.0`) |
| `fix`, `bugfix`, `bug` | Patch bump (`1.2.3` → `1.2.4`) |
| `chore`, `dependencies`, `documentation` | Patch bump |
| (no label) | Patch (default) |

Labels also drive section grouping in the drafted release notes (Features / Bug Fixes / Maintenance / Dependencies). Label every PR before merging so the resolved version and the drafted notes both reflect intent. You can preview the running draft any time on the [Releases](https://github.com/windsorcli/terraform-provider-hyperv/releases) page — look for the topmost entry tagged "Draft".

## Cutting the release

Three commands from a clean `main`:

```sh
git checkout main && git pull
git tag vX.Y.Z          # match release-drafter's resolved version
git push origin vX.Y.Z
```

The tag push triggers [`.github/workflows/release.yaml`](../../.github/workflows/release.yaml). The workflow runs end-to-end in roughly five minutes:

1. Imports the GPG release-signing key from the `GPG_PRIVATE_KEY` repo secret. The fingerprint is exposed as a step output and threaded into GoReleaser as `GPG_FINGERPRINT`.
2. Captures release-drafter's curated notes from its tagless draft (matched by `tag_name`) into a temp file. This must happen before GoReleaser runs because GoReleaser's `replace_existing_draft: true` will delete the release-drafter draft and create a fresh empty one in its place.
3. Runs GoReleaser in release mode. It cross-compiles 13 binaries (linux / darwin / windows / freebsd × supported arches per [.goreleaser.yml](../../.goreleaser.yml)), zips each, generates `SHA256SUMS`, GPG-signs the checksums file, and attaches the registry manifest.
4. Creates the GitHub Release as a **draft**. Drafts are mutable, so all 16 assets (13 zips + `SHA256SUMS` + signature + manifest) upload cleanly.
5. The final workflow step applies the captured release-drafter notes as the release body and flips the draft to **published** in a single call. At that moment the release becomes immutable — no further changes possible. Every byte that ships was already in place, and the notes that the maintainer reviewed in the running draft are now the published body.
6. The Terraform Registry's webhook fires on the publish event and pulls the manifest. The new version becomes installable via `required_providers` within a minute or two.

The two-step "create draft, then publish" is what makes the release pipeline compatible with immutability. If you ever see a release fail with `422 Cannot upload assets to an immutable release`, the most likely cause is that the workflow ran from a commit predating the draft-mode goreleaser config — re-tag from `main` HEAD.

## Verifying after the workflow finishes

Spot-check three things in this order:

1. **The workflow run** — green? Any non-fatal warnings worth a follow-up issue? See [Actions](https://github.com/windsorcli/terraform-provider-hyperv/actions).
2. **The release page** — does it list every expected asset? You should see 13 `_<os>_<arch>.zip` files, one `_SHA256SUMS`, one `_SHA256SUMS.sig`, and one `_manifest.json`. URL pattern: `https://github.com/windsorcli/terraform-provider-hyperv/releases/tag/vX.Y.Z`.
3. **The Terraform Registry** — does the new version show up at `https://registry.terraform.io/providers/windsorcli/hyperv/vX.Y.Z`? Initial publication of a brand-new provider can take several minutes; subsequent versions are usually visible within a minute.

If any of those is missing, treat it as a release failure and recover before users see it.

## Recovering from a failed release

The two-step flow makes most failures non-destructive:

- **Workflow failed before the publish step.** The release exists as a draft with whatever assets uploaded successfully. Drafts are mutable, so you can either delete and re-tag, or fix forward by re-running the workflow — [`.goreleaser.yml`](../../.goreleaser.yml) sets `replace_existing_draft: true` in the `release` block, so a re-run overwrites the prior draft cleanly:

  ```sh
  gh release delete vX.Y.Z --yes --cleanup-tag
  git tag vX.Y.Z   # from corrected main
  git push origin vX.Y.Z
  ```

- **Workflow failed at or after the publish step.** The release is now immutable. You can still delete it with `gh release delete --cleanup-tag` (deletion is allowed; modification isn't) and burn the version. The Terraform Registry never registers a release with missing assets, so the version number is reusable as long as you delete before fixing.

- **Wrong assets shipped (e.g., manifest version mismatch).** Same as above — delete, fix the underlying config, re-tag. Don't try to "patch" a published immutable release; the answer is always to cut a fresh version.

## Pre-flight: catching release-config drift before tagging

The `Release snapshot (goreleaser)` job in [.github/workflows/ci.yaml](../../.github/workflows/ci.yaml) runs on every PR and exercises the full GoReleaser pipeline (`task release:snapshot`) minus publish and sign — same 13-binary build, same archives, same checksums, same manifest, just no GitHub side-effects. If `.goreleaser.yml` drifts (broken archive name template, missing manifest glob, signs-block typo) it surfaces here, not at release time when an immutable release would burn the version.

You can run the snapshot locally before tagging if you want extra assurance:

```sh
task release:snapshot
```

Output lands in `dist/`. Eyeball the 13 zips, the `*_SHA256SUMS`, and the `*_manifest.json` — that's exactly what the real release will produce, minus the GPG signature.

## Known gaps

- **No staging Registry.** Once a tag publishes, downstream Terraform users on the public Registry can install it. Cut pre-release tags (`v0.1.0-rc1`) when you want a sanity-check release without exposing it as the latest stable.

- **Release-drafter's draft and the version you tag must match.** The capture step matches release-drafter's draft by `tag_name`, so if you push `v0.2.0` while release-drafter has resolved `v0.1.4` (e.g. you overrode the auto-resolved version), the capture finds nothing and the published release has no notes. Either re-label PRs so release-drafter resolves to the version you intend to tag, or copy the orphaned draft body into the published release manually after the fact.
