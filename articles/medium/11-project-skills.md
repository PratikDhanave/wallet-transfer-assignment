# Project-Scoped Skills: The Difference Between Useful and Noisy Triggers

This is a meta-post about the 8 project-scoped skills we wrote for one repo, what each triggers on, and the discipline that keeps them from stepping on each other.

The repo is a Go financial service that moves money between wallets — PostgreSQL 16, stdlib `net/http`, integration tests against a real database, govulncheck in CI, strict layered architecture. Public at [github.com/PratikDhanave/wallet-transfer-assignment](https://github.com/PratikDhanave/wallet-transfer-assignment). Eight Skills live under `.claude/skills/`. Each one was hard-won, in the sense that the first draft was wrong and we found out by watching it fire on the wrong PRs.

This post is about what we learned, not what we wrote down. The wrong-fire log was more useful than the right-fire log.

## Skill anatomy

A Skill is a Markdown file with YAML frontmatter at the top. The agent (Claude Code in our case, but the concept is portable) reads the frontmatter to decide *whether* to load the skill; the body becomes loaded context if it decides yes.

```yaml
---
name: postgres-tx-safety
description: Use this skill any time work in this wallet-transfer repo
  involves SQL queries, opening or extending a database transaction,
  locking wallet rows, or modifying balance/idempotency logic. ...
---
```

The body is the runbook. Templates, anti-patterns, verification commands.

So a Skill has two surfaces:

1. **The description (frontmatter).** Decides when the skill loads. This is the trigger.
2. **The body (Markdown).** Decides what happens once it loads. This is the runbook.

The first surface is where almost all the value (and almost all the pain) lives.

## The description as the trigger: DO + NOT-DO sentences

The pattern that worked, after rewriting every description at least twice:

```
Use this skill when <concrete-situation-1>, <concrete-situation-2>,
or <concrete-situation-3>. Trigger especially when editing files
under <directory> or when the user asks to "<phrase-1>",
"<phrase-2>", or "<phrase-3>". Do NOT trigger for <near-neighbour-1>,
for <near-neighbour-2>, or for documentation-only edits.
```

Two halves matter:

- **DO sentences** — file paths, user phrasings, code situations where the skill genuinely helps.
- **NOT sentences** — the closest plausible triggers that would tempt the agent to load the skill when its body has nothing to offer.

A description with only the DO half overfires. A description with no NOT half has no calibration data to tell the agent where the boundary is.

Worked example from `postgres-tx-safety`:

> Use this skill any time work in this wallet-transfer repo involves SQL queries, opening or extending a database transaction, locking wallet rows, or modifying balance/idempotency logic. Trigger especially when editing files under `internal/repository/`, `internal/service/`, or `migrations/`, or when the user asks to "add an endpoint that touches a wallet", "update balance", "add a query", "fix a race", or anything involving concurrency on the `wallets/transfers/ledger_entries/idempotency_records` tables. Do NOT trigger for pure handler/JSON work that does not reach the database, for domain-only changes, or for documentation-only edits.

Three categories of trigger:
- **Directory paths** (`internal/repository/`, `internal/service/`, `migrations/`).
- **User phrasings** ("add an endpoint that touches a wallet", "fix a race").
- **Concrete tables** (`wallets`, `transfers`, `ledger_entries`, `idempotency_records`).

Three categories of anti-trigger:
- Pure handler / JSON work that does not reach the DB.
- Domain-only changes.
- Documentation-only edits.

Why the negatives matter: a handler refactor is the easy case to wrongly match. The file is a Go file. The PR mentions transfers. But if no SQL is involved, loading `postgres-tx-safety` is pure noise — its body is parameterization templates and lock-order patterns that have nothing to say about JSON encoding.

## The body as the runbook

Once the skill fires, the body becomes loaded context. It needs to be three things:

1. **Templates the agent can copy directly.** Not "here is how transactions work in general" — specifically "here is `RunInTx` with `orderedPair` and a checklist."
2. **Anti-patterns the agent should auto-reject.** Listed in a table with the reason and the fix.
3. **Verification steps the agent should run before declaring done.** Specific `go test` / `golangci-lint` / `govulncheck` invocations.

Excerpt from `postgres-tx-safety`'s body — the lock-and-mutate template:

```go
err := s.txm.RunInTx(ctx, func(exec repository.Executor) error {
    firstID, secondID := orderedPair(srcID, dstID)
    if _, err := s.wallets.LockForUpdate(ctx, exec, firstID); err != nil {
        return err
    }
    if _, err := s.wallets.LockForUpdate(ctx, exec, secondID); err != nil {
        return err
    }
    src, err := s.wallets.Get(ctx, exec, srcID)
    if err != nil { return err }
    dst, err := s.wallets.Get(ctx, exec, dstID)
    if err != nil { return err }

    // 1. validate invariants (e.g. src.Balance >= amount)
    // 2. mutate (balance updates + ledger entries + state writes)
    return nil
})
```

And the matching anti-pattern table:

| Pattern | Reason | Fix |
|---|---|---|
| `db.Exec("UPDATE … WHERE id='" + id + "'")` | SQL injection | parameterize with `$N` |
| `SELECT balance FROM wallets` then UPDATE outside tx | TOCTOU race | inside `RunInTx`, `FOR UPDATE` |
| Locking source then destination by *direction* | Deadlock under counter-transfers | `orderedPair` |
| New money-moving endpoint without `IdempotencyKey` | Duplicate-charge on retry | Template C |
| `float64` for amount, fee, balance, anything monetary | Precision loss | `int64` minor units |

The anti-pattern table is the highest-leverage part of the body. It lets the agent reject obvious mistakes without reasoning from first principles.

## The 8 skills, walked through

### `postgres-tx-safety`

- **Loads when:** SQL queries, transactions, locking, balance / idempotency logic. Files under `internal/repository/`, `internal/service/`, `migrations/`.
- **Doesn't load when:** Handler-only work, domain-only changes, docs.
- **Provides:** Parameterised-SQL templates, `RunInTx` + lock-order patterns, idempotency template, SQLSTATE map (23505, 23503, 23514, 40001, 40P01), anti-pattern table.
- **Anchor rule it operationalises:** AGENTS.md §1 (security) and §3 (database conventions).

### `wallet-migration`

- **Loads when:** Schema DDL — adding or altering tables, columns, indexes, CHECK / UNIQUE / FK constraints.
- **Doesn't load when:** App-code-only changes, DML (which is `postgres-tx-safety`'s job).
- **Provides:** 6-step authoring workflow (pick the next number, write up, write down, copy to the embedded directory, diff for byte-equality, apply locally), patterns for the common DDL operations, house rules (`BIGINT` for money, `TIMESTAMPTZ` for time, every CHECK that the app validates).
- **Anchor rule it operationalises:** AGENTS.md §S-12.

