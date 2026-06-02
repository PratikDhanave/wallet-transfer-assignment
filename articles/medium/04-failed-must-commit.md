# The FAILED State Must Commit: Why Insufficient Funds Is Not a Rollback

There's a class of bugs that only appears when a user retries: the side effect commits, the *reason for failing* doesn't, and the next attempt sees an empty database.

Fixing it changes how you write transactions.

This post walks the bug end-to-end in a Go wallet-transfer service, then derives the rule: **every "fail" path that you want to be idempotent must commit.** We'll cover:

- the timeline of the bug, in detail,
- the state-machine theory behind "terminal states must be durable,"
- the actual `executeTransfer` step 5 from the repo,
- an integration test that retries an insufficient-funds transfer and asserts the second call returns the *same* FAILED transfer (not PROCESSED),
- the broader pattern: when to commit on error, when to rollback,
- and the counter-examples — transient failures that genuinely should rollback.

Repo: [github.com/PratikDhanave/wallet-transfer-assignment](https://github.com/PratikDhanave/wallet-transfer-assignment).

## The bug, in painful detail

Imagine a transfer endpoint with idempotency keys, written the obvious way: any error from the business logic returns from the tx callback, which causes the transaction to roll back. Insufficient funds is an error, so insufficient funds rolls back.

```
t=10:00:00  POST /transfers
            { idempotency_key: "K", from: A, to: B, amount: 1000 }
            wallet A balance = 500
            service: lock(A), lock(B), read balance, return ErrInsufficientFunds
            tx rolls back
            → 422 to client
            DB state: no transfer row, no idempotency record

t=10:00:30  wallet A is topped up; balance is now 5000

t=10:00:45  POST /transfers (client retry)
            { idempotency_key: "K", from: A, to: B, amount: 1000 }
            service: fast-path replay → no record found
            service: lock(A), lock(B), read balance, 5000 >= 1000 → proceed
            inserts transfer (PROCESSED), debits A, credits B, writes ledger
            commits
            → 200 to client
            DB state: A=4000, B=4000, transfer is PROCESSED
```

The user was told their request failed. Then their request succeeded. With the same idempotency key. Without them asking for it.

This is not a race condition. It's a deterministic consequence of treating "insufficient funds" as a transactional error. The retry is *correct*. The handler is *correct*. The idempotency check is *correct*. The bug is the rollback decision.

## State-machine theory: terminal states must be durable

A transfer is a finite state machine. The states (in the wallet repo) are PENDING, PROCESSED, FAILED. The transitions:

```
                  +-----------+
                  |  PENDING  |
                  +-----+-----+
                        |
              +---------+---------+
              |                   |
              v                   v
       +-------------+      +-----------+
       |  PROCESSED  |      |  FAILED   |
       +-------------+      +-----------+
       (terminal)           (terminal)
```

PROCESSED and FAILED are both terminal. Once a transfer reaches a terminal state, that's the final answer; no later observation should see a different answer.

For the idempotency contract — "every retry returns the same outcome" — to hold, both terminal states must be durable. If FAILED is implemented as "rollback the tx and return an error to the caller," then FAILED is not durable. The next observation (a retry) cannot see FAILED, because there's nothing in the database to see. The retry will compute a fresh outcome from scratch, and that outcome may differ.

The fix is to make FAILED durable in the same way PROCESSED is: write the row, commit the tx.

## The actual code

Here is step 5 of `executeTransfer` from `internal/service/transfer.go`, verbatim:

```go
// Step 5: insufficient funds is a committed FAILED outcome — see
// the function doc comment for why we COMMIT rather than rollback.
if src.Balance < req.Amount {
    const reason = "insufficient funds"
    if err := s.transfers.UpdateState(ctx, exec, t.ID, domain.StateFailed, reason); err != nil {
        return nil, err
    }
    t.State = domain.StateFailed
    t.FailureReason = reason
    return t, nil
}
```

Three things happen in order:

1. `UpdateState` writes `state='FAILED'` and `failure_reason='insufficient funds'` to the already-inserted transfer row.
2. The in-memory `domain.Transfer` is updated so the caller sees the right state.
3. We return `(t, nil)` — *nil error*. That nil is the load-bearing detail.

The wrapping `RunInTx` closure looks like:

```go
txErr := s.txm.RunInTx(ctx, func(exec repository.Executor) error {
    t, err := s.executeTransfer(ctx, exec, req, hash)
    if err != nil {
        return err
    }
    result = t
    return nil
})
```

`RunInTx` commits when the callback returns nil, rolls back when it returns a non-nil error. By returning `(t, nil)` for the FAILED case, `executeTransfer` opts into the commit path. The FAILED transfer row commits. The idempotency record committed in step 4 commits. Both are now durable.

A retry of the same idempotency key will hit the fast-path replay, find the record, and return the same FAILED transfer. The user observes a consistent outcome regardless of how many times they retry or what happens to the balance in between.

## The integration test

The test is short and the assertion is precise: retry an insufficient-funds transfer after the wallet has been topped up, and assert that the second call returns the same FAILED transfer (not a fresh PROCESSED).

```go
func TestTransferService_FailedIsIdempotent(t *testing.T) {
    db := newTestDB(t)
    svc := newTransferService(t, db)

    from := seedWallet(t, db, "wallet-A", 50)   // only 50 minor units
    to := seedWallet(t, db, "wallet-B", 0)

    key := "retry-key-" + uuid.New().String()
    req := CreateTransferRequest{
        IdempotencyKey: key,
        FromWalletID:   from.ID,
        ToWalletID:     to.ID,
        Amount:         100, // more than from has
    }

    // First call: insufficient funds, FAILED, committed.
    first, err := svc.Create(ctx, req)
    if err != nil {
        t.Fatalf("first call: %v", err)
    }
    if first.State != domain.StateFailed {
        t.Fatalf("expected FAILED, got %s", first.State)
    }

    // Top up the wallet so a fresh attempt would now succeed.
    mustExec(t, db, `UPDATE wallets SET balance = 1000 WHERE id = $1`, from.ID)

    // Second call: same key. Must return the same FAILED transfer.
    second, err := svc.Create(ctx, req)
    if err != nil {
        t.Fatalf("second call: %v", err)
    }
    if second.ID != first.ID {
        t.Fatalf("expected same transfer id; first=%s second=%s", first.ID, second.ID)
    }
    if second.State != domain.StateFailed {
        t.Fatalf("retry returned %s instead of FAILED", second.State)
    }

    // And the wallet has NOT been debited by the retry.
    after := getWallet(t, db, from.ID)
    if after.Balance != 1000 {
        t.Fatalf("retry silently debited wallet: balance=%d", after.Balance)
    }
}
```

This test fails loudly if anyone, at any future point, changes step 5 to `return ErrInsufficientFunds`. That's the whole point.

## The general pattern

The decision tree for whether a failure should commit or roll back:

- **Is the failure a terminal business outcome?** (Insufficient funds, account frozen, KYC reject, daily-limit hit, fraud rule fired.) → **Commit.** Persist the outcome with a failure reason. Make the failure idempotent.
- **Is the failure transient and re-triable?** (Connection dropped, lock-timeout, optimistic-CC conflict, deadlock victim, OOM.) → **Rollback.** The attempt itself should be erased and re-done. The next attempt should see a clean slate.

The discriminator is *re-tryability*. A transient failure means "redo this and it might work." A business failure means "this is the answer; don't redo it."

Note how this maps onto HTTP status codes:

- Business failures → 4xx (the client did something the server has a permanent answer for). Idempotent.
- Transient failures → 5xx (the server might give a different answer next time). Re-triable.

The wallet repo's handler returns 200 with `state: "FAILED"` in the JSON body for insufficient funds, not a 4xx, because the *request* was processed successfully; it was the *transfer* that failed. The response distinguishes the two. (This is a taste call; some teams prefer 422 with the failure detail. The principle holds either way.)

## Counter-examples: when rollback is the right call

To balance the post, here are cases where rolling back on error is correct:

- **A `SELECT ... FOR UPDATE` returns a deadlock victim error.** The right move is rollback and retry; the transaction's view of the world was demonstrably inconsistent with another concurrent transaction. (Or — better — design the deadlock out, as in the first post in this series.)
- **A connection drop mid-transaction.** Nothing to commit; nothing to roll back; just observe the error and move on.
- **A serializable-isolation conflict (`40001`).** The transaction's read set was invalidated by another commit. Retry.
- **A panic in user code.** Rollback and re-panic so the request boundary catches it. The wallet repo's `RunInTx` does exactly this:

  ```go
  if p := recover(); p != nil {
      _ = tx.Rollback()
      panic(p)
  }
  ```

  The panic is re-raised so the HTTP middleware's `Recover` translates it to a 500. The state from the partial transaction does not commit.

The rule of thumb: rollback when the *attempt itself* was tainted (concurrency conflict, transient infrastructure failure, panic). Commit when the *attempt produced a definitive answer*, even if that answer is "we cannot do what you asked."

## The takeaway

The reflex to map "error" to "rollback" is wrong for any failure that should be idempotent. Idempotent failures need to commit, because the next request needs to see them.

The Go shape that supports this is two-return: `(result, err)`. The transaction layer commits on nil error, rolls back on non-nil. Your service logic gets to decide: by setting `result.State = FAILED` and returning nil, you opt into "persist this failure forever." By returning an error, you opt into "redo this from scratch."

That's the API. Use it deliberately.

If you found this series useful, the full repo — with the integration tests, the migrations, the load-test harness, and the README that ties it together — is at:

[github.com/PratikDhanave/wallet-transfer-assignment](https://github.com/PratikDhanave/wallet-transfer-assignment)

Follow for more posts on building money-moving systems in Go.
