# Concurrency Safety as an Invariant Test, Not a Benchmark

Run the benchmark, get a number, ship. That's how production bugs are born.

This post is about the parallel test suite that catches what benchmarks can't — invariant-asserting stress tests that fire hundreds or thousands of concurrent goroutines and verify that the *answer* is right, not just that the answer arrived quickly.

The examples are from a Go wallet-transfer service. The patterns apply to any concurrent system where correctness under contention is the actual product.

---

## The benchmark fallacy

A Go benchmark looks like this:

```go
func BenchmarkTransferService_Create(b *testing.B) {
    testdb.Reset(b)
    tsvc, wsvc := newTransferSvcB(b)
    ctx := context.Background()
    mustSeedB(b, wsvc, "src", int64(b.N)*100)
    mustSeedB(b, wsvc, "dst", 0)

    b.ReportAllocs()
    b.ResetTimer()
    for i := 0; i < b.N; i++ {
        _, err := tsvc.Create(ctx, service.CreateTransferRequest{
            IdempotencyKey: "bench-seq-" + strconv.Itoa(i),
            FromWalletID:   "src",
            ToWalletID:     "dst",
            Amount:         1,
        })
        if err != nil {
            b.Fatalf("create: %v", err)
        }
    }
}
```

The output of `go test -bench=. -benchmem`:

```
BenchmarkTransferService_Create-8       1234    876543 ns/op    2456 B/op    42 allocs/op
```

That number — 876543 nanoseconds per operation, ~1140 ops/sec sequential — is useful. It's a baseline. It tells you what your hot path costs. It catches regressions when someone adds an accidental allocation.

It does not tell you whether the transfers are *correct*. The benchmark calls `b.Fatalf` only if `Create` returns a Go error. A code path that returns nil while quietly double-spending passes the benchmark with flying colours.

This is the benchmark fallacy: throughput is necessary for capacity planning, and entirely insufficient for correctness. The first cell of every benchmark spreadsheet is `ns/op`. There is no "did the answer come out right" column.

## The four invariants for a wallet service

Before you can write an invariant test, you need to enumerate what an invariant is. For a wallet/transfer system, four things must always be true after any sequence of operations:

**Invariant 1: balance >= 0 for every wallet.**

A wallet cannot go negative. The schema enforces this with `CHECK (balance >= 0)`. The service enforces it with `SELECT ... FOR UPDATE` followed by an arithmetic check. The invariant test confirms that no concurrent execution sequence violates either layer.

**Invariant 2: conservation — sum of all balances equals sum of initial balances.**

Money cannot be created or destroyed by the service. If you seed 100 wallets with 1,000 each (total 100,000) and run any number of transfers between them, the sum of all balances at the end must still be 100,000. A double-credit, a missing debit, or a lost-update bug breaks this invariant.

**Invariant 3: every transfer produces exactly one DEBIT and one CREDIT.**

The double-entry ledger requires that every transfer be balanced. The schema enforces this with `UNIQUE (transfer_id, type)` — a duplicate DEBIT or CREDIT for the same transfer is rejected. The invariant test confirms that the service never accidentally inserts an unbalanced pair.

**Invariant 4: no leaked goroutines.**

After the workload completes and the runtime has had a moment to settle, the goroutine count should be within a small slack of the pre-workload count. A leak — a worker that never exits, a context that never cancels — would show up here.

These invariants compose. Invariant 2 is implied by Invariant 1 (no negative balances) plus Invariant 3 (every transfer balances). But asserting them independently catches bugs faster, because the failure message tells you which invariant broke and points at the layer.

## The hot-wallet stress test

The first invariant test fires 1,000 concurrent debits at a single wallet seeded with 10,000. The math says exactly 100 must succeed (10,000 / 100 per transfer = 100). The other 900 must fail cleanly.

