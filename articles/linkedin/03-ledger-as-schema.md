"Every transfer must have exactly one DEBIT and one CREDIT."

That sentence is in every finance team's runbook, on every ledger-system design doc, in every code review comment.

It's also a constraint Postgres can enforce in three words.

UNIQUE (transfer_id, type).

---

Most ledger bugs in production don't look like "we double-wrote the credit." They look like "we wrote one of the two." A retry hits an error after the DEBIT and before the CREDIT. A panic in a defer. A timeout that cancels the second insert but lets the first commit. The ledger silently goes out of balance by exactly the transfer amount, and nobody notices until reconciliation runs the next morning.

You can try to defend against this in code. Wrap both inserts in the same tx. Add a metric. Add a daily reconciliation job. Write a runbook for the on-call. Add a code review checklist. All useful — and all *application-layer* compensations for a property that should be impossible to violate.

A better answer: tell the schema what "valid" means.

The wallet repo's `ledger_entries` table has one line that does the work:

  UNIQUE (transfer_id, type)

Combined with `type CHECK (type IN ('DEBIT','CREDIT'))`, this is a complete promise. For any given transfer id there can be at most one DEBIT row and at most one CREDIT row. The application code can attempt to insert a second DEBIT all day; Postgres will reject it with SQLSTATE 23505 (unique_violation). The bug becomes uncommittable.

Add `CHECK (amount > 0)` and `CHECK (from_wallet_id <> to_wallet_id)` and you've also made "zero-value transfer" and "self-transfer" structurally impossible. Three constraints, three classes of bug eliminated at the storage layer.

This is what people mean by "make wrong states unrepresentable." The schema is a type system. Every CHECK and UNIQUE is a refinement type. Every wrong state you push down into the schema is one fewer thing your test suite needs to cover, one fewer thing your code review needs to catch, one fewer 3am page.

The economics are excellent. A constraint is a test that runs on every write, in every environment, against every client, written once and maintained for the lifetime of the table. Compare to an application-level guard: it runs only on the code path that calls it, only in the language that ships with it, only as long as nobody refactors it away. The schema is the one place where defensive code outlives the developer who wrote it.

The wallet repo leans into this. The `wallets` table has `CHECK (balance >= 0)` — defence-in-depth against any future code path that updates a balance without first verifying under a row lock. The `transfers` table has `CHECK (from_wallet_id <> to_wallet_id)` — self-transfers are forbidden at the storage layer, so no batch loader or admin script can produce one by accident. The `transfers.state` column has `CHECK (state IN ('PENDING','PROCESSED','FAILED'))` — a typo in a state literal becomes a test failure instead of a stuck row.

Five constraints, five classes of bug gone. Each one was a single line of DDL.

The discipline isn't "put everything in the schema." Cross-table invariants (DEBIT sum equals CREDIT sum across all entries for a transfer) belong in the application or in periodic reconciliation. The right call is to push down anything that can be expressed as a row-level or local-table constraint, and accept that the rest needs a runtime guard or a daily reconcile job. The two together are layered defence; neither alone is enough.

A useful question to ask in design review: "what would Postgres need to know in order to refuse the bad write?" Often the answer is a five-word DDL change. Sometimes it's "Postgres can't know — this is a cross-table invariant." Either answer is useful; the question forces you to be clear about which kind of invariant you're dealing with.

One more thing worth noting: schema constraints fail *loudly*. SQLSTATE 23505, SQLSTATE 23514. The error includes the constraint name. Your logs tell you exactly which invariant got violated, on which row. Compare that to an application-level guard that silently early-returns and emits a metric you forgot to alert on. Loud failures are debuggable; quiet failures are incidents.

There's also a migration discipline worth adopting: every time you add a column whose values come from a fixed set, ask whether it deserves a `CHECK` or a foreign key into a lookup table. Every time you add a relationship between two tables, ask whether it deserves a foreign key with the right ON DELETE behaviour. Every time you add a column that should never be NULL in valid data, add `NOT NULL`. These are 20-second decisions that pay back for the entire life of the table.

What's the smartest constraint you've seen catch a bug at write time?
