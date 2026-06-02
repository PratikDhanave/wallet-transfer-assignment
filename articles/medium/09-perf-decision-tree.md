# From `make bench` to k6 to `pprof`: a Perf-Question Decision Tree

The reason most perf investigations get nowhere is the first 30 seconds: we picked the wrong tool. This is the decision tree, with code, that we ended up living by.

The setting: a Go wallet-transfer service backed by PostgreSQL 16. Stdlib `net/http`, `database/sql` + pgx, no framework, no ORM. Strict layered architecture (handler → service → repository → DB) with `domain` as a leaf package. The service moves money, so correctness is non-negotiable; throughput is the next concern after that. The repository is at [github.com/PratikDhanave/wallet-transfer-assignment](https://github.com/PratikDhanave/wallet-transfer-assignment).

We have four perf tools wired in: Go benchmarks, k6, pprof, and a "stress" tag for goroutine-heavy invariant tests. Each one is right for exactly one kind of question and wrong for the others. The point of this post is the decision tree — and the code — that we put on the team wiki so nobody has to relearn it.

## The four questions

Before any tool comes out of its case, answer:

1. **Is this one function fast enough in isolation?** → Go benchmarks.
2. **Does the service sustain its target RPS over a minute?** → k6.
3. **Where is the time, memory, or blocking actually going?** → pprof (CPU / heap / goroutine / trace).
4. **Does the invariant hold under hostile concurrency?** → stress tests.

Then look up the matching Make target. The targets exist precisely so the decision tree is mechanical:

```sh
make bench           # Go benchmarks via testing.B
make load            # k6 against a running `make run`
make pprof-cpu       # 30s CPU profile, opens web UI on :8081
make pprof-heap      # in-use heap, same web UI
make pprof-goroutine # goroutine dump
make pprof-trace     # 5s execution trace
make stress          # integration + stress build tags, ~35s
```

The rest of this post walks each layer, including the discipline that keeps the tool from lying to you.

## Layer 1 — Go benchmarks: function isolation

The cheapest perf signal. A `testing.B` benchmark loops your function `b.N` times and reports ns/op, B/op, allocs/op. Three lines of discipline make the difference between a useful number and a misleading one:

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

Three things to notice:

1. **`b.ResetTimer()` after setup.** The seed work, service construction, and `testdb.Reset` are all expensive. If you leave the timer running through them, your per-op number is whatever those one-time costs amortise to divided by `b.N`, and that is not the number you want.
2. **`b.ReportAllocs()` is free intelligence.** Allocations per op tell you whether GC pressure is going to bite you under load. A function that takes 1 ms/op with 200 allocs/op will look fine in isolation and ugly in production at 300 RPS.
3. **Distinct keys per iteration.** If the benchmark hits the fast-path replay (same idempotency key), you are measuring SELECT throughput, not money-movement throughput. Per-iteration keys force the slow path.

Our current baselines (pasted into the README so they have something to drift away from):

- `BenchmarkTransferCreate` — ~15.4 ms/op
- `BenchmarkGetTransfer` — ~988 µs/op
- `BenchmarkListByWallet` — ~1.8 ms/op
- `BenchmarkIdempotentReplay` — ~2.0 ms/op

What a benchmark **cannot** tell you: whether the service still hits those numbers under concurrent load, whether some other layer dominates the request, whether the DB connection pool is the wall.

### When benchmarks lie

- **Cold cache effects.** First iteration reads a page from disk; subsequent iterations hit the buffer cache. Use `-benchtime=3s` or longer so the warm-cache regime dominates.
- **Setup leaks into timing.** `b.ResetTimer()` is the fix.
- **Path mismatch.** A benchmark calls one function; production calls the whole stack. A "fast" function does not imply a fast endpoint. Check the next layer.

## Layer 2 — k6: sustained RPS at the HTTP boundary

Once individual functions are fast enough in isolation, the question shifts: what does the system do when the load arrives over a wire at a constant rate?

The only k6 scenario we use is `constant-arrival-rate`. Variants like `ramping-vus` are useful for finding the breaking point but they confuse the measurement: if the system slows down, the load shrinks with it, and the reported "RPS" is whatever the service was willing to accept, not the load you intended to apply. With `constant-arrival-rate`, k6 commits to N requests per second whether the service likes it or not. Read p50 / p95 / p99 / error rate at that pressure.

```javascript
export const options = {
  scenarios: {
    sustained: {
      executor: 'constant-arrival-rate',
      rate: TARGET_RPS,
      timeUnit: '1s',
      duration: DURATION,
      // Generous VU pool so a single slow response doesn't backpressure
      // the iteration rate. k6 only spins up as many as needed.
      preAllocatedVUs: Math.max(50, TARGET_RPS / 5),
      maxVUs: Math.max(200, TARGET_RPS),
    },
  },
  thresholds: {
    'http_req_duration{expected_response:true}': [
      'p(50)<30',
      'p(95)<120',
      'p(99)<400',
    ],
    'http_req_failed': ['rate<0.01'],
    'transfer_processed': ['count>0'],
  },
};
```

Two more things make k6 actually useful for this service:

```javascript
// Unique idempotency key per iteration so every request hits the
// slow path. (VU id, iteration) pair guarantees uniqueness.
const key = `loadtest-${__VU}-${__ITER}-${Date.now()}`;

// Pick a random pair to spread lock contention. Hot-wallet contention
// is its own benchmark; here we want sustained HTTP throughput.
const pair = data.wallets[Math.floor(Math.random() * data.wallets.length)];
```

Sustained run results from a recent `make load`:

- **RPS sustained:** 297 (target was 300)
- **p50:** 23 ms
- **p95:** 83 ms
- **p99:** 298 ms
- **Error rate:** < 0.1 %

That p99 is dominated by DB connection pool saturation. Verified by `make pprof-goroutine` mid-run — goroutines parked in `database/sql.(*DB).conn` waiting for a free connection. The fix is tuning `DB_MAX_OPEN_CONNS` and Postgres `max_connections`, not application code.

### When k6 lies

- **Ramping VUs instead of constant arrival rate.** Reports throughput the service is willing to give, not the load you intended.
- **Same idempotency key per request.** You are measuring replay path throughput, which is much cheaper than the slow path.
- **Single hot wallet.** You are measuring lock contention. Useful — but it is a different number than HTTP throughput, and you should know which one you are looking at.

## Layer 3 — pprof: where the time / memory / blocking actually goes

pprof answers "k6 said the p99 is 300 ms — where in the request does it go?" or "bench said allocations are high — what is allocating?"

Four flavours, all served from the admin listener (loopback, opt-in, never on the public mux — see [AGENTS.md §S-16](../../AGENTS.md)):

```sh
make pprof-cpu        # CPU profile, 30 seconds
make pprof-heap       # in-use heap
make pprof-goroutine  # snapshot of every goroutine and its stack
make pprof-trace      # 5s execution trace -> go tool trace
```

The targets resolve to:

```makefile
pprof-cpu:
    go tool pprof -http=:8081 http://$(DEBUG_ADDR)/debug/pprof/profile?seconds=30

pprof-heap:
    go tool pprof -http=:8081 http://$(DEBUG_ADDR)/debug/pprof/heap

pprof-goroutine:
    go tool pprof -http=:8081 http://$(DEBUG_ADDR)/debug/pprof/goroutine

pprof-trace:
    curl -s -o trace.out http://$(DEBUG_ADDR)/debug/pprof/trace?seconds=5 && go tool trace trace.out
```

What each one is good for:

- **CPU profile.** Use during a load run (`make load` in another terminal). Tells you what fraction of CPU time goes where. If your service is CPU-bound, this is the first stop.
- **Heap profile.** Use after the service has been running for at least 30 seconds under load — earlier than that and you see migration runner + package init, not steady state. Tells you what is allocated right now.
- **Goroutine profile.** Use when latency looks weird but CPU is idle. If the goroutine count is much higher than expected, or most goroutines are parked in the same `Sleep` / `Lock` / `conn` call, you have your answer.
- **Execution trace.** The most expensive to interpret but the highest-resolution. Shows scheduler events, GC pauses, syscall blocks, channel sends. Use when nothing else tells you a coherent story.

### When pprof lies

- **Heap snapshot too early.** Migration + init dominate; the steady state is not yet established.
- **CPU profile during idle.** If `make load` is not running, the CPU profile shows what `runtime.gopark` is doing. Surprise: nothing.
- **Goroutine profile in the middle of a GC cycle.** Some goroutines are temporarily parked for GC; they look like a leak. Take two snapshots a few seconds apart and compare.

## Layer 4 — stress tests: invariants under load

Benchmarks measure speed. k6 measures throughput. pprof measures where things go. None of them answer "does the invariant hold when 1000 goroutines hammer the same wallet at the same time?"

That is what the `stress` build tag is for:

```makefile
stress:
    DATABASE_URL=$(DATABASE_URL) \
      go test -tags='integration stress' -race -count=1 -timeout=600s \
        -run='Stress|NoGoroutineLeak' -v ./internal/service/...
```

A stress test does not care about ns/op. It cares about: did the source wallet ever go negative? Was the sum of balances conserved across all transfers? Are there any goroutines still alive at the end that should have exited? Did the race detector flag anything?

The test structure mirrors the concurrency template from `wallet-integration-test`:

```go
const (
    startingBalance int64 = 1000
    perTransfer     int64 = 100
    numTransfers          = 20
)
seedWallets(t, wsvc, map[string]int64{"src": startingBalance, "dst": 0})

var wg sync.WaitGroup
var processed, failed int64
wg.Add(numTransfers)
for i := 0; i < numTransfers; i++ {
    go func(i int) {
        defer wg.Done()
        tr, err := tsvc.Create(ctx, service.CreateTransferRequest{
            IdempotencyKey: keyFor(i),
            FromWalletID:   "src",
            ToWalletID:     "dst",
            Amount:         perTransfer,
        })
        // ... atomic.AddInt64 on processed / failed by tr.State ...
    }(i)
}
wg.Wait()

src, _ := wsvc.Get(ctx, "src")
dst, _ := wsvc.Get(ctx, "dst")

if src.Balance < 0 {
    t.Fatalf("source went negative: %d", src.Balance)
}
if src.Balance+dst.Balance != startingBalance {
    t.Fatalf("ledger not conserved: src=%d dst=%d", src.Balance, dst.Balance)
}
```

This is a perf tool the same way a fire-alarm test is a perf tool. It does not measure throughput. It verifies that nothing breaks at throughput. Both numbers matter.

## Composing them in a real investigation

Worked example — "the p99 has crept up from 200 ms to 400 ms over the last week."

1. **k6 first** to confirm the regression is real and reproducible: `make load`. Reproduced, p99 is 410 ms.
2. **pprof CPU during the run**: `make pprof-cpu`. Nothing dominant; CPU is mostly idle. So the time is not in compute.
3. **pprof goroutine during the run**: `make pprof-goroutine`. Half the goroutines are parked in `database/sql.(*DB).conn`. So the time is in pool wait.
4. **Confirm with a benchmark**: `make bench`. Bench numbers unchanged. So the function itself is fast; the regression is in pool sizing or concurrency.
5. **Fix**: tune `DB_MAX_OPEN_CONNS`. Re-run `make load`. p99 back to 280 ms.

Without the decision tree, the temptation is to stare at the benchmark numbers (which look fine), or refactor the SQL (which is already fast). With the tree, the second pprof grab tells you the answer in under a minute.

## The always-update-the-baseline-in-README rule

The single highest-leverage habit in perf work is keeping the baseline numbers somewhere git tracks. We paste them at the top of the README:

```
Bench (make bench):
  BenchmarkTransferCreate    ~15.4 ms/op
  BenchmarkGetTransfer       ~988 µs/op
  BenchmarkListByWallet      ~1.8 ms/op
  BenchmarkIdempotentReplay  ~2.0 ms/op

Load (make load, 60s @ 300 RPS):
  Sustained 297 RPS, p50 23 ms, p95 83 ms, p99 298 ms, errors <0.1%
```

If a PR changes the bench numbers, the README diff makes the change visible at review time. If a PR changes the load numbers, the same. The act of writing the number into a file makes the perf review part of normal review.

## Tooling summary

| Tool | Make target | Best question |
|---|---|---|
| `testing.B` | `make bench` | Is this function fast enough in isolation? |
| k6 | `make load` | Does the service hold its target RPS? |
| pprof CPU | `make pprof-cpu` | What is using the CPU? |
| pprof heap | `make pprof-heap` | What is allocated right now? |
| pprof goroutine | `make pprof-goroutine` | What are the goroutines blocked on? |
| pprof trace | `make pprof-trace` | What does the scheduler see? |
| stress | `make stress` | Do invariants hold under hostile load? |

Pick the question first. The tool follows.

---

The repository is at [github.com/PratikDhanave/wallet-transfer-assignment](https://github.com/PratikDhanave/wallet-transfer-assignment). Follow for the next post in the series.