Why this is separate from `postgres-tx-safety`: schema DDL and runtime SQL are different concerns. A migration that adds a column does not need the `RunInTx` template. A query that updates a balance does not need the dual-copy `migrations/` + `internal/db/migrations/` sync. Splitting them keeps each body short and each trigger crisp.

### `wallet-integration-test`

- **Loads when:** Writing or modifying integration tests in `internal/service/` or `internal/handler/`.
- **Doesn't load when:** Pure unit tests on `internal/domain/` (no DB), docs.
- **Provides:** `testdb.Get` / `testdb.Reset` patterns, service-level test scaffolding, HTTP handler test scaffolding, concurrency test template (the canonical 20-goroutine pattern that asserts no negative balance + ledger conservation + correct number of processed transfers), same-key collapsing template, paginated-list cursor-walk template.
- **Anchor rule it operationalises:** AGENTS.md §6.

The concurrency-test template is the single most-referenced part of the body. It is also the part that, when missing, lets a real race ship to production.

### `wallet-observability`

- **Loads when:** Prometheus metrics or OpenTelemetry tracing. Files under `internal/metrics/` or `internal/tracing/`. User asks for "add a metric", "add a span", "instrument X".
- **Doesn't load when:** Pure business-logic changes that don't touch metrics/tracing packages, log-line edits (those use slog and are covered by AGENTS.md §S-6).
- **Provides:** Counter / histogram / span templates, cardinality discipline (label by route *pattern*, not raw URL — wallet IDs are span attributes, not metric labels), the admin-listener-only rule for `/metrics`, OTLP exporter gating.
- **Anchor rule it operationalises:** AGENTS.md §S-6 and §S-16.

The cardinality discipline is the part most often violated by well-meaning agents. A metric label like `wallet_id` looks reasonable until you realise every unique wallet creates a separate time series and the Prometheus storage explodes. The body has both the rule and a test template that catches the regression.

### `wallet-vuln-triage`

- **Loads when:** New Dependabot or govulncheck alert. User asks to triage a CVE, decide whether to bump a dep, or write a dismiss-with-justification note.
- **Doesn't load when:** Routine dep bumps unrelated to a CVE.
- **Provides:** The two-question test (is the vulnerable function in our import set? does our code reach it?), `govulncheck -mode=source ./...` workflow, per-CVE worked examples, SECURITY.md note template.

This skill exists because Dependabot's signal is noisy (version match, not call-graph match) and the right answer is usually "unreachable, dismiss with justification" rather than "bump and risk a Go-version-floor change." Without the runbook, every alert becomes a fresh investigation.

### `wallet-deps`

