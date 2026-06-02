A skill that fires on every commit is noise.

A skill that fires only when it can actually help is leverage.

The difference is the description field.

---

Most teams I have talked to who use project-scoped agent capabilities (Claude Code calls them "Skills", other tools have analogous concepts) end up in one of two places. Either the skills sit unused because nobody remembers they exist, or the skills auto-load constantly and fill the context with templates that have nothing to do with the change being made. Both failures are noise.

The fix is not in the skill body. It is in the trigger description.

Quick framing first. A skill is auto-loaded context — when the agent decides this skill applies to the work at hand, the entire body of the skill loads into the model's context as instructions. That makes the skill body free if it fires correctly and a constant tax if it fires wrongly. Slash commands are different — those run only on explicit invocation. Skills are the always-on background help; commands are the on-demand tool.

The trigger-discipline pattern that worked for us has two halves in every description:

- **DO trigger when** — a list of file paths, user phrasings, and concrete situations where the skill would actually help.
- **Do NOT trigger when** — the closest neighbours that would be tempting to match but where the skill has nothing useful to add.

The DO half teaches the agent when to load. The NOT half is what prevents the skill from polluting unrelated changes.

A made-up bad example. A skill called "go-style" with the description "Use this skill when working on Go code". This loads on every commit, including pure README edits if anyone happens to mention Go in the prose. It teaches the agent nothing about *when* the skill is actually useful. The body of the skill might be excellent, but the trigger is noise.

A real example from our wallet-transfer repo, for the skill that owns SQL safety:

  Use this skill any time work in this wallet-transfer repo involves
  SQL queries, opening or extending a database transaction, locking
  wallet rows, or modifying balance/idempotency logic. Trigger
  especially when editing files under internal/repository/,
  internal/service/, or migrations/, or when the user asks to "add an
  endpoint that touches a wallet", "update balance", "add a query",
  "fix a race", or anything involving concurrency on the
  wallets/transfers/ledger_entries/idempotency_records tables. Do NOT
  trigger for pure handler/JSON work that does not reach the database,
  for domain-only changes, or for documentation-only edits.

Note what the description does NOT do. It does not say "use this skill when working on the wallet repo." It does not say "use this when SQL might be involved." It pins to specific directories, specific phrasings, and specific tables — and then explicitly excludes handler-only work, domain-only work, and documentation edits.

That precision is what makes the skill cheap. A handler refactor that does not touch SQL does not load the skill. A README edit does not load the skill. A test that only exercises domain types (no DB) does not load the skill. So when the skill *does* load, it is because the agent is about to write SQL — and now it has the parameterization templates, the lock-order helper, the idempotency pattern, the SQLSTATE table, and the anti-pattern matrix at its fingertips.

Our wallet repo has eight skills. Each one follows the same DO/NOT pattern:

- `postgres-tx-safety` — SQL and transactions. Doesn't fire on handler-only work.
- `wallet-migration` — schema DDL. Doesn't fire on DML (which is `postgres-tx-safety`'s job).
- `wallet-integration-test` — tests that hit a real Postgres. Doesn't fire on domain unit tests.
- `wallet-observability` — Prometheus and OpenTelemetry. Doesn't fire on log-line edits.
- `wallet-vuln-triage` — new Dependabot or govulncheck alert. Doesn't fire on routine dep bumps.
- `wallet-deps` — adding or upgrading a module. Doesn't fire on app-code-only edits.
- `wallet-ci-workflow` — `.github/workflows/*.yml`. Doesn't fire on Make target edits.
- `wallet-perf` — `make bench`, `make load`, pprof, stress. Doesn't fire on production code that doesn't touch perf tooling.

One trigger context per skill, one concern per skill. If two skills overlap on triggers, one of them is in the wrong scope. If a skill matches more than its body covers, the body is in the wrong place — that material belongs in AGENTS.md so every change benefits, not in a skill where only matching changes benefit.

Cross-references matter. We don't repeat security rules from AGENTS.md in every skill. We link to them. The skill says "Read AGENTS.md §S-3 first. These templates are the operational version of that rule." This keeps the skills small, prevents drift, and makes AGENTS.md the canonical source for cross-cutting rules.

There is a governance angle too. Skills are versioned with the codebase. They go through the same PR review as application code. When a skill becomes obsolete, mark it deprecated in its description for one release cycle before removing the directory — so anyone reading recent git history sees the deprecation rather than a silent disappearance.

The mental model that helped us: a skill is a runbook with a trigger. The trigger is the part most teams ignore and it is where most of the value lives.

What's the smallest action that should trigger an agent assist for your codebase?
