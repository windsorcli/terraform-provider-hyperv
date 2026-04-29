# Project conventions

Read this file before any commit or PR. The rules below are non-negotiable
unless the user asks otherwise.

## Commit titles

- **Format**: `<type>(<scope>?): <description>` (Conventional Commits).
- **Types**: `feat`, `fix`, `chore`, `docs`, `test`, `refactor`, `ci`,
  `perf`, `build`. Pick from this fixed set.
- **Scope** (optional, lowercase, single word): the package or area touched
  (`vm`, `connection`, `vhd`, `deps`, etc.). Multi-word scopes are a
  smell — split the commit.
- **Description**: lowercase first letter, imperative or descriptive, no
  trailing punctuation.
- **Length**: target ≤ 65 chars total, hard cap 72.

Anti-patterns: milestone tags as prefix (`M4: ...`), multi-clause titles
joined with "and" or `+`, verbose nouns where a verb fits.

Sample `git log main --pretty=format:'%s' | head -20` to verify shape
before authoring a new title.

## PR titles and bodies

- **Title**: same Conventional Commits shape as a commit title. ≤ 72
  chars. No `(#NNN)` suffix — GitHub adds the number.
- **Body**: calm, prose-first paragraphs. No bullet lists, no test-plan
  checklists, no emojis. Under ~150 words.
- **Don't write the `> [!NOTE]` callout block in the dev-machine body** —
  the `claude-code-review.yaml` workflow appends its own callout below
  on every push. Two callouts on a PR is noise.
- **Don't include the `<!-- claude-code-review:summary -->` markers** —
  the workflow owns that surface.
- See `.claude/skills/create-pr/SKILL.md` for the full procedure.

## Working in this repo

- All PowerShell scripts must run on PS 5.1 (Server 2022 floor) and PS
  7.4. See `docs/PLAN.md` §5 for the script contract.
- Acceptance tests run against a real Hyper-V bench at the host listed
  in `.env.local`. See `internal/scripts/vm/` for the cmdlet wrappers.
- Maintainer-only docs (`docs/PLAN.md`, `docs/spikes/`, `docs/adr/`) are
  gitignored. Don't reference them in commit messages or user-facing
  docs without confirming they'll never be public.
