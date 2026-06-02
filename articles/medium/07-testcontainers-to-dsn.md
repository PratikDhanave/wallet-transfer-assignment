# Replacing Testcontainers with a `DATABASE_URL` Contract

This post is about a 130-line file that replaced 86 transitive dependencies and freed our Go version. Plus the one thing we got wrong on the first try.

The service is a Go wallet-transfer system that talks to PostgreSQL 16. Integration tests need a real database. The original setup used [testcontainers-go](https://golang.testcontainers.org) — a library that spins up a `postgres:16-alpine` container per test process, applies migrations, and tears it down at the end. Standard, popular, recommended.

We removed it. Here's why, what we replaced it with, and the trap we walked into on the first iteration.

---

## The Go floor cascade

The repo pinned `go 1.24.0` in `go.mod`:

```
module github.com/PratikDhanave/wallet-transfer-assignment

go 1.24.0
```

That line is a *minimum* version, not an exact one. The Go toolchain enforces it: any consumer of this module must run Go ≥ 1.24.

The catch is that Go's module graph also enforces transitive floors. If anything in your `go.sum` declares `go 1.25`, your effective floor is `go 1.25` — and `go mod tidy` will quietly rewrite your `go.mod` to match. There is no warning. The next person who runs the command sees a one-line diff in `go.mod` and may not connect it to a transitive dep.

In our case the chain was:

```
testcontainers-go
  └── docker/docker
        └── docker/cli
              └── grpc-gateway   (requires go 1.25)
```

`go mod tidy` saw `grpc-gateway` demanding 1.25 and bumped our floor. Our CI runs on a fixed Go version, which started failing once the floor was higher than the installed toolchain. We had two choices:

1. Bump CI to 1.25, accept the floor.
2. Remove the dep that's pulling 1.25.

Bumping the floor sounds harmless, but it's a one-way door. Anyone who needs to consume this module on an older Go version (because they're stuck on a particular distribution, a particular Docker base image, a particular cloud runtime) can't. We chose to remove the dep.

## What testcontainers was doing for us

The original code was about 20 lines and idiomatic:

```go
// before: testcontainers-based bootstrap (sketch)
ctx := context.Background()
container, err := postgres.RunContainer(ctx,
    testcontainers.WithImage("postgres:16-alpine"),
    postgres.WithDatabase("wallet"),
    postgres.WithUsername("wallet"),
    postgres.WithPassword("wallet"),
)
if err != nil {
    return nil, err
}
dsn, _ := container.ConnectionString(ctx)
db, _ := sql.Open("pgx", dsn)
// apply migrations, hand back db
```

Convenient. Every contributor running `go test ./...` got a fresh container. CI got the same container. No external setup required, no `DATABASE_URL` to remember.

The 86 transitive dependencies behind that convenience were the actual cost. Docker's client library is large, grpc-gateway is large, and the various platform shims (containerd, runc bindings, image registry helpers) accumulate. Most of those deps have their own CVE histories and their own version bump cadences.

## The replacement: 130 lines, one env var

Here is the entire replacement, with the original doc comments preserved:

```go
//go:build integration

// Package testdb provides a shared PostgreSQL connection for integration
// tests.
//
// The test process expects a running Postgres instance reachable via the
// `DATABASE_URL` env var (the same variable the server reads). If the
// variable is unset, every test that calls Get / DSN / Reset is skipped
// with a clear message, so the integration suite can live in the same
// `go test ./...` invocation without forcing every contributor to have
// Docker running.
package testdb

import (
    "context"
    "database/sql"
    "fmt"
    "os"
    "sync"
    "testing"
    "time"

    "github.com/PratikDhanave/wallet-transfer-assignment/internal/db"
)

const envDatabaseURL = "DATABASE_URL"

var (
    once      sync.Once
    sharedDB  *sql.DB
    sharedDSN string
    sharedErr error
)

func Get(tb testing.TB) *sql.DB {
    tb.Helper()
    once.Do(func() {
        sharedDB, sharedErr = start()
    })
    if sharedErr != nil {
        if sharedErr == errNoDSN {
            tb.Skipf("integration tests require %s (e.g. `make db-up && export %s=postgres://...`); skipping",
                envDatabaseURL, envDatabaseURL)
        }
        tb.Fatalf("open postgres at %s: %v", envDatabaseURL, sharedErr)
    }
    return sharedDB
}

