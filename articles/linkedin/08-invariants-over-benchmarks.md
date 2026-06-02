Throughput numbers don't tell you if your wallet went negative under load.

A 1000-goroutine test that asserts `balance >= 0` does.

They are not the same test.

---

A Go benchmark measures one thing well: how many operations per second the code can do under a specific workload, with one or many goroutines, given the resources you provided. The output is a number — `nsec/op`, `B/op`, `allocs/op`. The number is useful for capacity planning, regression detection, and pprof targeting.

The number tells you nothing about correctness. A benchmark that runs at 10,000 ops/sec while silently double-spending is faster than one that runs at 5,000 ops/sec while correctly debiting every wallet. The throughput field on the benchmark output has no idea which one you shipped.

The thing that catches the double-spend is an invariant — a property that must hold after the workload regardless of how many ops succeeded or failed.

Four invariants for a wallet service:

1. **`balance >= 0`** for every wallet, always. Enforced by a Postgres CHECK constraint, but the test confirms no code path can bypass it.
2. **`sum(balances) == sum(initial balances)`** after any sequence of transfers. Conservation: money cannot be created or destroyed.
3. **For every transfer: exactly one DEBIT and one CREDIT** in the ledger, totalling zero net change across both wallets. Enforced by `UNIQUE (transfer_id, type)`.
4. **No leaked goroutines** after the workload completes. The pgx pool and the runtime should retire workers cleanly.

A 10-line invariant test, paraphrased from our stress suite:

  before := runtime.NumGoroutine()
  // run 200 transfers across 200 goroutines, wait for all
  runtime.GC(); time.Sleep(200 * time.Millisecond)
  after := runtime.NumGoroutine()
  if after - before > 10 {
      t.Fatalf("leak: before=%d after=%d", before, after)
  }

That's it. No latency measurement. No throughput number. The test passes if no goroutine leaks and fails if any do, including the boring background ones from a pool that wasn't sized properly.

We separate the two suites with build tags. Benchmarks live under `//go:build integration` and run as `make bench`. Stress tests with invariants live under `//go:build integration stress` and run as `make stress`. CI runs the integration suite on every push; stress only on demand or on a nightly schedule, because each stress test takes tens of seconds.

The other piece is `-race`. Stress tests run with `go test -race`. Race detection catches data races that benchmarks would never expose because benchmarks are usually low-contention by design. A stress test with 1,000 goroutines pounding the same wallet *and* `-race` enabled is the cheapest way to find a race in a concurrent data structure you didn't realise you wrote.

Our numbers: the many-wallets stress test runs 10,000 random transfers across 100 wallets through 50 worker goroutines at 533 transfers/second. The conservation invariant holds — sum of all balances equals 100,000 — every single time. The hot-wallet test fires 1,000 concurrent debits at a wallet seeded with exactly 10,000, where the math says exactly 100 must succeed. Exactly 100 do.

If you only ever ran the benchmark, you'd know the throughput. You wouldn't know whether it's the right throughput, or whether you have an off-by-one in the lock acquisition that double-spends once every thousand requests. The invariant test tells you. The benchmark cannot.

The cleanest split is: benchmarks for capacity, stress for correctness. Run both. Conflate neither.

What invariants do you assert under load?
