# Deadlock-Free Wallet Transfers with `SELECT ... FOR UPDATE` and a 4-Line Go Helper

You can simulate any Postgres deadlock with two `SELECT ... FOR UPDATE` statements and a coffee break. This post is about why the fix is `if a < b { lock(a); lock(b) }` and what the production code actually looks like in a Go wallet-transfer service.

We'll walk:

- a primer on the Postgres lock manager and what "deadlock" actually means in this layer,
- a 10-line Go reproducer you can run against any vanilla PG,
- the `orderedPair` + `executeTransfer` pattern as it ships in the wallet repo,
- an integration test that hammers the pattern with hundreds of concurrent counter-direction transfers,
- a sober discussion of pessimistic vs. optimistic locking for financial workloads,
- and footnotes on isolation levels (because somebody is going to ask).

The repo: [github.com/PratikDhanave/wallet-transfer-assignment](https://github.com/PratikDhanave/wallet-transfer-assignment).

## Why two transfers in opposite directions deadlock

Postgres acquires row-level locks lazily. When you run `SELECT ... FOR UPDATE` on a row, Postgres takes an exclusive row lock that other writers must wait on until your transaction commits or rolls back. The lock is held on the row's `tid`, tracked in shared memory by the lock manager.

That's enough machinery to deadlock. Two transactions, two rows:

- T1 locks row A. (`BEGIN; SELECT ... FROM wallets WHERE id='A' FOR UPDATE;`)
- T2 locks row B.
- T1 tries to lock row B → blocks behind T2.
- T2 tries to lock row A → blocks behind T1.

There is a cycle in the wait-for graph. Postgres' deadlock detector wakes up (`deadlock_timeout`, default 1s), picks a victim, and aborts it with SQLSTATE `40P01`.

That timeout is not free. Every counter-direction pair under load pays at least one second of wall-clock waiting for the detector to notice. If your retry loop has any backoff at all, you're north of a couple of seconds per affected request. Throughput collapses long before deadlocks become rare.

## A 10-line reproducer

You can produce the deadlock deterministically in two shells:

```sql
-- Session 1
BEGIN;
SELECT * FROM wallets WHERE id = 'A' FOR UPDATE;
-- Now go to session 2, then come back here:
SELECT * FROM wallets WHERE id = 'B' FOR UPDATE;
```

```sql
-- Session 2
BEGIN;
SELECT * FROM wallets WHERE id = 'B' FOR UPDATE;
SELECT * FROM wallets WHERE id = 'A' FOR UPDATE;
```

Run them in order: 1a, 2a, 2b, 1b. Within `deadlock_timeout` one of them dies.

In Go, the same shape:

```go
for i := 0; i < 2; i++ {
    go func(from, to string) {
        tx, _ := db.BeginTx(ctx, nil)
        defer tx.Rollback()
        tx.ExecContext(ctx, `SELECT * FROM wallets WHERE id=$1 FOR UPDATE`, from)
        time.Sleep(50 * time.Millisecond) // make the race deterministic
        tx.ExecContext(ctx, `SELECT * FROM wallets WHERE id=$1 FOR UPDATE`, to)
        tx.Commit()
    }("A", "B")
    go func() { /* opposite direction */ }()
}
```

If you've never seen this fail before, try it once. It's instructive.

## The naive fix and why it's a trap

The reflex is to retry. The retry loop looks like:

```go
for attempts := 0; attempts < 3; attempts++ {
    err := runTransfer(ctx)
    if isDeadlock(err) {
        time.Sleep(backoff(attempts))
        continue
    }
    return err
}
```

This is correctness theatre. The transactions still deadlock; you're just paying for the deadlock-detect, the rollback, and the redo on every collision. Under sustained load with N writers and any nonzero rate of counter-direction pairs, your effective throughput is bounded by `1 / (deadlock_timeout + redo_cost)` per affected wallet pair.

Worse: a retry loop encourages teams to declare the problem "solved" and stop looking. The next time the lock graph grows a new edge — say, you add a third row lock for a fee account — the deadlock manifests differently and nobody connects the dots.

## The real fix: a global lock order

A deadlock requires a cycle. A cycle requires two transactions to want the same set of locks in opposite orders. Pick a total order on the lockable resources, always acquire in that order, and the lock graph becomes a DAG. Cycles are impossible. Deadlocks are impossible.

In a wallet service the natural order is the wallet id. Lexicographic works fine; it just needs to be the same on every code path.

Here is the helper in the wallet repo (`internal/service/transfer.go`):

```go
// orderedPair returns its two arguments in lexicographic ascending
// order. The whole point is the deterministic wallet lock order
// described on executeTransfer.
func orderedPair(a, b string) (string, string) {
    if a < b {
        return a, b
    }
    return b, a
}
```

And here is how `executeTransfer` uses it. Lock first in id order; then re-fetch the rows by their *semantic* role (source / destination) for the business logic. The two roles are decoupled from the two lock acquisitions:

```go
func (s *TransferService) executeTransfer(
    ctx context.Context,
    exec repository.Executor,
    req CreateTransferRequest,
    hash string,
) (*domain.Transfer, error) {
    // Step 1: lock both wallets in lex-ascending order so two
    // concurrent transfers in opposite directions cannot deadlock.
    firstID, secondID := orderedPair(req.FromWalletID, req.ToWalletID)
    if _, err := s.wallets.LockForUpdate(ctx, exec, firstID); err != nil {
        return nil, err
    }
    if _, err := s.wallets.LockForUpdate(ctx, exec, secondID); err != nil {
        return nil, err
    }

    // Step 2: read the locked wallets back so we can validate the
    // source balance and compute the new balances.
    src, err := s.wallets.Get(ctx, exec, req.FromWalletID)
    if err != nil {
        return nil, err
    }
    dst, err := s.wallets.Get(ctx, exec, req.ToWalletID)
    if err != nil {
        return nil, err
    }
    // ... insert transfer, update balances, write ledger ...
}
```

Notice the two-step structure. We acquire locks by ordered id. Then we read the rows by their business role. This split matters: the locking layer doesn't need to know which wallet is the source, and the business layer doesn't need to know which lock came first.

## An integration test that proves it

The repo's integration suite hammers this with concurrent counter-direction transfers. The shape is:

```go
func TestTransferService_NoDeadlockUnderCounterDirection(t *testing.T) {
    db := newTestDB(t)
    svc := newTransferService(t, db)

    // Two wallets, both well-funded so we don't hit FAILED paths.
    a := seedWallet(t, db, "wallet-A", 1_000_000)
    b := seedWallet(t, db, "wallet-B", 1_000_000)

    const N = 200
    var wg sync.WaitGroup
    errs := make(chan error, N)

    for i := 0; i < N; i++ {
        wg.Add(1)
        go func(i int) {
            defer wg.Done()
            from, to := a.ID, b.ID
            if i%2 == 0 {
                from, to = b.ID, a.ID
            }
            _, err := svc.Create(ctx, CreateTransferRequest{
                IdempotencyKey: fmt.Sprintf("k-%d", i),
                FromWalletID:   from,
                ToWalletID:     to,
                Amount:         1,
            })
            errs <- err
        }(i)
    }
    wg.Wait()
    close(errs)
    for err := range errs {
        if err != nil {
            t.Fatalf("unexpected error: %v", err)
        }
    }
}
```

With `orderedPair` in place this test runs clean against PG16 every time. Remove it and `pgErr.Code == "40P01"` appears within a few iterations under any meaningful concurrency.

The repo's measured numbers from a focused concurrency run on a laptop: 533 transactions/second sustained, p50 23ms, p95 83ms, p99 298ms — no deadlock errors, no retry loop in sight.

## Pessimistic vs optimistic for money

This is the part where someone says "you should use optimistic concurrency control instead." Worth answering, because there's a real tradeoff.

Optimistic CC reads a row plus its version, computes the new value, and writes `UPDATE ... WHERE id = ? AND version = ?`. If nobody else updated in the meantime, one row changes and you commit. If someone else got there first, zero rows change and you retry.

For workloads where conflicts are rare, optimistic is genuinely faster. No row locks, no blocking, just a cheap version check.

For money, conflicts on "hot" wallets (think: the platform's float account, the top creator) are not rare. Optimistic CC degenerates into a retry storm on exactly the rows you care about, with no fairness guarantee — the unlucky writer can be starved for arbitrary wall-clock time.

Pessimistic CC with a global lock order gives you predictable latency under contention. Writers form a FIFO queue on the lock; throughput on a hot row is bounded by `1 / tx_duration`, which is honest and tunable (shorter transactions = higher throughput). You pay for the lock, but you know what you're paying.

The wallet repo picks pessimistic. The hot-row argument is the deciding factor; the deadlock fix is what makes pessimistic *boring* enough to actually deploy.

## Footnote: isolation levels

People sometimes ask, "doesn't `SERIALIZABLE` make all this unnecessary?"

It does — for correctness. SSI (Serializable Snapshot Isolation, what Postgres ships) will detect anomalous interleavings and abort one of the transactions with `40001 serialization_failure`. You write straight-line code and retry on conflict.

It does not — for latency under contention. SSI conflicts are detected at commit time, after you've done all the work. On a hot wallet you'll see high abort rates and the same retry-storm problem as optimistic CC, just shifted later in the request lifecycle.

`READ COMMITTED` (Postgres default) plus explicit `SELECT ... FOR UPDATE` with an ordered acquisition gives you the simplest mental model: locks are held for the duration of the transaction; the wait graph is acyclic by construction; throughput is bounded by transaction duration on hot rows.

That's the model the wallet repo uses. The lock manager does the work. The four-line helper makes the lock manager well-behaved.

---

If you found this useful, I'm publishing the next post in this series on idempotency-as-PK: how to make a POST endpoint exactly-once with nothing but a primary key and a unique-violation error code. Follow along.

Repo: [github.com/PratikDhanave/wallet-transfer-assignment](https://github.com/PratikDhanave/wallet-transfer-assignment)
