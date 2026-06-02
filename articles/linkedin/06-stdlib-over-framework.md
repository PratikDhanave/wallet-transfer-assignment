We built a financial-grade Go service with zero web framework, zero ORM, and zero generated code.

Reviewers asked why. The honest answer is in the diff.

---

Four design choices, all in the same "pick the boring option" direction:

1. Stdlib `net/http` with Go 1.22's method-aware `ServeMux` for routing. No chi, no gin, no echo.
2. `database/sql` with the pgx driver. No GORM, no ent, no sqlc.
3. One Postgres transaction per request, with explicit `SELECT ... FOR UPDATE` on wallets in lexicographic id order. No saga, no two-phase commit.
4. Integration tests against a real `postgres:16-alpine`. No sqlmock, no in-memory fake.

Each one feels heretical individually. Together they form a coherent thesis: in a small bounded domain, framework cost is greater than framework benefit.

What we gained, choice by choice:

**Stdlib net/http.** Every route declaration is one line and every middleware composition is a function call. No framework upgrade cycle. No "did chi change the way Param works again." Anything a reviewer knows about `net/http` transfers directly — there is no second layer to learn. The router is `mux.HandleFunc("POST /transfers", h.Create)` and reviewers can audit it without opening docs.

**database/sql + pgx.** Every SQL query is visible at the call site. Reviewing a `FOR UPDATE` lock means reading the literal string in the file, not chasing through three ORM helper layers to figure out whether the framework decided to use a row lock or an advisory lock or no lock. The parameterised query is `$1, $2, $3` and the driver does the binding. Nothing is hidden.

**Single transaction per request.** All-or-nothing atomicity is free when the work fits in one tx. A crash mid-transfer is implicitly recovered by Postgres rolling back. We don't have a saga coordinator because we don't have multiple systems to coordinate. The mental model is "the function returns nil, the tx commits; the function returns an error, the tx rolls back." That's the whole story.

**Real Postgres tests.** sqlmock can verify that you called `db.Query` with a certain string. It cannot verify that your `FOR UPDATE` actually serialises concurrent debits. It cannot verify that your `CHECK (balance >= 0)` constraint fires on a negative-balance write. It cannot verify that your unique-violation error code is `23505`. A real Postgres can. Our integration suite runs 146 tests at 88.1% coverage against a real database and catches the kind of bug that sqlmock politely ignores.

What we gave up:

**Boilerplate.** More lines of scanning code. More lines wiring up middleware. No auto-generated OpenAPI. No "register this struct and the framework figures out the JSON binding." If you have 50 CRUD endpoints, this hurts. We have six endpoints; it doesn't.

**Convenience features.** No automatic request-id propagation (we write 20 lines for it ourselves). No structured access logging out of the box (we write 30 more). No automatic request validation (we write the validation explicitly). Each of those is a feature we now own. We can also audit, change, and remove any of them without filing a framework issue.

The thesis: in a small bounded domain, the cost of the framework — version churn, hidden behaviour, learning curve, leaky abstractions over the parts that matter most (transactions, locks, error codes) — exceeds the cost of writing the 200 lines the framework would have saved you.

Numbers from the actual repo:

- 146 unit/integration tests
- 88.1% test coverage
- 297 RPS sustained under k6 load (single local Postgres)
- p50 23ms / p95 83ms / p99 298ms
- 41 transitive dependencies in `go.mod` (after we deleted testcontainers, which is its own post)

The dependency count is the part reviewers usually find startling. A typical Go service that reaches for chi + GORM + a structured logger + a config loader runs 80–150 transitive deps. Ours runs 41 because we kept reaching for `net/http`, `database/sql`, `log/slog`, and `os.Getenv` instead.

The boring choices compound. Once you commit to stdlib http, you can't paper over a database concurrency bug with a framework annotation, so you have to actually solve it with `FOR UPDATE` and lock ordering. Once you commit to `database/sql`, you can't hide a query-builder bug behind a higher-level helper, so the query is right there in the file. Once you commit to real-DB tests, you can't pretend the schema constraints are decorative.

The flip side: if you have 80 endpoints and a CRUD-heavy domain and a team that already knows your framework cold, the math goes the other way. Use the framework. The thesis is not "frameworks are bad." The thesis is "this size domain pays a framework tax that doesn't earn back its cost."

What's the smallest service where you'd still reach for Gin, Echo, or Fiber?
