# A Double-Entry Ledger That Can't Go Out of Balance — by Schema

Most ledger bugs aren't "we missed a write." They're "we wrote one of two."

A single `CHECK` plus a single `UNIQUE` make that impossible to commit. This post is about why a wallet-transfer service's `ledger_entries` table can structurally refuse most of the failure modes that historically required runbooks, alerts, and 3am pages.

We'll cover:

- the failure modes that put real ledgers out of balance,
- the constraint set in the wallet repo, line by line,
- what each constraint catches (and what it doesn't),
- an integration test that *tries* to insert an unbalanced ledger and asserts Postgres rejects it,
- the broader principle: schema as a refinement-type system,
- and the limits — when not to push invariants into the schema.

Repo: [github.com/PratikDhanave/wallet-transfer-assignment](https://github.com/PratikDhanave/wallet-transfer-assignment).

## How ledgers go out of balance

A double-entry ledger is a sequence of paired rows: every value movement is one DEBIT against the source account and one CREDIT against the destination, equal in magnitude. The invariant is that the sum of DEBITs equals the sum of CREDITs, always.

In a textbook this is just bookkeeping. In production, here's how it breaks:

- A transfer handler does two `INSERT`s. The first succeeds. The connection drops between writes. The transaction is left open until the connection reaper kills it. Only the DEBIT was written; the rollback was implicit at the connection close. The application logged "transfer failed," but the row is there.
- A panic in a deferred function on the happy path commits an unintended partial state.
- A retry library retries an idempotent-looking operation that wasn't actually idempotent. The DEBIT gets written twice for the same transfer.
- A new "fee" code path is added that writes a single CREDIT (the fee revenue account) without a paired DEBIT, because the developer didn't realize "ledger entries always come in pairs" was load-bearing.
- A migration changes the meaning of the `type` column. Old rows have NULL where new code expects 'DEBIT'/'CREDIT'.

You can defend against any single one in code. You can't easily defend against all of them — especially the last two, which are the most common in long-lived systems.

The other option is to tell the database what a valid ledger entry looks like, and let the database refuse anything else.

## The constraint set

Here's the `ledger_entries` table from the repo's `migrations/0001_init.up.sql`:

```sql
CREATE TABLE ledger_entries (
    id           BIGSERIAL   PRIMARY KEY,
    transfer_id  UUID        NOT NULL REFERENCES transfers(id),
    wallet_id    TEXT        NOT NULL REFERENCES wallets(id),
    type         TEXT        NOT NULL CHECK (type IN ('DEBIT','CREDIT')),
    amount       BIGINT      NOT NULL CHECK (amount > 0),
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (transfer_id, type)
);
```

And the relevant constraints on `transfers`:

```sql
CREATE TABLE transfers (
    id              UUID        PRIMARY KEY,
    from_wallet_id  TEXT        NOT NULL REFERENCES wallets(id),
    to_wallet_id    TEXT        NOT NULL REFERENCES wallets(id),
    amount          BIGINT      NOT NULL CHECK (amount > 0),
    state           TEXT        NOT NULL CHECK (state IN ('PENDING','PROCESSED','FAILED')),
    failure_reason  TEXT,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    CHECK (from_wallet_id <> to_wallet_id)
);
```

And on `wallets`:

```sql
CREATE TABLE wallets (
    id          TEXT PRIMARY KEY,
    balance     BIGINT      NOT NULL CHECK (balance >= 0),
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);
```

Five constraints earn their keep. Walk through them.

### `CHECK (amount > 0)` on transfers and ledger_entries

A zero-value transfer creates a DEBIT of 0 and a CREDIT of 0. Mathematically a no-op, in practice a noise row that turns up in reports as a transfer that did nothing. A negative amount is worse: you've inverted the meaning of DEBIT and CREDIT.

The CHECK eliminates both. The application also validates (`req.Amount <= 0` returns `domain.ErrInvalidAmount` in `CreateTransferRequest.Validate`), but the schema catches any future code path that forgets — a batch loader, a workflow engine, a backfill script.

### `CHECK (from_wallet_id <> to_wallet_id)` on transfers

A self-transfer would produce a DEBIT and a CREDIT on the *same* wallet for the same transfer. Net balance change: zero. Audit log: one transfer. The ledger reconciles but tells a lie about user behaviour. Worse: it inflates per-wallet transfer counts and skews any metric built on top of them.

The application also rejects it (`req.FromWalletID == req.ToWalletID` returns `domain.ErrSameWallet`), but again, schema is defence-in-depth.

### `CHECK (balance >= 0)` on wallets

Defence-in-depth against any future code path that updates a balance without first verifying under a row lock. The service layer does the `SELECT ... FOR UPDATE`, reads the balance, and decides whether to debit. The schema catches the case where someone, someday, forgets.

This one is particularly valuable because the bug it catches — a wallet going negative — is silent. There's no exception, no panic. The number just becomes wrong. A constraint converts a silent corruption into a loud transaction abort.

### `CHECK (state IN ('PENDING','PROCESSED','FAILED'))` on transfers

The state machine is finite. A typo in a literal string ("PROCESED") would otherwise commit a state nobody can query for or transition out of. The CHECK turns a typo into an immediate test failure instead of a stuck row that needs a manual cleanup.

### `UNIQUE (transfer_id, type)` on ledger_entries

The star of the show. Combined with `type IN ('DEBIT','CREDIT')`, this guarantees that for any transfer id there is at most one DEBIT row and at most one CREDIT row. The application can attempt to insert a second DEBIT all day; Postgres returns SQLSTATE 23505 and aborts the transaction.

A second DEBIT could happen from:

- a retry of a partially-failed transfer flow,
- a workflow engine that double-fires a step,
- a backfill that didn't realise rows already existed,
- a future code path that adds a "fee" leg without thinking about the constraint.

The constraint catches all of them.

It does *not* catch "we wrote the DEBIT but not the CREDIT," because that's a between-rows invariant, not a within-row one. We rely on transactional atomicity for that — both inserts are in the same `RunInTx` callback, so either both commit or neither does.

## What the application code looks like with these constraints

The application doesn't need to defensively retry on constraint violation; the only ways to hit one are bugs we want to know about. The repository wraps an unexpected `INSERT` error in a wrapped error:

```go
if err := s.ledger.Insert(ctx, exec, &domain.LedgerEntry{
    TransferID: t.ID,
    WalletID:   src.ID,
    Type:       domain.EntryDebit,
    Amount:     req.Amount,
}); err != nil {
    return nil, err
}
if err := s.ledger.Insert(ctx, exec, &domain.LedgerEntry{
    TransferID: t.ID,
    WalletID:   dst.ID,
    Type:       domain.EntryCredit,
    Amount:     req.Amount,
}); err != nil {
    return nil, err
}
```

Two inserts, same transaction. If either fails — for any reason, including a unique violation we didn't plan for — the whole transaction rolls back. The balances we updated in the same tx revert. The transfer row reverts. The idempotency key reverts. Nothing partial commits.

## An integration test that tries to insert garbage

Schema constraints deserve tests, because they're the silent guardrails. A test that *tries* to violate them and asserts Postgres rejects:

```go
func TestLedgerEntries_RejectsDuplicateDebit(t *testing.T) {
    db := newTestDB(t)
    ctx := context.Background()

    from := seedWallet(t, db, "wallet-A", 1000)
    to := seedWallet(t, db, "wallet-B", 0)
    transfer := seedTransfer(t, db, from.ID, to.ID, 100, domain.StateProcessed)

    // First DEBIT: should succeed.
    _, err := db.ExecContext(ctx,
        `INSERT INTO ledger_entries (transfer_id, wallet_id, type, amount)
         VALUES ($1, $2, 'DEBIT', $3)`,
        transfer.ID, from.ID, 100)
    if err != nil {
        t.Fatalf("first DEBIT failed: %v", err)
    }

    // Second DEBIT for the same transfer: should fail with 23505.
    _, err = db.ExecContext(ctx,
        `INSERT INTO ledger_entries (transfer_id, wallet_id, type, amount)
         VALUES ($1, $2, 'DEBIT', $3)`,
        transfer.ID, from.ID, 100)
    if err == nil {
        t.Fatalf("expected unique-violation, got nil")
    }
    var pgErr *pgconn.PgError
    if !errors.As(err, &pgErr) || pgErr.Code != "23505" {
        t.Fatalf("expected SQLSTATE 23505, got %v", err)
    }
}

func TestLedgerEntries_RejectsZeroAmount(t *testing.T) {
    db := newTestDB(t)
    ctx := context.Background()
    from := seedWallet(t, db, "wallet-A", 1000)
    to := seedWallet(t, db, "wallet-B", 0)
    transfer := seedTransfer(t, db, from.ID, to.ID, 100, domain.StateProcessed)

    _, err := db.ExecContext(ctx,
        `INSERT INTO ledger_entries (transfer_id, wallet_id, type, amount)
         VALUES ($1, $2, 'DEBIT', 0)`,
        transfer.ID, from.ID)
    if err == nil {
        t.Fatalf("expected CHECK violation, got nil")
    }
}

func TestTransfers_RejectsSelfTransfer(t *testing.T) {
    db := newTestDB(t)
    ctx := context.Background()
    w := seedWallet(t, db, "wallet-X", 1000)

    _, err := db.ExecContext(ctx,
        `INSERT INTO transfers (id, from_wallet_id, to_wallet_id, amount, state)
         VALUES ($1, $2, $2, 100, 'PENDING')`,
        uuid.New(), w.ID)
    if err == nil {
        t.Fatalf("expected CHECK violation on self-transfer, got nil")
    }
}
```

These tests exist to prevent silent regression. If a future migration relaxes one of these constraints — for whatever well-intentioned reason — the corresponding test fails immediately and the conversation happens in code review, not in incident review.

## Schema as a refinement-type system

The general principle is older than the wallet repo, older than Postgres, older than most software engineering: *make illegal states unrepresentable.* The phrase usually gets attributed to ML/F#-style type-driven design, but it applies cleanly to relational schema. A column type is a type. A `CHECK` is a refinement of that type. A `UNIQUE` is a constraint on the set of rows. Foreign keys are existential dependencies between tables.

Together, they form a type system that runs at write time, on every connection, regardless of which application is writing. The Go service can have a bug. A future Python admin tool can have a bug. A psql session at 2am with a tired engineer can have a typo. The schema is the last line of defence, and it is enforced by code that has been ferociously tested by literally millions of Postgres users.

The economic point: a constraint is a test that runs in production, on every write, forever, written once. It's the highest ROI defensive measure in the toolbox.

## When *not* to use schema constraints

Worth being clear about the limits.

**Cross-table invariants** generally don't belong in CHECK constraints. "Sum of all DEBITs for a transfer equals sum of all CREDITs" is the canonical example — you can't express it as a row-level CHECK, and the obvious alternatives (a trigger, a deferred constraint) are heavy, hard to reason about, and easy to break with future schema changes.

For those, use:

- **Transactional atomicity** to ensure both rows commit together (which is what the wallet repo does for DEBIT + CREDIT).
- **Periodic reconciliation** as a backstop. A daily job that sums DEBITs and CREDITs per wallet and alerts on drift. It's cheap, it's robust to schema evolution, and it catches the bugs that escape both code and constraints.

**Constraints on rapidly-evolving columns** can become painful. If you anticipate adding a new state to your state machine every quarter, a `CHECK (state IN (...))` becomes a per-quarter migration. Sometimes that's fine; sometimes you'd rather validate at the application boundary and let the column be free-text.

**Constraints with surprising performance implications.** A `UNIQUE` constraint is a b-tree index. On a hot insert path, that's a lock point. Most of the time the locking is fine and the throughput win from avoiding compensating logic dominates. But measure on the actual table.

## The takeaway

If you can express an invariant as a constraint, put it in the schema. Every CHECK and UNIQUE you add is a test that runs on every write, in every environment, against every client, for the lifetime of the system. That's leverage you don't get anywhere else in the stack.

Next post in this series: why the FAILED state of a transfer must *commit*, not roll back — and how that one decision turns failure into an idempotent outcome. Follow for the next one.

Repo: [github.com/PratikDhanave/wallet-transfer-assignment](https://github.com/PratikDhanave/wallet-transfer-assignment)
