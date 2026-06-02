# Articles

15 article topics extracted from this repo's design and build journey,
each rendered as both a LinkedIn variant (~800-1500 words) and a Medium
variant (~1500-3000 words). Same idea per row, two different openings
and two different framings.

These are drafts. They are **not** part of the wallet-transfer service
itself — they're a knowledge-extraction artefact for republication on
LinkedIn / Medium / personal blog, based on the patterns the repo
already demonstrates.

---

## Index

| # | Topic | LinkedIn | Medium |
|---|---|---|---|
| 1 | Pessimistic locking + `orderedPair`: deadlock-free `FOR UPDATE` transfers | [linkedin/01-ordered-pair-locking.md](linkedin/01-ordered-pair-locking.md) | [medium/01-ordered-pair-locking.md](medium/01-ordered-pair-locking.md) |
| 2 | Idempotency keys as primary keys: the race-loss replay pattern | [linkedin/02-idempotency-as-pk.md](linkedin/02-idempotency-as-pk.md) | [medium/02-idempotency-as-pk.md](medium/02-idempotency-as-pk.md) |
| 3 | Double-entry ledger as a schema constraint | [linkedin/03-ledger-as-schema.md](linkedin/03-ledger-as-schema.md) | [medium/03-ledger-as-schema.md](medium/03-ledger-as-schema.md) |
| 4 | The FAILED state must commit: insufficient-funds is not a rollback | [linkedin/04-failed-must-commit.md](linkedin/04-failed-must-commit.md) | [medium/04-failed-must-commit.md](medium/04-failed-must-commit.md) |
| 5 | Money is `int64` minor units: every place `float64` will bite you | [linkedin/05-int64-money.md](linkedin/05-int64-money.md) | [medium/05-int64-money.md](medium/05-int64-money.md) |
| 6 | Why we kept stdlib `net/http` + `database/sql` instead of a framework | [linkedin/06-stdlib-over-framework.md](linkedin/06-stdlib-over-framework.md) | [medium/06-stdlib-over-framework.md](medium/06-stdlib-over-framework.md) |
| 7 | Replacing testcontainers with a `DATABASE_URL` contract | [linkedin/07-testcontainers-to-dsn.md](linkedin/07-testcontainers-to-dsn.md) | [medium/07-testcontainers-to-dsn.md](medium/07-testcontainers-to-dsn.md) |
| 8 | Concurrency safety as an invariant test, not a benchmark | [linkedin/08-invariants-over-benchmarks.md](linkedin/08-invariants-over-benchmarks.md) | [medium/08-invariants-over-benchmarks.md](medium/08-invariants-over-benchmarks.md) |
| 9 | From `make bench` to k6 to pprof: a perf-question decision tree | [linkedin/09-perf-decision-tree.md](linkedin/09-perf-decision-tree.md) | [medium/09-perf-decision-tree.md](medium/09-perf-decision-tree.md) |
| 10 | AGENTS.md as vendor-neutral instruction protocol | [linkedin/10-agents-md-protocol.md](linkedin/10-agents-md-protocol.md) | [medium/10-agents-md-protocol.md](medium/10-agents-md-protocol.md) |
| 11 | Project-scoped Skills: useful triggers vs noisy ones | [linkedin/11-project-skills.md](linkedin/11-project-skills.md) | [medium/11-project-skills.md](medium/11-project-skills.md) |
| 12 | The two-question CVE test: govulncheck beats Dependabot version-match | [linkedin/12-govulncheck-two-question.md](linkedin/12-govulncheck-two-question.md) | [medium/12-govulncheck-two-question.md](medium/12-govulncheck-two-question.md) |
| 13 | The Go floor cascade: one `go get` rewrites your minimum version | [linkedin/13-go-floor-cascade.md](linkedin/13-go-floor-cascade.md) | [medium/13-go-floor-cascade.md](medium/13-go-floor-cascade.md) |
| 14 | Authoring the AI transcript for your assignment review | [linkedin/14-ai-transcript.md](linkedin/14-ai-transcript.md) | [medium/14-ai-transcript.md](medium/14-ai-transcript.md) |
| 15 | Mermaid diagrams: 4 traps GitHub enforces that `mmdc` doesn't | [linkedin/15-mermaid-github-traps.md](linkedin/15-mermaid-github-traps.md) | [medium/15-mermaid-github-traps.md](medium/15-mermaid-github-traps.md) |

---

## Recommended publishing order

For a quarter-long sequence:

1. **Week 1:** #12 (govulncheck two-question test) — broadest audience, leads with controversy
2. **Week 2:** #13 (Go floor cascade) — references #12, deepens the dep-management thread
3. **Week 3:** #2 (idempotency as PK) — pivots to engineering content
4. **Week 4:** #1 (`orderedPair` locking) — pairs naturally with #2
5. **Month 2:** #6, #7, #8 — perf/testing trilogy
6. **Month 3:** #10, #11, #14 — agent-workflow trilogy
7. **Filler:** #3, #4, #5, #9, #15 — short, evergreen, repost-friendly

The top two (#12, #13) carry the most signal-per-word and the most
reusable lesson; lead with those.

---

## Platform conventions used

**LinkedIn variants:**
- Hook in the first 2-3 lines (the "see more" cutoff is around line 3
  on mobile).
- 800-1500 words.
- Opinionated, lesson-first framing.
- No syntax highlighting — code shown sparingly, mostly inline.
- CTA = a question that invites comments.
- Emoji discipline: none.

**Medium variants:**
- 1500-3000 words.
- More technical depth: full code blocks, real numbers, diagrams.
- Multi-part-friendly structure (some have explicit "Part 1 of N" links).
- CTA = follow + link to the repo for the next in series.
- Suitable for publication submission (Better Programming, Level Up
  Coding, ITNEXT, etc.).

---

## Source material

Every article cross-references material that already lives in the repo:

- `internal/` source code (with the doc comments added in commit `0c1b3d8`)
- `migrations/0001_init.up.sql` (the schema constraints)
- `README.md` Design choices section
- `AGENTS.md` §1 security rules + §13 skill catalog
- `.claude/skills/*/SKILL.md` (8 project-scoped skills)
- `SECURITY.md` (the vuln-triage outcomes)
- `loadtest/transfer.js` + the benchmark / stress test files

Articles can quote from those files directly — the comments and doc
prose are designed to be readable independent of the code.