func DSN(tb testing.TB) string {
    tb.Helper()
    _ = Get(tb)
    return sharedDSN
}

func Reset(tb testing.TB) {
    tb.Helper()
    d := Get(tb)
    _, err := d.ExecContext(context.Background(),
        `TRUNCATE idempotency_records, ledger_entries, transfers, wallets RESTART IDENTITY CASCADE`)
    if err != nil {
        tb.Fatalf("reset db: %v", err)
    }
}

var errNoDSN = fmt.Errorf("%s is not set", envDatabaseURL)

func start() (*sql.DB, error) {
    dsn := os.Getenv(envDatabaseURL)
    if dsn == "" {
        return nil, errNoDSN
    }
    sharedDSN = dsn

    d, err := db.OpenWithPool(dsn, db.PoolConfig{
        MaxOpenConns:    25,
        MaxIdleConns:    5,
        ConnMaxLifetime: 5 * time.Minute,
        ConnMaxIdleTime: 1 * time.Minute,
    })
    if err != nil {
        return nil, fmt.Errorf("open: %w", err)
    }
    if err := db.Migrate(d); err != nil {
        return nil, fmt.Errorf("migrate: %w", err)
    }
    return d, nil
}
```

Five things to notice.

**`//go:build integration`.** The whole file only compiles when the `integration` build tag is set. Plain `go test ./...` skips it. `go test -tags=integration ./...` includes it. Contributors who don't want to run integration tests don't see them.

**`sync.Once` for migrations.** The first test in the process pays the migration cost. Every later test gets the cached `*sql.DB`. Migrations are idempotent — golang-migrate's `Up()` returns `ErrNoChange` on subsequent calls, which our `db.Migrate` swallows — but doing them once per process is still measurably faster.

**Bounded pool.** Postgres 16's default `max_connections=100`. Without bounds on the test side, a 1,000-goroutine stress test would try to open 1,000 connections and most would fail. We cap at 25 to keep the test side well under the container's ceiling.

**Skip vs Fail.** If `DATABASE_URL` is unset, we *skip* (the test reports SKIP, not FAIL). If `DATABASE_URL` is set but the database is unreachable, we *fail* (the test reports FAIL). The distinction matters because a contributor who doesn't have Docker should still see green tests, but a CI run with a misconfigured DSN should see red.

**`TRUNCATE ... RESTART IDENTITY CASCADE`.** Reset is per-test. It truncates all four tables in one statement, resets the BIGSERIAL counter on `ledger_entries` so ids stay stable across runs, and cascades through foreign keys. We do not drop and recreate the schema between tests — that would re-run migrations, which is slow and pointless.

## The bootstrap, end-to-end

The local workflow:

```bash
make db-up                                # docker compose up -d postgres
export DATABASE_URL=postgres://wallet:wallet@localhost:5432/wallet?sslmode=disable
make test-int                             # or: go test -tags=integration ./...
```

The CI workflow (extract from `.github/workflows/ci.yml`):

```yaml
jobs:
  integration:
    runs-on: ubuntu-latest
    services:
      postgres:
        image: postgres:16-alpine
        env:
          POSTGRES_USER: wallet
          POSTGRES_PASSWORD: wallet
          POSTGRES_DB: wallet
        ports:
          - 5432:5432
        options: >-
          --health-cmd pg_isready
          --health-interval 10s
          --health-timeout 5s
          --health-retries 5
    steps:
      - uses: actions/checkout@v4
      - uses: actions/setup-go@v5
      - name: Integration tests
        env:
          DATABASE_URL: postgres://wallet:wallet@localhost:5432/wallet?sslmode=disable
        run: make test-int
```

GitHub Actions' `services.postgres` block runs a real `postgres:16-alpine` container alongside the job. The `DATABASE_URL` env var is injected into the step. Tests run against the real DB. No Go code knows or cares that it's running in CI.

The local docker-compose file is the same image:

```yaml
# compose.yml
services:
  postgres:
    image: postgres:16-alpine
    environment:
      POSTGRES_USER: wallet
      POSTGRES_PASSWORD: wallet
      POSTGRES_DB: wallet
    ports:
      - "5432:5432"
    healthcheck:
      test: ["CMD-SHELL", "pg_isready -U wallet"]
      interval: 5s
      timeout: 5s
      retries: 5
```

Same image, same credentials, same DSN shape. The test code can't tell the difference.

## The trap: `-p 1` for cross-package tests

