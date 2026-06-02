# Why We Kept Stdlib `net/http` + `database/sql` Instead of a Framework

This is a long-form on a single design choice repeated four times: we picked the boring option on purpose. Here are the four places, the code, the alternatives, and the numbers.

The service is a Go wallet-transfer system — POST /transfers, GET /wallets/{id}, the usual financial-domain shape. The interesting constraints are double-entry ledger integrity, durable idempotency, and no double-spend under concurrent load. The uninteresting parts — routing, middleware, JSON, query building — are exactly the places a framework would have absorbed.

We didn't reach for one. Here's the four-choice breakdown.

---

## The shape of the service

Six HTTP endpoints. One database. One binary.

- `POST /wallets` — create a wallet
- `GET /wallets/{id}` — read a wallet
- `GET /wallets/{id}/transfers` — list transfers (paginated)
- `POST /transfers` — create a transfer (the interesting one)
- `GET /transfers/{id}` — read a transfer
- `GET /healthz` — liveness

The "interesting" part is `POST /transfers`. It must be idempotent under retry, atomic under concurrent debits, and durable across crashes. The other endpoints are straightforward — a framework would have saved maybe 100 lines on them, but those 100 lines are not where the bugs live.

The four design choices below all push complexity toward the layer that matters (the database) and away from the layer that doesn't (the HTTP surface).

## Choice 1: Pessimistic locking + PK-as-mutex idempotency

The exactly-once requirement is satisfied by the primary key on `idempotency_records`. The no-double-spend requirement is satisfied by `SELECT ... FOR UPDATE` on both wallets in lexicographic id order. Both checks live in the same Postgres transaction.

```go
// internal/service/transfer.go (excerpted)
err := s.txm.RunInTx(ctx, func(exec repository.Executor) error {
    firstID, secondID := orderedPair(req.FromWalletID, req.ToWalletID)
    if _, err := s.wallets.LockForUpdate(ctx, exec, firstID); err != nil {
        return err
    }
    if _, err := s.wallets.LockForUpdate(ctx, exec, secondID); err != nil {
        return err
    }
    src, _ := s.wallets.Get(ctx, exec, req.FromWalletID)
    // ... insert transfer + idempotency record + ledger ...
    if err := s.idem.Insert(ctx, exec, req.IdempotencyKey, hash, t.ID); err != nil {
        return err // returns ErrIdempotencyExists on 23505
    }
    return nil
})
if errors.Is(err, repository.ErrIdempotencyExists) {
    return s.replayIfExists(ctx, s.exec, req.IdempotencyKey, hash)
}
```

```sql
CREATE TABLE idempotency_records (
    key           TEXT        PRIMARY KEY,
    request_hash  TEXT        NOT NULL,
    transfer_id   UUID        NOT NULL REFERENCES transfers(id),
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now()
);
```

The PK on `key` is the entire serialisation primitive. Two concurrent first-time inserts with the same key race on the index lock; the loser receives SQLSTATE `23505` (unique_violation), which our repository maps to `ErrIdempotencyExists`, and the service replays the winner's response via a fast-path SELECT.

The alternative would have been an optimistic CAS loop on a `version` column plus a Redis lock for the idempotency key. That's two storage systems for one invariant, and the recovery story for a half-committed state is painful. Postgres' PK lock is one system, free, and exact.

## Choice 2: `database/sql` + pgx, no ORM

Every query is a literal string at the call site. The repository layer is thin — one Go function per query, with `Executor` parameterised so the same function works inside and outside a transaction.

```go
// internal/repository/postgres/wallet.go (excerpted)
const q = `SELECT id, balance, created_at, updated_at FROM wallets WHERE id = $1`
row := exec.QueryRowContext(ctx, q, id)
return scanWallet(row)
```

```go
// internal/db/db.go (excerpted)
func OpenWithPool(dsn string, pool PoolConfig) (*sql.DB, error) {
    db, err := sql.Open("pgx", dsn)
    if err != nil {
        return nil, fmt.Errorf("sql.Open: %w", err)
    }
    if pool.MaxOpenConns > 0 {
        db.SetMaxOpenConns(pool.MaxOpenConns)
    }
    // ...
    if err := db.Ping(); err != nil {
        _ = db.Close()
        return nil, fmt.Errorf("db.Ping: %w", err)
    }
    return db, nil
}
```

The alternative would have been GORM, ent, or sqlc. Each one is a real tool used by serious teams. Each one also abstracts the part of the system we most care about reviewing — the actual SQL, the actual transaction boundaries, the actual error codes.

When a code reviewer reads our wallet repository, they read SQL. When they read a GORM-based repository, they read Go code that constructs SQL at runtime, and to verify the lock semantics they have to either trust the framework or print the generated query. For most CRUD code that trade-off is fine. For financial code where `FOR UPDATE` is load-bearing, it isn't.

sqlc deserves a separate mention because it's the option that comes closest to the convention we chose. sqlc generates Go code from SQL queries — you write the SQL, sqlc gives you typed Go. The tradeoff is a code-generation step and a directory of generated files that need to live in your repo or your build. We chose to type the scan code by hand because the volume is small (12 queries total) and the generated code adds a layer for any reviewer to navigate. At 50+ queries, sqlc is the obvious right answer.

## Choice 3: Single transaction per request

The transfer transaction does seven things and commits exactly once:

```go
err := s.txm.RunInTx(ctx, func(exec repository.Executor) error {
    // 1. lock both wallets in id order
    // 2. read balances
    // 3. insert transfer (PENDING)
    // 4. insert idempotency record
    // 5. update balances
    // 6. insert ledger entries (DEBIT + CREDIT)
    // 7. mark transfer PROCESSED
    return nil
})
```

