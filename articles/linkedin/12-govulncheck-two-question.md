We had 4 open Dependabot alerts. 1 critical, 2 high, 1 low.

All 4 are unreachable from our code. Bumping the deps would have cost us our Go version pin.

Here's the 5-minute test that proved it.

---

Dependabot is great at one thing: telling you that a module in your `go.sum` has a CVE attached to a version range that includes your version. It is honest about that signal. It is also a blunt instrument, because "the dependency is vulnerable" and "your code is exploitable" are different statements, and the gap between them is where most CVE alerts actually sit.

On the wallet-transfer service, the inbox last week looked like this:

- pgx critical — CVE-2026-33816
- pgx low — CVE-2026-41889
- OpenTelemetry high — CVE-2026-39883
- SonarSource action high — CVE-2025-59844

Four alerts. None reachable. All dismissed with documented justifications. Total investigation time: under five minutes per alert, because the test is the same every time.

The two-question test:

1. **Is the vulnerable symbol in our import set?** Grep our source tree for the symbol name. If nothing reads or calls it, the answer is no and we stop.
2. **If yes, do we reach it on any execution path?** Run `govulncheck -mode=source ./...`. Source mode walks the call graph from `main` and reports only vulnerabilities your code can actually reach. If govulncheck says "modules you require, but your code doesn't appear to call these vulnerabilities," the answer is no and we stop.

Run question 1 first because it is the fastest. A negative answer from grep is dispositive — if the symbol is not in your code, there is no execution path. Question 2 only comes up when grep finds something and you need to verify whether the path is actually live.

Walking each alert.

**pgx critical, CVE-2026-33816.** Vulnerable function is `pgproto3.Backend.ReceiveFunctionCall.Decode`. `Backend` is the server-side parser of the Postgres wire protocol — used when you are *implementing* a Postgres server, not when you connect to one as a client. We are a Go service connecting to PostgreSQL 16. We use `pgproto3` indirectly through pgx but only ever as a *frontend* (client). Grep for `pgproto3.Backend` in our tree: zero hits. govulncheck source mode: not reachable. Dismissed.

**pgx low, CVE-2026-41889.** Vulnerable function is `QueryExecModeSimpleProtocol`. This is the legacy non-prepared-statement mode where pgx interpolates parameters into a single SQL string. We use the default (extended protocol with prepared statements and `$N` placeholders), which is what AGENTS.md §S-1 mandates anyway. Grep for `QueryExecModeSimpleProtocol`: zero hits. govulncheck: not reachable. Dismissed.

**OpenTelemetry high, CVE-2026-39883.** Vulnerable path is gated by a BSD-only build tag. We deploy on Linux. The vulnerable code is not in our compiled binary because the build constraint excludes it on Linux. govulncheck reports it as unreachable on our build target. Dismissed.

**SonarSource action high, CVE-2025-59844.** This one is a GitHub Action, not a Go module. The vulnerable path requires Windows runners and a specific argument shape. Our `sonarqube.yml` workflow runs on `ubuntu-latest` with no args.input set. Not exploitable on our runner. Dismissed.

Four alerts, four dismissals with justifications written into the SECURITY.md file. Total dep bumps: zero. Go version floor unchanged.

Why does the floor matter? Bumping any of those four would force a transitive upgrade. The pgx critical-fix release pulls in a newer pgconn that requires Go 1.25. So does the OpenTelemetry fix. Our `go.mod` declares `go 1.24.0` and we have downstream consumers who haven't moved yet. Spending a sprint to fix unreachable CVEs by breaking the Go floor for our consumers is a bad trade.

The other reason the test matters: it tells you when to take the alert seriously. If grep finds the symbol, or govulncheck reports it as reachable, the conversation is completely different — that is an actionable security finding, not a dashboard nuisance. The two-question test is what separates the two cases in five minutes instead of half a day.

There is a process question hiding here. Who signs off on a dismissal? Our rule: a dismissal is not silence. Every dismissal lands in SECURITY.md with the CVE number, the vulnerable function, the evidence from grep + govulncheck, and the date. If the alert reopens in a future scan, the SECURITY.md note is the receipt. Dismiss-and-document, never dismiss-and-forget.

If you want one operational change to make from this post: add a `make govulncheck` target that runs `govulncheck -mode=source ./...` and a `make security-check` that gates `fmt + vet + lint + govulncheck`. Run security-check before every push. The CI mirror is automatic.

How many of your Dependabot alerts are reachable?