```go
//go:build integration stress

func TestStress_HotWallet(t *testing.T) {
    testdb.Reset(t)
    tsvc, wsvc := newTransferSvc(t)
    ctx := context.Background()

    const (
        startingBalance int64 = 10_000
        perTransfer     int64 = 100
        numGoroutines         = 1_000
    )
    seedWallets(t, wsvc, map[string]int64{"hot": startingBalance, "sink": 0})

    var (
        processed, failed atomic.Int64
        wg                sync.WaitGroup
    )
    start := time.Now()
    wg.Add(numGoroutines)
    for i := 0; i < numGoroutines; i++ {
        go func(i int) {
            defer wg.Done()
            tr, err := tsvc.Create(ctx, service.CreateTransferRequest{
                IdempotencyKey: "hot-" + strconv.Itoa(i),
                FromWalletID:   "hot",
                ToWalletID:     "sink",
                Amount:         perTransfer,
            })
            if err != nil {
                t.Errorf("transfer %d: %v", i, err)
                return
            }
            switch tr.State {
            case domain.StateProcessed:
                processed.Add(1)
            case domain.StateFailed:
                failed.Add(1)
            default:
                t.Errorf("transfer %d unexpected state %s", i, tr.State)
            }
        }(i)
    }
    wg.Wait()
    dur := time.Since(start)

    src, _ := wsvc.Get(ctx, "hot")
    dst, _ := wsvc.Get(ctx, "sink")

    if src.Balance < 0 {
        t.Fatalf("source went negative: %d", src.Balance)
    }
    if src.Balance+dst.Balance != startingBalance {
        t.Fatalf("ledger not conserved: src=%d dst=%d (sum=%d, want %d)",
            src.Balance, dst.Balance, src.Balance+dst.Balance, startingBalance)
    }
    wantProcessed := startingBalance / perTransfer
    if processed.Load() != wantProcessed {
        t.Fatalf("processed: got %d want %d", processed.Load(), wantProcessed)
    }
    if processed.Load()+failed.Load() != int64(numGoroutines) {
        t.Fatalf("processed=%d failed=%d total=%d want %d",
            processed.Load(), failed.Load(),
            processed.Load()+failed.Load(), numGoroutines)
    }

    t.Logf("stress=hot_wallet goroutines=%d processed=%d failed=%d elapsed=%s rate=%.0f/s",
        numGoroutines, processed.Load(), failed.Load(), dur,
        float64(numGoroutines)/dur.Seconds())
}
```

Five assertions, each tied to one invariant:

1. `src.Balance < 0` — Invariant 1.
2. `src.Balance + dst.Balance != startingBalance` — Invariant 2 (conservation between the two wallets in this test).
3. `processed.Load() != wantProcessed` — exact count of successes.
4. `processed.Load() + failed.Load() != numGoroutines` — no transfer left in PENDING; every goroutine terminated cleanly.
5. The `default` switch case — no transfer ended up in some other state.

If the code has a double-spend bug, `src` goes negative or the sum exceeds 10,000. If the lock ordering is wrong and you deadlock, the `wg.Wait()` hangs or some goroutines error out. If the idempotency record commits but the transfer doesn't, the counts don't match the goroutine count.

No latency assertion. No throughput threshold. Throughput is logged for context (`rate=%.0f/s`) but never compared. The test passes or fails based on correctness, not speed.

## The many-wallets stress test

The hot-wallet test exercises one specific contention pattern. The many-wallets test exercises the realistic mix: 100 wallets, 10,000 random transfers between random pairs, 50 worker goroutines.

