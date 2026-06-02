If your idempotency check is "SELECT WHERE key = ?, and if missing, INSERT" — you have a race.

The pattern that fixes it is older than your ORM. And it doesn't need a distributed lock, a Redis SETNX, or a saga.

It's a primary key.

---

Here's the race, in slow motion.

Two requests arrive with the same idempotency key, milliseconds apart. Both run the SELECT. Both see no record. Both decide they are the first writer. Both execute the side effect. Now you have two transfers, two ledger entries each, and one very confused user.

The reflex fixes are all worse than the problem. A "check, sleep, check again" loop just shrinks the window. A mutex on the application instance breaks the moment you scale to two pods. A Redis distributed lock adds a second source of truth that can disagree with your database under partition.

The honest fix is to push the serialization point into the database, on a primitive the database already provides for free: a primary key.

Make the idempotency key the PK of an `idempotency_records` table. Insert it as part of the same transaction that performs the side effect. When two requests race, both transactions try to insert. Postgres takes a lock on the unique index for the key value. One transaction wins and commits; the other blocks until the winner is done, then receives SQLSTATE 23505 (unique_violation). On the loser path, you roll back the entire transaction — undoing any side-effects — and read back the winner's row to return the same response.

In Go with pgx, the code reduces to checking `pgErr.Code == "23505"` and mapping it to an in-process `ErrIdempotencyExists`. The service layer's race-loss handler then calls a fast-path replay (the simple `SELECT ... WHERE key = ?`) which is now guaranteed to find the row, because Postgres only surfaces the unique-violation *after* the winner has committed.

Three things make this pattern good:

One — the database is already adjudicating the race. You're not adding infrastructure; you're naming a guarantee that's already on disk. There's no Redis to run out of memory, no Zookeeper to operate, no consensus protocol that breaks during a network partition. The b-tree lock on the unique index is the same primitive that's been holding the line on UNIQUE constraints since the 1970s.

Two — the response the loser returns is the *exact* same response the winner returned. Not "similar." Not "equivalent." Same row, same id, same state. The user cannot tell which goroutine got the lucky timing. This is what idempotency at the API level actually means: the side effect happens once, and every observer sees the same outcome.

Three — it composes with everything else in the transaction. Insufficient funds, validation failures, write errors — they all roll back together. There's no half-committed state where the side effect happened but the idempotency record didn't (or vice versa). When the loser's transaction rolls back, all of its work — the transfer row, the ledger entries, the balance updates — is gone too. The winner's transaction is the only thing visible on disk. The loser then re-reads and returns the winner's outcome.

A subtle detail worth calling out: the race-loss replay must happen *after* the rollback. Postgres only surfaces SQLSTATE 23505 to the loser after the winner has committed (because until then the winner's row isn't visible to anyone). That means by the time your Go code sees the error code, the winner's record is guaranteed visible to any subsequent read — even a non-transactional one. The replay can be a cheap `SELECT` outside any tx; you don't need to re-open the transaction just to read.

The pattern has a name in database literature: "insert-or-read." It's also the same pattern that drives outbox tables, exactly-once message processing, and most modern queue consumer implementations. Once you see it, you'll spot it everywhere.

The anti-pattern to watch out for: storing the idempotency record in a *separate* transaction from the side effect "for performance." That breaks the guarantee. The two writes can fail independently and you'll have either orphaned records (record committed, side effect rolled back) or non-idempotent retries (side effect committed, record rolled back) — both are worse than the original race. If you find yourself doing this for performance reasons, the actual bottleneck is almost always somewhere else; profile first.

Another anti-pattern: hashing the entire HTTP body to detect "different request, same key." Whitespace and JSON key order shouldn't count as a different request. Hash the *semantic* tuple — for a transfer, `from|to|amount` — and you'll dodge a long tail of "the same Python dict serialises differently on Tuesdays" support tickets.

What's your idempotency pattern? Distributed lock, PK-based, or something more exotic?
