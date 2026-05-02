# Changelog

All notable changes to the windsorcli/hyperv Terraform provider are
documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).
Pre-1.0 minor bumps may include breaking changes; pin to an exact version
in production until `v1.0.0` ships.

**This file is updated automatically when a GitHub Release is
published.** Two workflows collaborate:

1. **`release-drafter.yaml`** — runs on every push to `main` and on
   PR open/sync. Maintains a *draft GitHub Release* (visible on the
   repo's `/releases` page) whose body is built from labeled PRs.
   The maintainer never has to touch it directly; PR labels do the
   work.
2. **`changelog-on-release.yaml`** — runs when a release is
   *published* (i.e., the maintainer opens the draft release,
   confirms the version + notes, and clicks "Publish"). It prepends
   the release body into this file as a new versioned section and
   commits the change back to `main` with `[skip ci]`.

So the maintainer's flow at release time is:

1. Open the draft release on github.com, sanity-check the notes
   + version label.
2. Click **Publish release**. (This also creates the tag, which fires
   `release.yaml` to build + sign artifacts via GoReleaser.)
3. The changelog auto-update commit appears on `main` shortly after.

To edit a release after the fact, edit it on github.com and republish
-- the workflow re-runs and updates this file. Hand-editing this file
directly is fine for one-off corrections but will be overwritten on
the next release.

## [Unreleased]

### Added

- Initial release plumbing: GoReleaser config, tag-triggered release
  workflow, GPG-signed checksums.