```go
func TestStress_ManyWallets(t *testing.T) {
    testdb.Reset(t)
    tsvc, wsvc := newTransferSvc(t)
    ctx := context.Background()

    const (
        numWallets        = 100
        perWalletBalance  = int64(1_000)
        numTransfers      = 10_000
        concurrentWorkers = 50
        transferAmountMin = int64(1)
        transferAmountMax = int64(50)
    )
    totalInitial := int64(numWallets) * perWalletBalance

    // Seed N wallets.
    seed := make(map[string]int64, numWallets)
    for i := 0; i < numWallets; i++ {
        seed[fmt.Sprintf("w%03d", i)] = perWalletBalance
    }
    seedWallets(t, wsvc, seed)

    // ... build 10,000 jobs with random (from, to, amount), distribute across 50 workers ...

    // Sum every wallet's balance — must equal the original total
    // regardless of how many transfers landed as PROCESSED vs
    // FAILED. This is the conservation invariant.
    var sum int64
    for id := range seed {
        w, err := wsvc.Get(ctx, id)
        if err != nil {
            t.Fatalf("get %s: %v", id, err)
        }
        if w.Balance < 0 {
            t.Fatalf("wallet %s went negative: %d", id, w.Balance)
        }
        sum += w.Balance
    }
    if sum != totalInitial {
        t.Fatalf("conservation broken: sum=%d want %d (delta=%d)",
            sum, totalInitial, sum-totalInitial)
    }
}
```

The headline assertion is `sum != totalInitial`. After 10,000 random transfers, the sum of all wallet balances must equal 100,000 exactly. Not approximately. Exactly.

This is where `int64` minor units pays off (see the previous post in this series). The conservation check is exact integer equality. With `float64`, you'd need an epsilon, and the epsilon would grow with N, and a growing epsilon means the test cannot actually fail.

Measured rate on this test: 533 transfers per second. The conservation invariant holds every run.

## Why `-race` is mandatory

Run the stress tests with `-race`. Always:

```bash
go test -race -tags='integration stress' -count=1 -timeout=600s \
    -run='Stress|NoGoroutineLeak' ./internal/service/...
```

`-race` instruments every memory access. When two goroutines access the same memory without synchronisation, and at least one is a write, `-race` reports a data race and fails the test.

Benchmarks rarely catch races because benchmarks are typically low-contention by design — you want clean numbers, not pathological cases. Stress tests are pathological cases on purpose. The combination of 1,000 goroutines fighting over the same wallet *and* `-race` enabled is the cheapest way to find a race in a concurrent data structure you didn't realise you wrote.

We don't run the integration suite with `-race` because of performance — but we always run stress with it.

## The goroutine leak test

Invariant 4 (no leaked goroutines) gets its own test because it's the easiest to forget and the hardest to spot in production.

```go
func TestStress_NoGoroutineLeak(t *testing.T) {
    testdb.Reset(t)
    tsvc, wsvc := newTransferSvc(t)
    ctx := context.Background()

    // Settle the runtime: give whatever was running time to exit.
    runtime.GC()
    time.Sleep(50 * time.Millisecond)
    before := runtime.NumGoroutine()

    seedWallets(t, wsvc, map[string]int64{"src": 5_000, "dst": 0})

    const numGoroutines = 200
    var wg sync.WaitGroup
    wg.Add(numGoroutines)
    for i := 0; i < numGoroutines; i++ {
        go func(i int) {
            defer wg.Done()
            _, _ = tsvc.Create(ctx, service.CreateTransferRequest{
                IdempotencyKey: "leak-" + strconv.Itoa(i),
                FromWalletID:   "src",
                ToWalletID:     "dst",
                Amount:         1,
            })
        }(i)
    }
    wg.Wait()

    // Give the runtime + pgx pool a moment to retire any work
    // goroutines spawned during the test.
    runtime.GC()
    time.Sleep(200 * time.Millisecond)
    after := runtime.NumGoroutine()

    // Allow a small upward drift; pgx maintains pool worker
    // goroutines that may scale up. 10 is generous.
    const slack = 10
    if after-before > slack {
        t.Fatalf("possible goroutine leak: before=%d after=%d (delta=%d > slack %d)",
            before, after, after-before, slack)
    }
    t.Logf("goroutine count: before=%d after=%d delta=%d",
        before, after, after-before)
}
```

The pattern is straightforward: snapshot `runtime.NumGoroutine()` before and after, allow a small slack for the runtime and pgx pool to scale, fail if the delta exceeds the slack. The `GC` + `time.Sleep` between the workload and the after-snapshot gives the runtime a moment to retire goroutines whose work just finished.