The first version of this setup passed locally and failed in CI with a beautifully confusing error: a transfer assertion midway through one test would find the row missing. The transfer existed when it was created; ten lines later, when the assertion ran, it didn't.

The cause was Go's test parallelism. `go test ./...` compiles one binary per package and runs them concurrently by default. Two test binaries both pointed at the same `DATABASE_URL`. Test binary A creates a row. Test binary B starts a fresh test and calls `testdb.Reset(t)`, which truncates every table. Test binary A's row is now gone. Test binary A's next assertion fails.

The fix is `-p 1`:

```makefile
test-int:
    go test -tags=integration -p 1 -race ./...
```

`-p 1` forces serial execution of test packages. One package at a time, one set of mutations at a time, no cross-binary truncate races.

It's slower. On a 6-package integration suite, you pay roughly the cumulative wall time of each package instead of the maximum. The number is bounded by the slowest package, so it's not catastrophic, but it is real.

There are three other ways to solve the same problem:

1. **One DSN per test package.** Each package gets its own database (`wallet_transfer_package1`, `wallet_transfer_package2`, ...). Significantly more setup complexity, and CI needs to provision N databases or N containers.

2. **Per-test schemas.** Use Postgres schemas instead of tables; each test creates and drops its own schema. Adds noticeable per-test overhead (schema creation isn't free) and complicates migrations.

3. **`-parallel 1` inside packages plus `-p 1` across packages.** Doesn't solve the cross-binary issue alone, but combined with one DSN per package, it's the most expensive option that still avoids touching test code.

We picked `-p 1` because it's a one-line Makefile change with no test-code impact. The performance hit is acceptable for a suite that runs in under a minute.

## Tradeoffs: skip-if-unset vs fail-loud

The skip-if-unset behaviour is a choice. The alternative is to fail loudly when `DATABASE_URL` is unset, forcing every contributor to have Postgres running before they can run any tests.

Skip-if-unset means:

- A contributor running `go test ./...` for the first time sees green tests immediately.
- The unit suite (88.1% coverage on its own) is the gating layer; integration tests are additive.
- CI is the place where "integration tests must pass" is enforced. `services.postgres` is always available there, so the env var is always set, so the tests always run.

Fail-loud means:

- Every contributor needs Docker on their first day.
- The "tests pass" signal is stronger — there's no "passed because skipped."
- CI failures are easier to reproduce locally because the local setup matches CI by default.

We chose skip-if-unset because the wallet-transfer service is small enough that the unit suite gives high confidence on its own, and the friction of forcing Docker on every contributor outweighed the benefit of stronger local signal. Larger or more critical codebases might choose the other side.

## When *not* to do this

The env-var contract is the right call when:

- Your CI already runs a database service container — `services.postgres` in GitHub Actions, `docker:dind` in GitLab, equivalents elsewhere. The contract just names the env var.
- You're paying a noticeable dependency cost for testcontainers (CVE triage, version cascade, build time).
- Your tests can tolerate cross-test reset via TRUNCATE rather than container restart.

It's the wrong call when:

- You need per-test container isolation. Some tests modify postgres.conf, install extensions, or assert on autovacuum behaviour — none of which survive TRUNCATE.
- Your codebase tests against multiple databases simultaneously (Postgres + MySQL + SQLite) and the in-process container model keeps the wiring uniform.
- You're publishing a library and want consumers to be able to run your tests without external setup.

For a single-database service where the schema is one-way migrations and the tests need a real Postgres for `FOR UPDATE` semantics, the env-var contract is 130 lines, zero deps, and matches the CI shape you already have.

## The summary

- Testcontainers pulled docker/docker → docker/cli → grpc-gateway, which rewrote our Go floor to 1.25.
- The replacement reads `DATABASE_URL`, applies migrations once per process, bounds the test pool, and exposes a `Reset` helper.
- Local: `make db-up && export DATABASE_URL=... && make test-int`. CI: `services.postgres` provides the database, env var is injected.
- The one trap was `go test ./...` running test binaries in parallel against the same DB. Fixed with `-p 1` in the Makefile.
- Tradeoff is contributor friction (need to set up Postgres) for a smaller dep graph and a stable Go floor. We took it.

---

Source code: [github.com/PratikDhanave/wallet-transfer-assignment](https://github.com/PratikDhanave/wallet-transfer-assignment)

Follow for the next post in this series — on concurrency safety as an invariant test, not a benchmark.
