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

## Code comments

- **Length**: package doc ≤ 3 lines. A doc comment on an exported
  type/func, or on a field guarding a real invariant (a lock's scope,
  an ordering requirement): one summary sentence, plus up to two more
  only for that invariant — 6 lines hard cap. Everything else inline:
  1 line, never wrapped. `task lint:comments` enforces a flat 10-line
  backstop on a doc comment (it can't tell which of the sub-caps above
  applies), so treat those as the actual target, not the ceiling.
- **A comment inside a function body: one line, always.** No exception,
  no escape hatch — not even the wire-contract one below. A doc comment
  sitting right above a multi-line internal comment at the top of the
  same function is its own smell: say the thing once, in the doc
  comment, not twice. If a line of code needs more than one line of
  explanation, that's a sign that the WHY belongs in the doc comment
  above the function, in a well-named helper extracted from that
  section, or in the commit message.
- **Wire-contract exception**: a *doc* comment documenting an external
  format (a script's stdin/stdout JSON contract, a wire protocol) is
  reference data, not prose, and is exempt from the length cap. Mark it
  by making `lint:allow-long-comment` the block's last line, so it
  doesn't become the godoc synopsis for an exported identifier. This
  exception never applies inside a function body.
- **One job per comment.** A struct comment covering five different
  concerns is a sign each concern belongs on the field it explains, not
  in a preamble above the type.
- **No process or ticket narration**: no PR or issue numbers, no
  "previously", no "used to be". A comment states current behavior, not
  its history — put history in the commit message.
- **No `docs/PLAN.md` / `docs/spikes/` / `docs/adr/` references** — see
  above; paraphrase the finding instead.
- **No lazy stand-in words.** `shape` is this repo's worst offender —
  say format, structure, contract, schema, or layout instead, whichever
  one is actually true. The `technical-writing` skill's §13 catalog
  (leverage, seamless, robust, ensure, essentially, and the rest) is the
  general list; this repo adds `shape` to it.

`task lint:comments` (`hack/lint-comments`) enforces the four bullets
above; it runs in CI. For `*.go` files it parses the AST to tell a doc
comment from a function-body comment, so the one-line body rule is
exact, not a heuristic. Its only override is the `lint:allow-long-comment`
marker on a doc comment — if a body comment or an unmarked doc comment
still needs an exception, shorten it instead.

Two rules above don't have a mechanical check and won't get one:

- **No uncited platform claims.** State what was directly observed
  (`parallel writes have returned ERROR_SHARING_VIOLATION`), not an
  unverified claim about how Windows or another external system works
  internally, unless a source is linked.
- **Dash overuse is a symptom, not the target.** Reaching for `--` as
  the connector for every clause is usually standing in for a staged
  contrast or a clause bolted onto another instead of a real sentence —
  the same tells the `technical-writing` skill catalogs. Swapping the
  dash for a colon and leaving the sentence's structure alone doesn't
  fix that; rewrite the sentence. A regex can't distinguish a real
  rewrite from a punctuation swap, so this gets caught by reading the
  comment against that skill during review, not by `lint:comments`.

## Working in this repo

- All PowerShell scripts must run on PS 5.1 (Server 2022 floor) and PS
  7.4. See `docs/PLAN.md` §5 for the script contract.
- Acceptance tests run against a real Hyper-V bench at the host listed
  in `.env.local`. See `internal/scripts/vm/` for the cmdlet wrappers.
- Maintainer-only docs (`docs/PLAN.md`, `docs/spikes/`, `docs/adr/`) are
  gitignored. Don't reference them in commit messages or user-facing
  docs without confirming they'll never be public.
