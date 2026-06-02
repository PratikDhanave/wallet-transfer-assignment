Two goroutines, two wallets, transferring in opposite directions — and Postgres deadlocked.

The fix was one Go function and 4 lines of code.

Here's why most teams get this wrong.

---

The setup is the simplest one you can imagine in a wallet service. Two wallets, A and B. Two concurrent transfers: one from A to B, another from B to A. Both use `SELECT ... FOR UPDATE` to lock the source wallet before debiting. Both look correct in isolation.

In production, you'll see this in your logs:

  ERROR: deadlock detected
  SQLSTATE: 40P01

What happened: transfer 1 grabbed the row lock on A, then tried to lock B. Transfer 2 had already grabbed B and was waiting for A. Postgres' deadlock detector noticed the cycle and shot one of them.

The instinct is almost always wrong. The instinct is: "deadlocks are transient, just retry."

So you wrap the transaction in a retry loop. You back off. You add jitter. You call it done.

It's not done. You've turned a correctness problem into a throughput problem. Under load, every counter-direction pair pays a deadlock-detect tax (Postgres' default is 1 second) plus your retry, plus the work you did before getting killed. Your p99 latency spikes. Your throughput is whatever the deadlock rate lets it be.

The real fix is to design the deadlock out. Postgres deadlocks need a cycle in the lock graph. You break the cycle by always acquiring locks in the same global order.

In a wallet service, the order is "lock the wallet with the lexicographically smaller id first, then the other one." Both transfers — A→B and B→A — now want A first, then B. The second transfer waits for the first to commit. No cycle. No deadlock. Ever.

In Go, that's a one-line helper: `if a < b { return a, b } else { return b, a }`. Call it `orderedPair`. Use it before any `SELECT ... FOR UPDATE`. The transfer logic stays straight-line — you lock first, lock second, then read both rows back by their semantic role (source / destination) regardless of lock order.

Why does this work? Postgres deadlocks happen when transactions form a cycle in the wait-for graph. T1 holds A and wants B; T2 holds B and wants A. Cycle. Detector fires. With ordered acquisition, both transactions want A first. T2 simply queues behind T1 on the lock for A — a linear wait, not a cycle. The lock manager turns into a fair FIFO queue on hot rows. Throughput on contended wallets becomes bounded by transaction duration, which is the right thing to optimise.

The broader lesson: deadlocks are not a transient failure mode like a connection blip. They are a design defect. A retry loop hides the defect but pays for it forever in latency and capacity. A four-line function makes them structurally impossible.

This is the same shape as the rule "don't catch panics, fix the nil deref." Compensating control is not equivalent to elimination. If your only line of defence is a retry loop, you've decided to live with a known broken state forever.

The pattern generalises. Any time you take more than one lock per transaction — wallet pairs, user-and-account, parent-and-child rows — pick a total order and stick to it. The order doesn't have to be clever. Lexicographic on a stable id works in every codebase I've seen. The only requirement is that *every* code path that takes those locks uses the same order. Even one stray "lock from-wallet first because that's what the request mentions" reintroduces the cycle.

A useful tell when you're reading a financial codebase: if you see retry-on-deadlock, look at the lock acquisition order. Nine times out of ten the order is "whatever the request happens to mention first," and the retry loop is load-bearing infrastructure for a bug nobody named. Removing the retry without fixing the order takes down the next production deploy. Fixing the order makes the retry vestigial — and now you can delete it with a clean conscience.

There's a measurement that helps make this concrete. In the wallet repo, with `orderedPair` in place and counter-direction load running, the service sustains 533 transactions/second with p50 23ms, p95 83ms, p99 298ms — and zero deadlock errors in the Postgres log. The same load with the helper removed produces a steady stream of 40P01 errors and tail latency that blows past one second on any contended wallet.

Two takeaways:

1. The cheapest concurrency primitive is structural ordering. It's free at runtime, free at design time, and impossible to get subtly wrong if you put it in one helper.
2. Retry loops are a smell, not a feature. They belong as the last line of defence for genuinely transient failures (a connection that died mid-tx, a single-row lock-timeout), never as the primary handling of an avoidable conflict.

Have you been bitten by a counter-direction deadlock? What did your team do — retry loop, lock-order fix, or something else?
