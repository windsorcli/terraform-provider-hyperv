---
name: create-pr
description: Push the current branch and open or update its pull request with a Conventional-Commits title. Never writes or touches the PR body -- the repo's automated review posts its own description on the PR, so a skill-generated one would just be redundant. Run after committing and before announcing the PR. Use whenever the user asks to "open the PR", "push and PR", or after `task lint && test:unit && test:pester` are green and the branch is ready.
disable-model-invocation: true
---

# Create or Update PR

Open a pull request for the current branch with a Conventional-Commits
title. Leave the body empty on create, and never touch it on an existing
PR — the repo's automated review already writes the PR's description, so
generating a second one here would just be noise on top of it.

## Apply when
- The user says "open the PR", "push the branch and PR", "make a PR", or
  similar after work is committed.
- All gates have passed locally (lint + unit + pester) and the branch has
  commits ahead of `main`.
- Skip if `git status -s` shows uncommitted changes that should be in the
  PR -- ask the user to commit or stash first.

## Inputs to gather
1. `git rev-parse --abbrev-ref HEAD` — branch name.
2. `git log main..HEAD --oneline` — commits the PR contains.
3. `git diff main...HEAD --stat` — file-level shape of the change.
4. `gh pr view --json number,title 2>/dev/null` — does a PR exist already?

If `gh pr view` returns "no pull requests found", we'll create one. If a
PR already exists, this skill has nothing left to do beyond confirming
CI — see the decision tree below.

## Title rules

Hard requirements:
- **Conventional Commits shape**: `<type>(<scope>?): <description>`.
- **Types**: `feat`, `fix`, `chore`, `docs`, `test`, `refactor`, `ci`,
  `perf`, `build`. Pick from this fixed set.
- **Scope** (optional, lowercase): the package or area touched, e.g.
  `vm`, `connection`, `vhd`, `deps`. Single word; multi-word scopes are a
  smell.
- **Description**: lowercase first letter, imperative or descriptive,
  no trailing punctuation.
- **Length**: aim for ≤ 65 chars total. Hard cap at 72.
- **Same shape as commit titles** in the project — sample
  `git log main --pretty=format:%s | head -20` to verify.

Examples that fit: `feat(vm): inline boot_order on gen 2`,
`fix(connection): bound ssh ctx with command timeout`,
`chore(deps): bump terraform-plugin-framework to v1.20`.

Anti-patterns to avoid:
- ALL CAPS prefixes ("M4: ..."): drop the milestone tag.
- Multi-clause titles with "and" or `+`: pick the bigger half, list the
  rest in the body.
- Verbose nouns ("complete the hyperv_vm resource"): use the verb
  ("complete hyperv_vm").

## Why no body

The repo's `claude-code-review` workflow writes the PR's description
itself once CI runs. A skill-generated prose body would either get
overwritten or sit alongside the workflow's own writeup as duplicate,
possibly conflicting narration — neither is useful. So this skill's job
stops at the title: create with an empty body, and never touch the body
of a PR that already exists, no matter what's in it.

## Decision tree

```
Does a PR exist for this branch?
├── No  → gh pr create with the generated title and an empty body,
│         then print URL.
└── Yes → Print the URL. Title and body both untouched.
```

After the PR exists, **always check CI status** (see below). The point
of opening a PR is to get the change reviewed and merged; surfacing a
red check immediately lets the user fix it before they walk away.

## Commands

Push first, then open the PR if one doesn't exist yet.

```bash
# 1. Push (set upstream on first push of this branch)
git push -u origin "$(git rev-parse --abbrev-ref HEAD)"

# 2. Detect existing PR
PR_NUM=$(gh pr view --json number --jq '.number' 2>/dev/null)

# 3. Decide
if [ -z "$PR_NUM" ]; then
  gh pr create --title "<generated title>" --body ""
  PR_NUM=$(gh pr view --json number --jq '.number')
else
  echo "PR #$PR_NUM already exists; title and body left as-is."
  gh pr view --json url --jq .url
fi
```

If `gh` returns `HTTP 401: Bad credentials`, the operator has a stale
`GITHUB_TOKEN` env var overriding the keychain. Prepend `unset GITHUB_TOKEN`
to the failing command and retry.

## CI status check

After the PR exists (just created OR already-existed), run a quick
status check. CI typically kicks off within a few seconds of the push,
but most pipelines take 1-5 minutes to complete. Two modes:

```bash
# Snapshot: list current state of every check, no waiting.
gh pr checks "$PR_NUM"
```

If any row shows `fail` or `failure`, surface those rows to the user
verbatim and direct them to the failing job's URL (the rightmost
column of `gh pr checks`). Don't try to diagnose the failure from the
skill -- the failing job's logs are the source of truth.

If every row shows `pending` or `queued`, that's expected on a fresh
push. Print the URL with a "checks running" hint. Do NOT block the
skill on completion -- the user can run `gh pr checks --watch` to
follow them.

If every row shows `pass`, say so explicitly. The user shouldn't have
to scroll back to verify.

## What NOT to do
- Don't write a PR body, on create or update — the automated review
  owns the description.
- Don't touch the body of a PR that already exists, empty or not.
- Don't push to `main` directly. Always operate on a feature branch.
- Don't run `git push --force` unless the user explicitly asked.

## After posting

Print the PR URL on its own line so the operator can click through.
