---
name: create-pr
description: Push the current branch and open or update its pull request with a calm, prose-first description that matches the project's claude-code-review.yaml style. Generates the description ONLY when the PR has no body (or just whitespace) -- a substantial human- or AI-authored body is preserved untouched. Run after committing and before announcing the PR. Use whenever the user asks to "open the PR", "push and PR", or after `task lint && test:unit && test:pester` are green and the branch is ready.
disable-model-invocation: true
---

# Create or Update PR

Open a pull request for the current branch with a description that matches
the project's review-comment voice: prose paragraphs only, no bullet lists,
no test-plan checklists, no emojis, under ~150 words. Skip body generation
when the PR already has prose -- never overwrite a human-authored
description.

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
4. `gh pr view --json number,body,title 2>/dev/null` — does a PR exist
   already, and does it have a body?

If `gh pr view` returns "no pull requests found", we'll create. Otherwise
we'll update -- but only the body, only when empty.

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

## Body rules

The body lives at the top of the PR. The repo's `claude-code-review`
workflow appends its own `> [!NOTE]` block below this on every push --
do NOT include the `<!-- claude-code-review:summary -->` markers, and do
NOT write a `> [!NOTE]` callout in the body (the workflow's callout is
the canonical AI-authored one).

Hard rules:
- **Prose paragraphs only.** No bullet lists. No code fences except for
  literal CLI examples a reader needs to copy.
- **No "Test plan" checklist.** If a reviewer needs to know what was
  validated, mention it in a sentence.
- **No emojis.**
- **One topic sentence per paragraph.** Reader should grasp the change
  in 15 seconds.
- **Under ~150 words.**
- **Sign off** with `Generated with [Claude Code](https://claude.com/claude-code)` on its own line at the bottom (no emoji prefix).

Recommended shape (1-3 paragraphs):

> 1. **What and why** (1-2 sentences): the change at the highest level
>    and the user-facing motivation.
> 2. **Notable detail** (optional, 2-3 sentences): a constraint, a
>    deliberate trade-off, or a follow-up the reviewer should know
>    about. Skip when the change is straightforward.
> 3. **Risk note** (optional, 1 sentence): when the change touches
>    schema migrations, sensitive attributes, shared infra, or could
>    cause data loss. Otherwise omit.

## Decision tree

```
Does a PR exist for this branch?
├── No  → gh pr create with generated title + body, then print URL.
└── Yes → Read existing body.
    ├── Empty (or only whitespace, or only the workflow's marker block)
    │     → gh pr edit --body with generated body. Title untouched.
    └── Non-empty prose present
          → Print URL only. Do NOT overwrite the body. Tell the user
            "PR already has a description; leaving it as-is."
```

After the PR exists, **always check CI status** (see below). The point
of opening a PR is to get the change reviewed and merged; surfacing a
red check immediately lets the user fix it before they walk away.

## Commands

Always pass body via HEREDOC for proper newline handling. Push first,
then open or update the PR.

```bash
# 1. Push (set upstream on first push of this branch)
git push -u origin "$(git rev-parse --abbrev-ref HEAD)"

# 2. Detect existing PR
PR_JSON=$(gh pr view --json number,body 2>/dev/null || echo '{}')
PR_NUM=$(echo "$PR_JSON" | jq -r '.number // empty')
PR_BODY=$(echo "$PR_JSON" | jq -r '.body // ""')

# 3. Decide
if [ -z "$PR_NUM" ]; then
  # Create
  gh pr create --title "<generated title>" --body "$(cat <<'EOF'
<generated body>

Generated with [Claude Code](https://claude.com/claude-code)
EOF
)"
elif [ -z "$(printf '%s' "$PR_BODY" | python3 -c 'import re, sys; print(re.sub(r"<!-- claude-code-review:summary -->.*?<!-- /claude-code-review:summary -->\s*", "", sys.stdin.read(), flags=re.DOTALL), end="")' | tr -d '[:space:]')" ]; then
  # Empty body (or only the workflow's markers/whitespace) — update
  gh pr edit "$PR_NUM" --body "$(cat <<'EOF'
<generated body>

Generated with [Claude Code](https://claude.com/claude-code)
EOF
)"
else
  echo "PR #$PR_NUM already has a description; leaving it as-is."
  gh pr view --json url --jq .url
fi
```

If `gh` returns `HTTP 401: Bad credentials`, the operator has a stale
`GITHUB_TOKEN` env var overriding the keychain. Prepend `unset GITHUB_TOKEN`
to the failing command and retry.

If `gh pr edit` returns a `projectCards` GraphQL error (deprecated API),
fall back to `gh api -X PATCH "repos/$REPO/pulls/$PR_NUM"` with `-f
title=...` and `-F body=@/tmp/body.md` -- it bypasses the broken path.

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
- Don't include a "Test plan" / `- [x]` checklist.
- Don't enumerate every file that changed.
- Don't use the `> [!NOTE]` callout (workflow owns it).
- Don't include the `<!-- claude-code-review:summary -->` markers.
- Don't overwrite a non-empty PR body.
- Don't push to `main` directly. Always operate on a feature branch.
- Don't run `git push --force` unless the user explicitly asked.

## After posting

Print the PR URL on its own line so the operator can click through.
