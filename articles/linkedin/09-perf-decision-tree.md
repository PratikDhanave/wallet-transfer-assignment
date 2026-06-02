Three tools. Three questions. Most teams reach for the wrong one.

Here's the decision tree we put on the team wiki.

---

The mistake I see most often in performance work is not the analysis — it is the first 30 seconds, when someone reaches for the tool they already know instead of the tool the question actually needs. A k6 run is not going to tell you which allocation is killing your GC. A pprof CPU profile is not going to tell you whether your p99 latency holds up at 300 RPS. A Go benchmark is not going to tell you whether your service collapses under real concurrent load.

So we wrote it down. On the team wiki, at the top of the perf page, four rows and three Make targets.

| Question | Tool | How we invoke it |
|---|---|---|
| Is this one function fast enough in isolation? | `testing.B` benchmarks | `make bench` |
| Does the service sustain its target RPS over a minute? | k6 HTTP load test | `make load` |
| Where is the time / memory / blocking actually going? | `pprof` (CPU, heap, goroutine, trace) | `make pprof-cpu`, `pprof-heap`, `pprof-goroutine`, `pprof-trace` |
| Does the invariant hold under hostile concurrency? | stress test | `make stress` |

Each row maps to a separate Makefile target so nobody has to remember the flag soup. `make bench` runs the integration-tagged Go benchmarks. `make load` runs the k6 scenario. The four `pprof-*` targets pull a profile from the admin listener and open the web UI on `:8081`.

---

Now the part that matters: when each one lies to you.

`testing.B` lies when you forget `b.ResetTimer()` after a setup. The seed work — wallet creation, service construction, fixtures — gets folded into the per-op time and the number you publish is wrong by a factor of two. It also lies when the test exercises a path that nobody hits in production. A sequential benchmark over a cold cache is interesting; it is not the same number you will see when 200 concurrent goroutines fight over the same wallet row.

Our actual numbers from `make bench` on a recent run: BenchmarkTransferCreate around 15.4 ms/op, GetTransfer around 988 µs/op, ListByWallet around 1.8 ms/op, IdempotentReplay around 2.0 ms/op. These get pasted into the README so they have a baseline to drift away from.

k6 lies when the scenario uses `ramping-vus` instead of `constant-arrival-rate`. A ramping-VU run measures whatever throughput the system can sustain — which is a tautology, because if the system slows down, k6 slows down with it, and the reported RPS shrinks to whatever the service can deliver. You want `constant-arrival-rate`: the load generator commits to sending N requests per second whether the service likes it or not, and you read p95 / p99 / error rate at that pressure.

Our load test sustains 297 RPS, p50 23 ms, p95 83 ms, p99 298 ms. The tail is dominated by DB connection pool saturation; you can move it by tuning `DB_MAX_OPEN_CONNS` and Postgres `max_connections`, not by changing the application code.

pprof lies when you pull a heap profile too early. The heap is sampled and the first few seconds of a server's life are dominated by package init and the migration runner, not by the steady-state workload. A 5-second pprof grab after `make run` will show you `golang-migrate` and `embed.FS`, not your hot path. Run real traffic for at least 30 seconds first, then grab the profile.

---

A one-paragraph confession for each tool, of the time I picked the wrong one.

**The benchmark sprint.** I once spent a week tuning a SQL query that BenchmarkGetTransfer said was the bottleneck. Got it from 1.4 ms/op down to 720 µs/op. Shipped. Service latency at the p95 did not move at all. The bottleneck was never in that function — it was in the JSON encoder downstream, which the benchmark did not exercise because the benchmark called the service directly. pprof of a 60-second load run would have shown me in 30 seconds what the benchmark hid for a week.

**The k6 sprint.** A different sprint, I built an elaborate k6 scenario to figure out whether refactoring the transfer state machine would hurt throughput. RPS came out flat. Conclusion: ship it. Two days later, integration tests went red because the refactor introduced a goroutine leak under retry. k6 saw the steady state and missed the failure-mode behaviour. The right tool was the stress test (`make stress`), which fires retries against contended wallets and checks for goroutine leaks at the end.

**The pprof sprint.** And one for pprof: spent half a day staring at a heap profile, convinced the per-request allocations were the problem. Heap was honest about what was allocated; what I missed was that the allocations were already gone — GC was reaping them at the next safepoint. The actual problem was blocking on the DB pool, which a goroutine profile makes obvious in five seconds (`make pprof-goroutine` and look at how many goroutines are parked in `sql.(*DB).conn`).

---

The lesson, written on the wiki right under the table: ask the question first, pick the tool second. The questions go in order — function isolation, sustained throughput, concrete hot spot, invariant under load — because that is the order in which a real perf investigation actually escalates. You only reach for pprof when bench tells you something is slow but you do not know what; you only reach for k6 when bench is clean but production is not; you only reach for stress when everything looks fine but you suspect a concurrency-only failure mode.

What's the perf tool you reach for first?
