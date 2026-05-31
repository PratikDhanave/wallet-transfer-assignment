---
name: wallet-perf
description: Use this skill when working on performance in this wallet-transfer repo — measuring latency, profiling CPU/heap/goroutines, running stress or load tests, comparing benchmark numbers across commits, or diagnosing a slow query / hot lock / DB pool exhaustion. Trigger when the user asks to "profile", "capture a CPU trace", "run pprof", "run the benchmarks", "run the load test", "is this slow", "where is the bottleneck", "tune the connection pool", "throughput", "RPS", "latency". Trigger especially when editing internal/db/db.go (pool tuning), internal/service/ (transaction-heavy paths), or loadtest/. Do NOT trigger for routine functional bug fixes that aren't perf-related, or for documentation-only edits.
---

# wallet-perf

This repo already has the perf scaffolding wired up. Use it before
guessing. Read [AGENTS.md §S-16](../../../AGENTS.md) (admin listener) and
the **Concurrency and scale** section of [README.md](../../../README.md)
first; the latter has the measured baseline numbers you'll be comparing
against.

If anything here conflicts with `AGENTS.md`, `AGENTS.md` wins.

---

## The four kinds of measurement, and when each fits

| Question | Tool | Make target |
|---|---|---|
| "How fast is a single Go function?" | `testing.B` benchmarks | `make bench` |
| "How does the service behave under a sustained HTTP rate?" | k6 | `make load` |
| "What does an in-process hot path look like?" | pprof CPU / heap / goroutine | `make pprof-*` |
| "Does the system correctness invariant hold at scale?" | stress tests (build-tagged) | `make stress` |

Pick by the question you have, not by which tool you like. A
benchmark won't tell you about lock contention across goroutines; k6
won't tell you which Go function is hot; pprof won't tell you the
RPS ceiling.

---

## Pattern A — service-layer benchmark

The benchmarks live in `internal/service/transfer_bench_test.go`
(build-tagged `integration`). They cover Create (sequential + parallel),
GetTransfer, ListByWallet, and idempotent replay.

```sh
DATABASE_URL=postgres://wallet:wallet@localhost:5432/wallet?sslmode=disable \
  make bench
# under the hood: go test -tags=integration -bench=. -benchmem
#                  -benchtime=3s -run='^$' -timeout=300s ./internal/service/...
```

Measured baseline (M1 Max, see README):

- `BenchmarkTransferCreate`        ≈ 15.4 ms/op
- `BenchmarkGetTransfer`           ≈ 988 µs/op
- `BenchmarkListByWallet`          ≈ 1.8 ms/op
- `BenchmarkIdempotentReplay`      ≈ 2.0 ms/op

When adding a new benchmark:

```go
//go:build integration

func BenchmarkXxx(b *testing.B) {
    testdb.Reset(b)                  // testing.TB, works for both T and B
    tsvc, wsvc := newTransferSvc(b)
    ctx := context.Background()
    seedWallets(b, wsvc, map[string]int64{"src": int64(b.N) * 1000, "dst": 0})

    b.ResetTimer()                   // discard setup time from the measurement
    for i := 0; i < b.N; i++ {
        _, err := tsvc.Create(ctx, service.CreateTransferRequest{
            IdempotencyKey: keyFor(i),  // unique per iteration
            FromWalletID:   "src",
            ToWalletID:     "dst",
            Amount:         1,
        })
        if err != nil {
            b.Fatal(err)
        }
    }
}
```

Rules:

- Seed enough balance to cover all `b.N` iterations or you'll start
  hitting "insufficient funds" partway through and measure the
  failure path.
- `b.ResetTimer()` after fixture setup. Otherwise the boot cost
  pollutes the per-op number.
- One unique `IdempotencyKey` per iteration unless you're explicitly
  benchmarking the replay path.
- Always commit a "before" run alongside an "after" run in PRs that
  change perf-relevant code.

---

## Pattern B — k6 HTTP load test

Lives in `loadtest/transfer.js`. Hits the running service over HTTP
with a constant-arrival-rate scenario.

```sh
# Terminal 1:
make db-up
make run                              # listens on :8080

# Terminal 2:
make load                             # k6 run loadtest/transfer.js
```

Override via env if you want to push harder:

```sh
RPS=500 DURATION=120s WALLETS=200 BASE_URL=http://localhost:8080 make load
```

Measured baseline:

- Sustained 297 RPS, 0% errors
- p50 23 ms, p95 83 ms, p99 298 ms

When the numbers drop noticeably:

1. Check `/metrics` — is the latency histogram p99 the same as k6
   reports? (If much lower, the slowdown is client-side / network.)
2. Capture a CPU profile during the run (Pattern C).
3. Check the DB pool counters in Postgres
   (`select * from pg_stat_activity`) — are we saturating
   `DB_MAX_OPEN_CONNS`?

---

## Pattern C — pprof on the live service

The admin listener (`DEBUG_ADDR`, loopback by default) exposes the
pprof endpoints. They are **never** reachable on the public listener
— see [AGENTS.md §S-16](../../../AGENTS.md) and
`cmd/server.TestRun_DebugListenerServesPprof`.