A real leak — a context that never cancels, a worker that loops on a closed channel, a missing `wg.Done()` — shows up as a delta hundreds of goroutines large. A spurious leak from pgx scaling its pool shows up as 1–5. The slack of 10 is generous enough to ignore pool churn and tight enough to catch real leaks.

In production, you'd hit the same leak after a few hours of traffic. The test catches it in 200 milliseconds.

## CI integration: build tag separation

The build tags do the work of separating which suite runs when:

- `//go:build integration` — integration tests that need a database. Run on every push via `make test-int`.
- `//go:build integration stress` — stress tests that take tens of seconds. Run via `make stress`, on demand or nightly.

```go
//go:build integration

// in transfer_bench_test.go and the regular integration tests
```

```go
//go:build integration stress

// in stress_integration_test.go
```

The Makefile encodes the matching commands:

```makefile
test-int:
    go test -tags=integration -p 1 -race -count=1 ./...

bench:
    go test -tags=integration -bench=. -benchmem -benchtime=5s \
        -run='^$$' ./internal/service/...

stress:
    go test -race -tags='integration stress' -count=1 -timeout=600s \
        -run='Stress|NoGoroutineLeak' ./internal/service/...
```

Three commands, three purposes:

- `test-int` — fast integration suite, runs on every push. Catches the typical bugs.
- `bench` — capacity measurement, runs on demand. Catches throughput regressions.
- `stress` — invariant verification under contention, runs on demand or nightly. Catches the concurrency bugs that benchmarks can't see.

Mixing them confuses the signal. A stress test that fails at 800 transfers/second instead of 533 looks like a regression but isn't (you've just changed test machine, or `-race` overhead, or both). A benchmark that runs at the same throughput but produces wrong answers looks fine but isn't.

## Measured baseline

From our actual stress runs:

- **TestStress_HotWallet**: 1,000 concurrent debits, exactly 100 succeed, conservation holds. Elapsed varies with hardware (~2-4 seconds local, ~3-5 seconds CI).
- **TestStress_ManyWallets**: 10,000 random transfers, 50 workers, 533 transfers/second, conservation holds across all 100 wallets.
- **TestStress_NoGoroutineLeak**: 200 transfers, delta ≤ 10 goroutines after settle.

The 533 number is the headline. It's not the maximum throughput the service can do — k6 against the HTTP surface sustains 297 RPS with much higher per-request overhead. It's the throughput at which our many-wallets concurrency pattern saturates the wallet lock + idempotency PK on a single local Postgres. The number is useful as a regression detector. The conservation assertion is what makes it a *test*.

## The relationship to chaos engineering

Invariant tests are chaos engineering for the small. Chaos engineering at scale injects faults at the infrastructure layer (kill a node, partition the network, throttle disk) and asserts that the application's invariants survive. Invariant stress tests inject contention at the application layer (fight over the same wallet, race on the same idempotency key) and assert that the storage invariants survive.

The technique is the same: define the property that must hold, perturb the system, check the property. The only difference is the layer of perturbation.

A production chaos exercise might assert "no customer balance disagrees with the ledger sum." A local stress test asserts the same thing in 30 seconds with a SQL TRUNCATE between runs. The stress version catches the bugs before they ship; the chaos version catches them in production. Both are useful. They're not substitutes.

## The summary

- Benchmarks measure speed. Invariants measure correctness. Conflating them ships bugs.
- Four wallet invariants: balance ≥ 0, conservation, balanced ledger entries, no goroutine leaks.
- Build tag the stress suite separately (`//go:build integration stress`) so it doesn't run on every push.
- Always run stress with `-race`. The combination of high contention and the race detector is the cheapest way to find concurrency bugs.
- Log throughput for context; assert correctness for the test result.

The benchmark gives you a number. The invariant test tells you whether the number means anything.

---

Source code: [github.com/PratikDhanave/wallet-transfer-assignment](https://github.com/PratikDhanave/wallet-transfer-assignment)

Follow for the next post in this series — a perf-question decision tree: `make bench` to k6 to pprof, when to reach for each, and what each one is actually answering.
