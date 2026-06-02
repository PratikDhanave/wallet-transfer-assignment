If your insufficient-funds branch rolls back the transaction, your retries will succeed the moment the user gets funded.

That's almost certainly not what you want.

---

Here's the timeline that should keep you awake.

10:00:00 — User submits a transfer with idempotency key K. Source wallet has $5; transfer is $10. The service opens a tx, locks the wallets, reads the balance, decides "insufficient funds," rolls back.

10:00:01 — Client gets a 422. Application logs the failure. No row exists in the database for transfer K. No idempotency record either, because the rollback took everything with it.

10:00:30 — User tops up the wallet. Balance is now $50.

10:00:45 — Client's automatic retry layer fires the same request with the same idempotency key K. Service runs the fast-path replay: no record found. Slow path opens a tx, locks the wallets, reads the balance ($50), decides "sufficient funds," commits a $10 transfer.

The user has been debited for a transfer they were told had failed. The idempotency key did exactly nothing. The retry layer was correct. The handler was correct. The race was correct.

The bug is that a *terminal* outcome wasn't durable.

The fix is unintuitive but unambiguous: when the transfer fails for an in-business reason (insufficient funds, account frozen, daily limit hit), you must commit the FAILED row *along with* the idempotency record. The "rollback on error" reflex is wrong here. Insufficient funds isn't an *error*; it's a business outcome.

In the wallet repo, step 5 of executeTransfer does exactly this. The source balance check goes south. We update the transfer row to state FAILED with a failure_reason. We return nil from the tx callback — not an error — so RunInTx commits the FAILED transfer plus its already-inserted idempotency record. Same atomic unit as a successful PROCESSED.

The retry now does the right thing. Fast-path replay finds the idempotency record. Returns the same FAILED transfer. The user's second attempt observes the same outcome as the first. The wallet they topped up is not silently drained by a retry of a request the system already told them had failed.

The general pattern: any "fail" path that you want to be idempotent must commit. Rollback is for transient failures — connection drops, lock-timeout, panic — where the *attempt itself* should be erased and retried. Terminal outcomes belong in the database forever, errors and all.

The discriminator is re-tryability. A transient failure means "redo this and it might work." A business failure means "this is the answer; don't redo it." If a future retry could legitimately produce a different result (because some external state changed), the failure is terminal and must commit. If a future retry should produce the same result because the conditions haven't actually changed, the failure is transient and should rollback.

The mental flip is small but it inverts how you write transactional code. Errors aren't "the thing that triggers rollback." Errors are "an outcome to be persisted, like any other." Whether you commit or rollback is a question about durability, not about success. In Go, the cleanest expression of this is the two-return-value tx callback: returning `(result, nil)` opts into commit; returning `(nil, err)` opts into rollback. Your service code chooses which one explicitly, every time.

There's a corollary that bites people: if you log "transfer failed" and then rollback, you've written a log line that disagrees with the database. The log says one thing happened; the database says nothing happened. Reconciliation tooling, debugging sessions, support tickets — all of them will be confused for as long as the discrepancy exists. Committing the FAILED row makes the log and the database tell the same story.

Same shape applies to: payment declines (the card processor said no — that's an answer, persist it), fraud rejects (the rules engine fired — persist the decision so the user can't bypass it with a retry), KYC blocks (the identity check failed — persist so the next request doesn't burn another check call), daily-limit hits (the user hit their cap — persist so the retry returns the same answer rather than letting them try again).

In every case the rule is the same: if the next retry should observe the same outcome, the outcome must be on disk before the response goes out.

The shape this gives your service is worth describing. Every endpoint with side effects gets the same skeleton: validate, idempotency fast-path replay, open tx, do work, on terminal failure commit a failed-state row, on success commit a processed-state row, on transient failure rollback and surface to the caller. The terminal-state rows have a `failure_reason` column for the diagnostic detail. The state machine has explicit terminal states and explicit transitions; nothing about it is implicit in error handling.

The result is a service where every persistent outcome is observable. Support can answer "what happened to my transfer?" by selecting one row. Reconciliation can group transfers by state and reason without joining against logs. Customer-facing UIs can render a definitive answer. None of that is free without the discipline of committing failures.

What other "failure paths must commit" cases have you seen — payment declines, fraud rejects, regulatory blocks?