```sh
DEBUG_ADDR=127.0.0.1:6060 make run    # in one terminal
make load                             # generate load in another

# Heap snapshot:
make pprof-heap                       # opens go tool pprof web UI on :8081

# CPU profile (captures 30 s of CPU samples):
make pprof-cpu

# Goroutine dump:
make pprof-goroutine

# Execution trace (capture 5 s, open with `go tool trace`):
make pprof-trace
```

Inside the pprof web UI:

- **`top`** — flat / cumulative time per function. Read flat for "what
  is hot", cumulative for "what holds things up".
- **`web` / Graph view** — call graph with edge widths proportional
  to cumulative time. Best at-a-glance overview.
- **`source` view** — line-level annotation; click into a function to
  see which line allocated / used CPU.
- **`diff_base=<other.pb.gz>`** — compare two profiles. Use this to
  validate that an optimisation actually moved the needle.

---

## Pattern D — stress tests (correctness under load)

Different from benchmarks: these are gated by `//go:build integration stress`
and assert **invariants** under high goroutine count, not throughput.

```sh
DATABASE_URL=postgres://wallet:wallet@localhost:5432/wallet?sslmode=disable \
  make stress
# under the hood: go test -tags='integration stress' -race -count=1
#                  -timeout=600s -run='Stress|NoGoroutineLeak' -v ./internal/service/...
```

Coverage:

- `TestStress_HotWallet` — 1000 goroutines hammer the same wallet.
  Asserts: balance never goes negative, debits == credits, ledger
  always balances. Validates that the `FOR UPDATE` lock + the
  `orderedPair` lock-ordering work under real contention.
- `TestStress_ManyWallets` — 10000 transfers across 100 wallets.
  Measures sustained tx/s (≈ 533 on the reference box).
- `TestStress_NoGoroutineLeak` — uses `runtime.NumGoroutine()` before /
  after to assert we don't leak per-request goroutines.

When adding a new stress test:

```go
//go:build integration && stress

func TestStress_<scenario>(t *testing.T) {
    testdb.Reset(t)
    tsvc, wsvc := newTransferSvc(t)
    // ... seed, fan out goroutines, wg.Wait() ...

    // INVARIANTS — these are the point of the test.
    src, _ := wsvc.Get(ctx, "src")
    dst, _ := wsvc.Get(ctx, "dst")
    if src.Balance < 0 { t.Fatalf("source went negative: %d", src.Balance) }
    if src.Balance+dst.Balance != startingBalance {
        t.Fatalf("ledger not conserved: src=%d dst=%d", src.Balance, dst.Balance)
    }
}
```

Run with `-race` always. A successful stress test that produces a race
report is still a failure.

---

## DB pool tuning

The pool is env-configurable from `cmd/server` via
`internal/db.PoolConfig` (see commit `3cc2c40`):

| Env var | Default | What to tune |
|---|---|---|
| `DB_MAX_OPEN_CONNS` | 25 | Raise in lock-step with Postgres `max_connections`. Below ≈ 8, throughput collapses. Above the Postgres cap, you get "too many clients" (SQLSTATE 53300). |
| `DB_MAX_IDLE_CONNS` | 5 | Keeps that many warm between bursts; reduces first-request latency. |
| `DB_CONN_MAX_LIFETIME` | 5m | Recycle a connection after this age. Set lower than any upstream LB / pgbouncer idle cut. |
| `DB_CONN_MAX_IDLE_TIME` | 1m | Close a connection after this idle gap. |

The test-side pool in `internal/testdb` is bounded to `MaxOpenConns=25`
on purpose: with 1000-goroutine stress tests, an unbounded pool would
exhaust `max_connections=100` on the compose Postgres and surface as
`pq: sorry, too many clients already`. Don't remove that cap.

---

## Anti-patterns

| Pattern | Why it's wrong | Fix |
|---|---|---|
| Benchmarking through `time.Now()` instead of `testing.B` | No statistical control; one-shot numbers vary by 2-3× run to run | Use `go test -bench` with `-benchtime` and `-count` |
| Capturing pprof on a single request | Profiles are statistical; one request rarely shows the hot path | Generate sustained load (`make load`) while capturing |
| Comparing perf across commits without a `-diff_base` profile | Eyeballing two flame graphs is unreliable | `go tool pprof -diff_base=before.pb.gz after.pb.gz` |
| Increasing `DB_MAX_OPEN_CONNS` without raising Postgres `max_connections` | Hits SQLSTATE 53300 under bursts | Tune both sides together; the docker-compose Postgres uses the default 100 |
| Adding a sleep / poll to "wait for the goroutines" | Race-prone; flakiness varies by CPU load | Use `sync.WaitGroup` or `errgroup` |
| Exposing pprof on the public listener | Attackers can request a 30 s CPU profile and crater latency | Always on the admin listener (loopback default) — guarded by `cmd/server.TestRun_DebugListenerServesPprof` |
| Forgetting `-race` on stress tests | A passing stress test that has a hidden race is worse than a failing one — false confidence | Always `-race`, even though it's 2-10× slower |

---

## Verification after a perf change

```sh
make bench                            # before/after numbers in PR description
make stress                           # invariants still hold under load
make security-check                   # the change didn't introduce a new CVE
```

If the change touches the DB or pool, also rerun `make load` and paste
the new RPS / p95 / p99 numbers into the README's
**Concurrency and scale** section, replacing the previous baseline.
