# `AGENTS.md` as a Vendor-Neutral Instruction Protocol for AI Coding Assistants

There's a quiet de facto standard forming for agent instructions: a single `AGENTS.md` at the repo root that every major coding assistant reads. This post is what we put in ours and why.

The repository is a Go financial service that moves money between wallets — PostgreSQL 16, stdlib `net/http`, `database/sql` + pgx, no framework, no ORM. Strict layered architecture, integration tests against a real database, govulncheck in CI. Public at [github.com/PratikDhanave/wallet-transfer-assignment](https://github.com/PratikDhanave/wallet-transfer-assignment).

This is a meta-post about the file at the root of that repository. Specifically: how we got from "four tool-specific instruction files" to "one shared `AGENTS.md` and a few one-line stubs," what we put in the consolidated file, and what we deliberately left out.

## The lock-in problem

Run an audit of any sufficiently active developer's repo and you find a thicket of agent-specific files:

```
CLAUDE.md           # Claude Code
.cursorrules        # Cursor (legacy)
.cursor/rules/*     # Cursor (new)
CONVENTIONS.md      # Aider
.continue/...       # Continue
AGENT.md            # OpenCode
```

Each one is a few hundred to a few thousand lines. Each one describes the same security rules, the same conventions, the same "don't do this" patterns — in slightly different wording, because they were written at different times by different people copying from different sources.

The bug is structural. When the wallet codebase added a 17th security rule, which file got the update? In the best case, all of them; in the worst case, one. The mismatch becomes the next regression — an agent armed with the stale file makes a "correct" change that violates the new rule.

The fix is the same fix you would apply to any duplicated state: pick one source of truth.

## `AGENTS.md` as the emerging convention

`AGENTS.md` at the repo root has been getting adoption across coding-assistant ecosystems as the shared instruction file. Some tools read it natively. Others read their own file and you redirect them with a stub:

```md
# CLAUDE.md
Project instructions live in AGENTS.md so that any agent
(Claude Code, Cursor, Aider, Continue, OpenCode, ...) picks them up.
Claude Code follows that file as if it were this one.
```

That is the entire `CLAUDE.md` in this repository. Three lines. Every actual instruction lives in `AGENTS.md`. Adding a new tool to the stack means adding a new stub, not authoring a parallel instruction file.

## What we put in `AGENTS.md` — a walkthrough

Our `AGENTS.md` is structured as 13 numbered sections plus appendices. Below is the section-by-section breakdown with the reasoning. The full file is in the repository; this is the table of contents and the *why* for each block.

### §0 — Project context (read first)

The minimum a fresh agent needs to make any decision at all:

- Language / runtime: Go 1.24+ (the floor declared in `go.mod`).
- HTTP: stdlib `net/http` with Go 1.22 method-aware `ServeMux`. No web framework.
- Database: PostgreSQL 16 via `database/sql` + the `pgx` driver. No ORM.
- Schema migrations: `golang-migrate`, SQL files embedded via `go:embed`. Forward-only.
- Tests: integration tests gated by `//go:build integration`, real Postgres via `DATABASE_URL`.
- Architecture: strict layering, dependencies point inward (`handler → service → repository → DB`).

This section is short on purpose. Anything that needs three paragraphs of context belongs in `README.md`. `AGENTS.md` answers "what is this codebase made of" in fifteen seconds.

### §1 — Security, 16 numbered rules

The largest single section by volume. Each rule has a number (S-1 through S-16), a one-line summary, a short rationale, and where useful a correct-vs-wrong code example. The numbering matters: in code review, "S-9" is faster than "the idempotency rule" and unambiguous in a way that prose is not.

A sampling:

> **S-1. SQL must be parameterized — always**
>
> ```go
> // correct
> exec.ExecContext(ctx, `UPDATE wallets SET balance = $1 WHERE id = $2`, balance, id)
>
> // NEVER
> exec.ExecContext(ctx, fmt.Sprintf(`UPDATE wallets SET balance = %d WHERE id = '%s'`, balance, id))
> ```

> **S-3. Mutations live inside a transaction**
>
> ```go
> err := txm.RunInTx(ctx, func(exec repository.Executor) error {
>     // 1. SELECT ... FOR UPDATE on every row whose state will depend on a read
>     // 2. read state
>     // 3. validate invariants
>     // 4. mutate
>     return nil
> })
> ```

> **S-9. Idempotency is mandatory on any money-moving endpoint**
>
> Every new endpoint that creates a domain object whose existence has a financial effect MUST require an `idempotencyKey`, use the same "insert into a UNIQUE-keyed table inside the tx" pattern as `internal/service/transfer.go`, return the same response for replays, and return `409 Conflict` for same-key-different-body.

A reasonable question: why so many? Because the cost of "the agent forgot S-9 on a new endpoint" is a duplicate-charge incident. Spelling it out once, in one file, with a number, is cheap insurance.

### §2 — Architecture invariants

Strict layering, codified:

```
handler → service → repository → DB
              ↓
           domain  (no deps on the other layers)
```

A-1: handler does no business logic. A-2: service is the only layer that opens transactions. A-3: repository is SQL only. A-4: domain has zero external imports. A-5: errors flow up as sentinels, matched with `errors.Is`.

These are the rules that make the security rules enforceable. S-3 ("mutations live inside a transaction") presumes there is a layer that owns transactions — A-2 names it.

### §3 — Database conventions

- Always parameterize (see S-1).
- Always `SELECT ... FOR UPDATE` when the row will be mutated based on the read.
- Always declare invariants at the schema level (`UNIQUE`, `CHECK`, `FOREIGN KEY`) in addition to the application check. Defence in depth.
- `BIGINT` for monetary amounts (see S-2).
- `TIMESTAMPTZ` for times, never `TIMESTAMP` (without TZ).
- New schema changes go in a new numbered migration file. Never edit a merged one.
- After updating SQL files in `migrations/`, copy them to `internal/db/migrations/` (the embedded copy) and verify both are identical.

The last bullet exists because our binary embeds migrations via `go:embed`, and the dual-copy problem (developer-facing source + embedded copy) is the single most common mistake in this part of the codebase.

### §§ 4–5 — Concurrency and idempotency

Two sections that repeat material from §1 (yes, with intent) because reviewers and agents searching for "concurrency rules" should not have to read all 16 security rules to find them. Duplication for findability is acceptable; conflicting duplication is not.

### §6 — Testing rules

- Unit tests live next to the code (`*_test.go`, no build tag).
- Integration tests are gated with `//go:build integration` and use `internal/testdb`, which connects to the Postgres pointed at by `DATABASE_URL`.
- New service / handler logic gets at least one integration test.
- Concurrency-sensitive code gets a multi-goroutine test that asserts the invariant under load.
- Race detector must be clean: `go test -race -tags=integration -count=1 ./...`.

### §7 — Code style

Two paragraphs worth quoting in full because they are unusual:

> **Commenting style is tutorial-grade.** A reviewer who has never opened this codebase should be able to read top-to-bottom and understand what each layer is doing and why. That means:
> - Every exported identifier has a doc comment.
> - Every non-trivial function body has step-labeled or block-explaining comments — they describe *intent*, not just restating the code.
> - Each test starts with a comment naming the scenario under verification and the invariant the assertions enforce.

> Comments must still earn their keep: avoid restating obvious names (`// constructor`), avoid TODO/HACK markers that have no owner, avoid commenting out code (delete it; git has the history).

The point of this section in `AGENTS.md`, as opposed to in a separate `STYLE.md`, is so the agent applies the same style to the code it writes. Without this section, agents produce average code; with it, they produce code that matches the rest of the file.

### §8 — Commit and PR conventions

Imperative subjects ≤ 72 chars. Body explains *why*. Never `--no-verify`. Never `--force` to `main`. PR template populated, including the AI disclosure.

### §9 — What NOT to do (anti-pattern table)

A 13-row table:

| Anti-pattern | Why banned |
|---|---|
| `fmt.Sprintf("SELECT … WHERE id = '%s'", id)` | SQL injection (S-1) |
| `float64` for `amount` | Precision loss (S-2) |
| `SELECT balance` then `UPDATE balance` outside a tx | TOCTOU race (S-3) |
| Skipping `idempotencyKey` on a money-moving endpoint | Duplicate charges (S-9) |
| Editing a merged migration file | Out-of-sync schemas (S-12) |
| `git commit --no-verify` | Hooks exist for a reason |
| `git push --force` to main | Rewrites history, drops other people's work |
| Adding an ORM | We deliberately don't use one — propose in a separate discussion first |
| Returning a raw DB error to the client | Information leak (S-7) |
| Logging full request body | Secret leak (S-6) |
| `context.Background()` inside request-scoped code | Loses cancellation / deadlines |
| Holding a tx open across `http.Client.Do` | Connection-pool exhaustion + lock-pinning |

Anti-pattern tables turn out to be the single most-quoted section in PR comments. "Row 7 of the table in §9" is shorter than the rule itself.

### §10 — "When asked to add a new endpoint"

A default 8-step checklist: domain → migration → repository → service → handler → errors → tests → docs. Step 4 (service) is where the transaction boundary, business rules, and idempotency live. Step 7 (tests) is non-negotiable.

This section makes "add an endpoint that does X" a much shorter conversation. The agent already knows the build order; the only question is the business logic.

### §11 — "When to ask before acting"

Eight items the agent must stop and confirm before doing:

- Dropping a database table or column.
- Editing CI configuration or branch protection.
- Adding a new module dependency.
- Disabling a `CHECK` / `FOREIGN KEY` / `UNIQUE` constraint.
- Changing the idempotency or locking strategy.
- Touching anything that could affect money-flow correctness.
- Force-pushing, hard-resetting, or rewriting any history.
- Bypassing pre-commit hooks.

The principle: anything that would be expensive to undo silently goes on this list.

### §12 — Common commands

```sh
make db-up         # local Postgres on :5432
make db-down       # stop it
make run           # build + run server on :8080
make test          # unit tests
make test-int      # integration tests (Docker required)
make test-all      # both
make lint          # golangci-lint
make fmt-check     # gofmt verification
make tidy          # go mod tidy
```

This section is short on purpose. The agent should know what `make test` does without reading the `Makefile`.

### §13 — Skills catalog

A table of every project-scoped skill (Claude Code's auto-loaded context modules) with three columns: skill name, when it loads, what it provides:

| Skill | Loads when… | What it provides |
|---|---|---|
| `postgres-tx-safety` | Editing SQL, transactions, locking, or balance logic | Parameterised-SQL templates, RunInTx + lock-order patterns, idempotency template, SQLSTATE map |
| `wallet-migration` | Adding or altering tables, columns, indexes, constraints | 6-step authoring workflow, dual-copy sync, patterns for common DDL operations |
| `wallet-integration-test` | Writing or modifying integration tests | `DATABASE_URL`-backed test patterns, concurrency / same-key collapsing templates |
| `wallet-observability` | Touching Prometheus metrics or OpenTelemetry tracing | Counter / histogram / span templates, cardinality discipline, admin-listener rules |
| `wallet-vuln-triage` | New Dependabot / govulncheck alert | Two-question test, dismiss-with-justification template |
| `wallet-deps` | Adding or upgrading a module dependency | Vetting checklist, Go-floor pinning, transitive surface review |
| `wallet-ci-workflow` | Editing `.github/workflows/*.yml` | Required-check policy, postgres service container template |
| `wallet-perf` | Working on `make bench`, `make load`, pprof, or stress tests | Tool decision tree, baseline maintenance |

The point of this section is interoperability: an agent that does not support skills can still read the table, see when each one would have loaded, and read the underlying `SKILL.md` by hand to follow the same pattern.

## The stub pattern for vendor-specific files

For tools that demand their own file, write a stub:

```md
# CLAUDE.md
Project instructions live in AGENTS.md so that any agent
(Claude Code, Cursor, Aider, Continue, OpenCode, ...) picks them up.
Claude Code follows that file as if it were this one.
```

Three lines. The stub is allowed to be exactly as long as it takes to redirect; anything more substantive belongs in `AGENTS.md`.

If the tool genuinely needs vendor-specific configuration (e.g. an MCP server config, a per-tool keybinding), keep that in the tool-specific file. The line is: instruction = `AGENTS.md`; configuration = vendor file.

## Versioning policy

Two rules we found load-bearing after a few months:

1. **`AGENTS.md` is committed alongside the code that motivates it.** A new security rule lands in the same PR as the example that prompted it. A new entry in §9 (anti-pattern table) lands in the same PR as the lint rule or test that prevents it.
2. **Loosening `AGENTS.md` is reviewed as carefully as loosening code.** Tightening (adding a rule, narrowing an exception) is cheap. Loosening (removing a rule, broadening an allowed pattern) needs the same scrutiny as deleting a security check from a source file.

## Measuring whether it works

A few signals we watched for:

- **Are PR comments quoting rule numbers?** Yes. "Per S-9, please add idempotency." in code review is faster than re-explaining the rule and more enforceable.
- **Are agents producing code that matches the existing style without prompting?** Mostly. The tutorial-grade comment requirement in §7 is the one we still nudge most often, but the security rules and the layering rules are observed reliably.
- **Are we still maintaining tool-specific files?** Only the three-line stubs.
- **Is `AGENTS.md` becoming a kitchen sink?** Watch for this. At ~500 lines it is still navigable; past that you need to split into multiple files (and a master `AGENTS.md` that points to them).

## What does NOT belong in `AGENTS.md`

- **Tool-specific UI guidance.** Keybindings, menu paths, slash commands. Per-tool.
- **Long architectural rationale.** Belongs in `README.md` or `docs/`. `AGENTS.md` is operational, not pedagogical.
- **Personal preferences.** "I prefer named returns" is not a project rule. Project rules are things that, if violated, would break the system or fail review.
- **Anything that changes weekly.** Quickly-changing surface (which package owner handles which feature) belongs in `CODEOWNERS` or a routing doc.

## The takeaway

`AGENTS.md` is a small idea with a large payoff: one file, one source of truth, every agent reads it. The lock-in problem disappears. The duplicated-rules-with-subtle-mismatch bug disappears. The code-review vocabulary shrinks to numbered references that both humans and agents share.

If your repo has more than one tool-specific instruction file today, the consolidation is a one-afternoon job. The hard part is not the writing — it is deleting the duplicates afterwards.

---

The repository is at [github.com/PratikDhanave/wallet-transfer-assignment](https://github.com/PratikDhanave/wallet-transfer-assignment). Follow for the next post in the series.