`RunInTx` is 30 lines. It begins a tx, runs the function, commits on nil, rolls back on error, rolls back and re-panics on panic. There is no framework, no annotation, no `@Transactional`. The transaction boundary is the function literal you can see.

The alternative would have been a saga: each step its own short tx, with compensating actions on failure and an external coordinator for recovery. Sagas are the right answer when the transaction crosses a process boundary — an external bank API call, a payment processor, a cross-region database. They are the wrong answer when the entire work fits in one local Postgres transaction, because you're paying the coordination complexity for no isolation benefit.

The whole `RunInTx` body runs in tens of milliseconds at our load (p50 23ms end-to-end including HTTP overhead). Postgres holds the row locks for that duration. Any concurrent transfer touching the same wallet pair serialises behind. That serialisation is exactly the contention we *want* serialised, because the alternative — concurrent debits on the same wallet — is the double-spend bug we're paid to prevent.

## Choice 4: Real Postgres tests, no mocks

Every integration test runs against a real `postgres:16-alpine`. Locally, that's `docker compose up -d postgres`. In CI, it's the `services.postgres` block in `.github/workflows/ci.yml`. Either way, the test process reads `DATABASE_URL` from the environment.

```go
// internal/testdb/testdb.go (excerpted)
func Get(tb testing.TB) *sql.DB {
    tb.Helper()
    once.Do(func() {
        sharedDB, sharedErr = start()
    })
    if sharedErr != nil {
        if sharedErr == errNoDSN {
            tb.Skipf("integration tests require %s ...", envDatabaseURL, envDatabaseURL)
        }
        tb.Fatalf("open postgres at %s: %v", envDatabaseURL, sharedErr)
    }
    return sharedDB
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
```

The alternative would have been sqlmock. sqlmock is a tool that lets you assert "the code called `db.Query` with this string and these arguments." That assertion is fine when the code is doing trivial CRUD. It is not fine when the code is doing `SELECT ... FOR UPDATE` and you need to know whether the lock actually serialises concurrent writers.

A few things sqlmock cannot test:

- Whether `CHECK (balance >= 0)` rejects a write.
- Whether `UNIQUE (transfer_id, type)` rejects a second DEBIT for the same transfer.
- Whether a `23505` unique_violation arrives as the right SQLSTATE for your error mapping to fire.
- Whether two concurrent goroutines actually serialise on the PK lock.
- Whether the transaction isolation level matters for your read-then-write pattern.

A real Postgres tests all of those automatically because the assertions are the ones the database itself makes. Our stress test fires 1,000 concurrent goroutines at a single wallet seeded with 10,000 and asserts that exactly 100 succeed and 900 fail cleanly — a test that has no equivalent under sqlmock because the answer depends on what Postgres actually does under contention.

The cost of the real-DB approach is the DB. You need Docker locally and a service container in CI. We accept that cost because the alternative is shipping financial code with tests that pass while the production behaviour is wrong.

## Measured outcomes

Numbers from the repo:

- **146 unit/integration tests, 88.1% coverage.**
- **297 RPS sustained** under a 60-second k6 load test (`make load`) against a single local Postgres.
- **p50 23ms / p95 83ms / p99 298ms** at that load.
- **533 transfers/second** in the many-wallets stress test (`make stress`).
- **41 transitive dependencies** in `go.mod` after we removed testcontainers (down from 86).

The dependency count is the part that flagged for reviewers. A typical Go service that reaches for chi + GORM + zap + viper + testcontainers runs 100+ transitive deps. Ours runs 41 because every choice above removed a layer.

Fewer deps means fewer CVE advisories to triage, fewer breaking changes to chase, fewer arguments about whether to upgrade. `govulncheck` runs in seconds against our `go.sum` because there's less to scan.

## When each choice fails

Each of the four choices is wrong for the right service. Specifically:

**Stdlib net/http fails** when you have 50+ endpoints with consistent shape. The boilerplate compounds. A framework's `r.Group("/admin")` is genuinely faster to read than `mux.HandleFunc` repeated 20 times. If your service is mostly CRUD over a stable schema, reach for chi.

**database/sql fails** when your query volume is high and your scan code is repetitive. By the time you've written your tenth `for rows.Next() { ... }` loop with the same shape, sqlc generates better code than you do. We'd reach for sqlc at 30+ queries.

**Single transaction fails** when work crosses a process boundary. An external bank API call inside the same tx as a wallet write is a deadlock waiting to happen. A saga is the right answer; build one when you have the second system to coordinate.

**Real-DB tests fail** when you're testing pure business logic that doesn't touch the database. Use unit tests against the domain layer for those — we have plenty. The integration tests cover only the code paths that touch SQL.

## The meta-lesson

"Boring" is not a virtue by itself. The reason boring won here is that every part of this service that *isn't* boring (FOR UPDATE locking, idempotency PK, double-entry ledger, conservation invariants) needs all of your attention. A framework or an ORM in the boring layer doesn't reduce the interesting complexity; it adds another layer to think about while you're trying to think about the interesting layer.

Pick boring in the places where you have nothing interesting to say. Save the unboring decisions for the places where you do.

---

Source code: [github.com/PratikDhanave/wallet-transfer-assignment](https://github.com/PratikDhanave/wallet-transfer-assignment)

Follow for the next post in this series — on replacing testcontainers with a 130-line `DATABASE_URL` contract that freed our Go floor version.