- **Loads when:** Adding or upgrading a module dependency.
- **Doesn't load when:** App-code-only edits that do not touch `go.mod`.
- **Provides:** Vetting checklist (stdlib first, release cadence, transitive surface), Go-floor pinning rules (we pin to `go 1.24.0` because bumping pgx/v5 ≥ 5.9.0, OpenTelemetry ≥ v1.42.0, or testcontainers-go ≥ v0.41.0 would force the floor up to 1.25), transitive surface review.
- **Anchor rule it operationalises:** AGENTS.md §S-11.

### `wallet-ci-workflow`

- **Loads when:** Editing `.github/workflows/*.yml`. Adding a job, changing a service container, adjusting branch-protection-required checks.
- **Doesn't load when:** Make target edits, README edits about CI.
- **Provides:** Required-check policy, postgres service container template, action-pinning rules (pin third-party actions to a commit SHA, not a tag).

### `wallet-perf`

- **Loads when:** Working on `make bench`, `make load`, pprof, stress tests. User asks "is this fast enough", "where's the bottleneck", "what's our p99".
- **Doesn't load when:** Production code that doesn't touch perf tooling.
- **Provides:** Tool decision tree (benchmarks → k6 → pprof → stress), baseline maintenance (numbers in the README so drift is visible at review time), `b.ResetTimer()` discipline, k6 `constant-arrival-rate` rationale.

## Governance: one trigger context, one concern

Three rules we settled on:

1. **One trigger context per skill.** If two skills load on the same files, one of them is too broad. We learned this when an early version of `wallet-migration` and `postgres-tx-safety` both fired on every migration file — both bodies were partially relevant, neither was fully right, and the agent had to decide which template to use. We split them by DDL-vs-DML.

2. **One concern per skill.** A skill that owns both "how to write a migration" and "how to test a migration" is two skills wearing one hat. Split. The first is `wallet-migration`. The second falls under `wallet-integration-test`.

3. **Cross-cutting concerns belong in AGENTS.md, not in a skill.** A security rule that applies to every change (S-1 "parameterize every query", S-2 "money is `int64`") goes in AGENTS.md so it loads on every PR. A pattern that applies only when you're touching SQL goes in `postgres-tx-safety`. The split is "always relevant" vs "sometimes relevant."

## The skill catalog table in AGENTS.md

The last governance lever: `AGENTS.md` carries a table of every skill in the repo. Three columns: skill name, when it loads, what it provides. This serves two purposes:

- An agent that does not support skills can still read the table, see when each one would have loaded, and read the underlying `SKILL.md` by hand. Vendor-neutral.
- A human reviewer can spot overlap. If two rows say "loads when editing migrations," that is the signal to split or merge.

Excerpt:

| Skill | Loads when… | What it provides |
|---|---|---|
| `postgres-tx-safety` | Editing SQL, transactions, locking, or balance logic | Parameterised-SQL templates, RunInTx + lock-order patterns, idempotency template, SQLSTATE map |
| `wallet-migration` | Adding or altering tables, columns, indexes, CHECK / UNIQUE / FK constraints | 6-step authoring workflow, dual-copy sync, common DDL patterns |
| `wallet-integration-test` | Writing or modifying integration tests | DATABASE_URL-backed test patterns, concurrency / same-key collapsing templates |
| `wallet-observability` | Touching Prometheus metrics or OpenTelemetry tracing | Counter / histogram / span templates, cardinality discipline |

(Five more rows in the actual table.)

## Naming

A small thing that matters: action-oriented names. `wallet-migration` says what it's for. `db-stuff` would not. The convention is `<scope>-<action>` or `<scope>-<concern>`:

- `postgres-tx-safety` — Postgres, transaction safety.
- `wallet-migration` — wallet domain, migrations.
- `wallet-integration-test` — wallet domain, integration tests.
- `wallet-observability` — wallet domain, observability.

When a skill becomes obsolete, mark it deprecated in its description for one release cycle before deleting the directory. Anyone reading recent git history then sees the deprecation rather than a silent disappearance.

## The takeaway

The hard part of a project-scoped skill is the trigger, not the body. The trigger discipline is:

- Write DO sentences that pin to directories, phrasings, and concrete table or file names.
- Write NOT sentences that explicitly exclude the closest tempting neighbours.
- One trigger context, one concern.
- Cross-cutting rules live in AGENTS.md, not in skills.
- Maintain a catalog table in AGENTS.md so overlap is visible.

Get the trigger right and the body is a free runbook that loads exactly when it's useful. Get it wrong and the skill is noise that takes context away from the real work.

---

The repository is at [github.com/PratikDhanave/wallet-transfer-assignment](https://github.com/PratikDhanave/wallet-transfer-assignment). Follow for the next post in the series.
