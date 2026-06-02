# Idempotency Keys as Primary Keys: the Race-Loss Replay Pattern

There are two ways to make a POST endpoint exactly-once: a distributed lock you have to operate, or a primary key the database already gives you for free.

This is the second one.

We're going to walk the full implementation in a Go wallet-transfer service: the table design, the fast-path/slow-path split, the race-loss branch, and an integration test that fires N goroutines with the same idempotency key and asserts everyone observes the same transfer id.

Repo: [github.com/PratikDhanave/wallet-transfer-assignment](https://github.com/PratikDhanave/wallet-transfer-assignment).

## What "exactly-once" actually means at an API boundary

True exactly-once-delivery is a fiction at the network layer. The client can always drop a response and retry; the server can never tell the difference between "first call" and "retry of a call whose response I dropped." So "exactly-once" at an API boundary means the *side effect* happens at most once, and every retry observes the same outcome.

Concretely, for a transfer endpoint:

- POST with key `K`, body B → returns transfer T1 in state PROCESSED.
- POST with key `K`, body B (retry) → returns transfer T1 in state PROCESSED. Same id. Same balances. No double-debit.
- POST with key `K`, body B' (different body) → returns 409 Conflict. The client is reusing a key it shouldn't.

That's the contract. Everything below exists to enforce it.

## The two-path split: fast read, slow write

Most idempotency-key implementations look like this:

1. Read the idempotency record by key.
2. If it exists, return the original response.
3. Otherwise, run the side effect, write the record, return the new response.

Step 1 → step 3 is the race. Two concurrent requests both see "no record" and both proceed to step 3.

The fix is to keep the read for the common case (a duplicate, where the record already exists), and use a transactional insert with a unique-violation check for the uncommon case (a true first-time write that loses the race).

The wallet repo's `TransferService.Create` is the canonical shape. Here's the dispatcher, edited for narrative flow:

```go
func (s *TransferService) Create(ctx context.Context, req CreateTransferRequest) (*domain.Transfer, error) {
    if err := req.Validate(); err != nil {
        return nil, err
    }
    hash := req.hash()

    // Fast path: the idempotency record already exists.
    if t, err := s.replayIfExists(ctx, s.exec, req.IdempotencyKey, hash); err != nil {
        return nil, err
    } else if t != nil {
        return t, nil
    }

    // Slow path: open a transaction and try to be the first writer.
    var result *domain.Transfer
    txErr := s.txm.RunInTx(ctx, func(exec repository.Executor) error {
        t, err := s.executeTransfer(ctx, exec, req, hash)
        if err != nil {
            return err
        }
        result = t
        return nil
    })

    // Race-loss path: another tx won the PK insert. By the time we
    // see 23505, the winner has committed — so the replay below is
    // guaranteed to find the row.
    if errors.Is(txErr, repository.ErrIdempotencyExists) {
        return s.replayIfExists(ctx, s.exec, req.IdempotencyKey, hash)
    }
    if txErr != nil {
        return nil, txErr
    }
    return result, nil
}
```

Three branches:

- **Fast path replay.** Common case for retries. A non-transactional `SELECT` is fine because reads outside a tx see committed data, and the winner committed both the side-effect rows and the idempotency record atomically.
- **Slow path first write.** Opens a tx, does the work, inserts the idempotency record last. If the record insert wins, the tx commits and we return the new transfer.
- **Race loss replay.** The record insert lost to a concurrent winner. Postgres rolls our tx back (no half-state), and we fall through to the same fast-path replay code — now guaranteed to find a row.

The race-loss branch is the load-bearing part. Let's look at it more carefully.

## The slow path: inserting the side effects and the key in one tx

`executeTransfer` is the function that runs inside the transaction. Step order matters:

```go
func (s *TransferService) executeTransfer(
    ctx context.Context,
    exec repository.Executor,
    req CreateTransferRequest,
    hash string,
) (*domain.Transfer, error) {
    // 1. lock wallets in ordered id (see prior post on deadlocks)
    // 2. read balances
    // 3. insert the transfer row in PENDING

    t := &domain.Transfer{
        ID: uuid.New(), FromWalletID: req.FromWalletID,
        ToWalletID: req.ToWalletID, Amount: req.Amount,
        State: domain.StatePending,
    }
    if err := s.transfers.Insert(ctx, exec, t); err != nil {
        return nil, err
    }

    // 4. claim the idempotency key. ErrIdempotencyExists is the
    //    race-loss signal; bubble it up so Create can replay.
    if err := s.idem.Insert(ctx, exec, req.IdempotencyKey, hash, t.ID); err != nil {
        return nil, err
    }
    // 5. ... balance check / FAILED commit / PROCESSED happy path ...
}
```

A subtle point: the idempotency row is inserted *after* the transfer row, because it carries a foreign key to `transfers(id)`. The FK requires the transfer to already exist in the same transaction. The whole sequence is atomic — either both commit or neither does.

## The repository's job: SQLSTATE 23505 → ErrIdempotencyExists

The repository layer wraps the conflict detection. Here is the actual insert, verbatim from `internal/repository/postgres/idempotency.go`:

```go
const pgUniqueViolation = "23505"

func (r *IdempotencyRepo) Insert(ctx context.Context, exec repository.Executor, key, hash string, transferID uuid.UUID) error {
    const q = `INSERT INTO idempotency_records (key, request_hash, transfer_id) VALUES ($1, $2, $3)`
    _, err := exec.ExecContext(ctx, q, key, hash, transferID)
    if err == nil {
        return nil
    }
    var pgErr *pgconn.PgError
    if errors.As(err, &pgErr) && pgErr.Code == pgUniqueViolation {
        return repository.ErrIdempotencyExists
    }
    return fmt.Errorf("insert idempotency: %w", err)
}
```

Two things worth noting:

**We check the typed error code, not the error string.** Postgres' message text changes between versions and locales. The SQLSTATE code is part of the wire protocol and the SQL standard; it's stable. String matching is brittle in a way that fails *silently* — the moment pgx rewords the message, your idempotency path becomes a generic 500 and nobody notices until a CFO does.

**The `INSERT ... ON CONFLICT DO NOTHING` alternative would also work** — it would skip the conflict instead of erroring — but it complicates the race-loss detection: you'd need to inspect the row count to distinguish "first writer" from "duplicate," and that's harder to map to a clean Go error type. The unique-violation pattern is more direct.

## The schema: PRIMARY KEY is the whole story

The table is small:

```sql
CREATE TABLE idempotency_records (
    key           TEXT        PRIMARY KEY,
    request_hash  TEXT        NOT NULL,
    transfer_id   UUID        NOT NULL REFERENCES transfers(id),
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now()
);
```

`key` is the PRIMARY KEY. That single declaration is the entire serialization mechanism. Concurrent first-time inserts of the same key block on the unique index's b-tree lock; the loser waits for the winner to commit; the loser then receives 23505.

`request_hash` is for the body-mismatch case. We hash `from|to|amount` (not the raw JSON — whitespace shouldn't count as a different request) and store it alongside the key. On replay, we compare hashes. Mismatch → `ErrIdempotencyConflict` → HTTP 409.

```go
func (r CreateTransferRequest) hash() string {
    h := sha256.Sum256([]byte(fmt.Sprintf("%s|%s|%d", r.FromWalletID, r.ToWalletID, r.Amount)))
    return hex.EncodeToString(h[:])
}
```

`transfer_id` is the FK back to the actual side-effect row, so replay is one join away.

## An integration test that proves it

The shape of the test is "fire N goroutines with the same key, assert they all observe the same transfer id."

```go
func TestTransferService_Create_ConcurrentSameKey(t *testing.T) {
    db := newTestDB(t)
    svc := newTransferService(t, db)

    from := seedWallet(t, db, "wallet-A", 100)
    to := seedWallet(t, db, "wallet-B", 0)

    const N = 50
    key := "shared-key-" + uuid.New().String()

    var wg sync.WaitGroup
    ids := make(chan uuid.UUID, N)
    errs := make(chan error, N)

    for i := 0; i < N; i++ {
        wg.Add(1)
        go func() {
            defer wg.Done()
            t, err := svc.Create(ctx, CreateTransferRequest{
                IdempotencyKey: key,
                FromWalletID:   from.ID,
                ToWalletID:     to.ID,
                Amount:         10,
            })
            if err != nil {
                errs <- err
                return
            }
            ids <- t.ID
        }()
    }
    wg.Wait()
    close(ids); close(errs)

    for err := range errs {
        t.Fatalf("unexpected error: %v", err)
    }
    var first uuid.UUID
    for id := range ids {
        if first == uuid.Nil {
            first = id
            continue
        }
        if id != first {
            t.Fatalf("observed two transfer ids for same key: %s and %s", first, id)
        }
    }

    // And the source wallet balance moved by exactly one transfer.
    fromAfter := getWallet(t, db, from.ID)
    if fromAfter.Balance != 90 {
        t.Fatalf("expected balance=90, got %d", fromAfter.Balance)
    }
}
```

50 goroutines, 1 transfer id observed by all of them, source balance moved by exactly 10. That's the guarantee.

## Body-hash mismatch: 409 vs 422

A subtle product call: when the same key is replayed with a different body, do you return 409 (Conflict) or 422 (Unprocessable Entity)?

The wallet repo uses 409. The reasoning: 422 implies "the body is malformed" — but the body is *fine*; the problem is that it conflicts with a prior commitment. 409 says "your request conflicts with the current state of the resource," which is exactly the case. The current state of the idempotency key is "already bound to a different transfer."

This is taste, but it's defensible taste. The HTTP error code becomes part of the contract; the client can branch on it cleanly.

## What this pattern does not give you

Worth being honest about scope:

- **Cross-resource exactly-once.** If a transfer triggers an email, you still need an outbox or a workflow engine. The PK guarantees the transfer side effect; it can't reach into Mailgun.
- **Expiry.** The repo keeps idempotency records forever. In a real system you'd add a `(created_at)` index and a periodic GC job for records older than (say) 7 days. The replay window is bounded by your retention.
- **Cross-region replication.** The PK is local to one Postgres instance. If you replicate, you need to ensure idempotency keys are partitioned by some routing dimension so the same key always lands on the same primary. Otherwise you have N independent uniqueness scopes.

## The takeaway

Distributed locks are infrastructure you have to operate, monitor, and reason about under partition. Primary keys are a guarantee the database already gives you, for free, with semantics that have been stable since the 1970s. For exactly-once-at-the-API on a single Postgres, the PK is almost always the right tool.

Next post in this series: how a single `UNIQUE (transfer_id, type)` constraint turns "every transfer has exactly one DEBIT and one CREDIT" from a documented promise into something Postgres physically refuses to violate. Follow for the next one.

Repo: [github.com/PratikDhanave/wallet-transfer-assignment](https://github.com/PratikDhanave/wallet-transfer-assignment)
