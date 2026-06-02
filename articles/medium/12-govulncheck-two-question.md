# The Two-Question CVE Test: `govulncheck` Call-Graph Beats Dependabot Version-Match

Your security dashboard is lying to you in a polite, well-intentioned way. This post is the test that tells you which alerts to take seriously, and the workflow for ignoring the rest safely.

The setting: a Go wallet-transfer service backed by PostgreSQL 16. Stdlib `net/http`, `database/sql` + pgx, no framework, no ORM. Pinned to `go 1.24.0` (bumping pgx/v5 ≥ 5.9.0, OpenTelemetry ≥ v1.42.0, or testcontainers-go ≥ v0.41.0 would force the floor up to 1.25). govulncheck wired into a `make` target. Dependabot enabled. Public at [github.com/PratikDhanave/wallet-transfer-assignment](https://github.com/PratikDhanave/wallet-transfer-assignment).

Last week the Dependabot inbox had four open alerts: one critical, two high, one low. After a five-minute investigation per alert, all four were dismissed with documented justifications, zero deps were bumped, and the Go floor stayed at 1.24.0. The test is reproducible. This post is how.

## The Dependabot mechanism — and what it misses

Dependabot's signal is straightforward: scan `go.sum`, match each module against an advisory database, flag any version that falls inside an advisory's affected range. The signal is honest in what it says: "this module, in this version, has a known CVE."

What the signal does not say:

- **Whether you call the vulnerable function.** A CVE in `pgx/v5` does not mean every consumer of `pgx/v5` is exploitable. Most CVEs in a large module affect one symbol; if you don't call that symbol, you don't have the bug.
- **Whether the vulnerable code is in your build.** Go modules use build tags. A vulnerability that exists only in the BSD-specific code path is unreachable from a Linux-targeted binary.
- **Whether the runtime configuration triggers the path.** A CVE that needs `QueryExecModeSimpleProtocol` is unreachable from code that uses extended protocol with prepared statements.
- **Whether the deployment environment matters.** A CVE in a GitHub Action that requires Windows runners is unreachable from a workflow that uses `ubuntu-latest`.

These four gaps are where the noise lives. The vast majority of Dependabot alerts on a well-architected Go service are unreachable in one of those four ways. The vast majority of *fix-the-alert* PRs in such a service are dep bumps that satisfy the dashboard and change nothing about your security posture — often at the cost of dragging your Go floor forward or pulling in fresh transitive surface.

## `govulncheck` source mode — what it actually does

`govulncheck -mode=source ./...` walks the Go call graph from `main` outwards. It loads the vulnerability database (the same one that backs the Go advisory feed), maps each advisory to specific Go symbols (functions, methods, types), and reports only the advisories whose symbols are reachable from your code.

A sample run on this repo's main:

```
Scanning your code and 234 packages across 47 dependent modules for known vulnerabilities...

=== Symbol Results ===

No vulnerabilities found.

=== Package Results ===

No vulnerabilities found.

=== Module Results ===

Vulnerability #1: GO-2026-XXXX
    More info: https://pkg.go.dev/vuln/GO-2026-XXXX
  Module: github.com/jackc/pgx/v5
    Found in: github.com/jackc/pgx/v5@v5.7.1
    Fixed in: github.com/jackc/pgx/v5@v5.9.0

Your code is affected by 0 vulnerabilities.
This scan also found 0 vulnerabilities in packages you import and 1
vulnerability in modules you require, but your code doesn't appear to
call these vulnerabilities.
```

The last sentence is the one Dependabot does not give you: "modules you require, but your code doesn't appear to call these vulnerabilities." That is the call-graph result. The module is in your `go.sum`, the version matches the advisory, but you do not reach the affected symbol.

## The two-question test, formalised

For any incoming CVE alert (Dependabot, govulncheck, security-tracker email):

1. **Is the vulnerable symbol in our import set?** Grep the source tree for the symbol name. If nothing references it, the answer is "no" and we stop.
2. **If yes, do we reach it on any execution path?** Run `govulncheck -mode=source ./...` and read the output. If govulncheck lists the advisory under "modules you require, but your code doesn't appear to call these vulnerabilities," the answer is "no" and we stop.

Run question 1 first because it is the cheapest. A negative grep is dispositive — if the symbol literally does not appear in your source, there is no execution path that uses it. Question 2 is the calibration: if grep finds something but you are not sure whether the path is live (transitive call, conditional code, build-tag gate), let govulncheck walk the graph for you.

If the answer to either question is "yes" — the symbol is in your code AND govulncheck confirms it is reachable — the alert is real. Bump the dep, write a test that exercises the formerly vulnerable path (so a future regression catches it), and ship.

If the answer is "no" — and it usually is — dismiss with justification.

## Worked example: the four alerts

### Alert 1 — pgx critical, CVE-2026-33816

Vulnerable function: `pgproto3.Backend.ReceiveFunctionCall.Decode`.

Step 1 — grep:

```sh
grep -rn "pgproto3.Backend" .
# (no results)
grep -rn "ReceiveFunctionCall" .
# (no results)
```

`pgproto3.Backend` is the server-side parser of the Postgres wire protocol — used when you are *implementing* a Postgres server, not when you are connecting to one as a client. We do not import `pgproto3` directly; pgx pulls it in for its frontend (client) use. Our code is a Postgres client. No grep hits.

Step 2 — govulncheck:

```sh
make govulncheck
# ...
# This scan also found ... vulnerability in modules you require,
# but your code doesn't appear to call these vulnerabilities.
```

Both questions are "no." Dismissed with the justification: "Not reachable. We are a Postgres client; `pgproto3.Backend` is server-side. Confirmed by govulncheck source mode."

### Alert 2 — pgx low, CVE-2026-41889

Vulnerable function: `QueryExecModeSimpleProtocol`.

The simple protocol is pgx's legacy non-prepared-statement mode where parameters are interpolated into a single SQL string. Extended protocol (the default) uses prepared statements with `$N` placeholders — which is what [AGENTS.md §S-1](../../AGENTS.md) mandates for this service anyway.

Step 1 — grep:

```sh
grep -rn "QueryExecModeSimpleProtocol" .
# (no results)
grep -rn "QueryExecMode" .
# (no results — we use the default)
```

Step 2 — govulncheck: same "not reachable" line.

Dismissed: "Not reachable. We use the default extended protocol (prepared statements with `$N` placeholders), not the simple protocol. AGENTS.md §S-1 explicitly forbids the simple protocol pattern."

### Alert 3 — OpenTelemetry high, CVE-2026-39883

This one is more interesting because the answer to question 1 is initially "maybe." The vulnerable symbol exists in our import graph, but it is guarded by a BSD-only build tag (`//go:build freebsd || netbsd || openbsd`). Our deploy target is Linux.

Step 1 — grep finds the symbol. Inconclusive.

Step 2 — govulncheck on a Linux build:

```sh
GOOS=linux make govulncheck
# This scan also found ... in modules you require, but your code
# doesn't appear to call these vulnerabilities.
```

Build tags exclude the file from a Linux build, so the symbol is not in the binary, so govulncheck reports it as unreachable.

Dismissed: "Not reachable on our build target. Vulnerable code is gated by `//go:build freebsd || netbsd || openbsd`; we deploy on Linux. Confirmed by `GOOS=linux govulncheck`."

### Alert 4 — SonarSource action high, CVE-2025-59844

This one is not a Go module at all — it's a GitHub Action. The govulncheck test does not apply. Instead, we read the advisory and check the exploitation preconditions against our workflow.

The advisory says the vulnerability requires:
- A Windows runner (we use `ubuntu-latest`).
- A specific `args.input` value containing path traversal (we don't set `args.input`).

Our `sonarqube.yml`:

```yaml
jobs:
  sonarqube:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
      - name: SonarQube Scan
        uses: SonarSource/sonarqube-scan-action@vX.Y.Z
        with:
          # no args.input set
```

Both preconditions fail. Dismissed: "Not exploitable. Workflow runs on `ubuntu-latest`, not Windows; no `args.input` is set."

## The SECURITY.md note template

Every dismissal lands in SECURITY.md (or a `docs/security/dismissals.md` if your SECURITY.md is policy-focused). The template:

```md
### CVE-YYYY-NNNNN — <one-line summary>

- **Module / surface:** github.com/foo/bar @ v1.2.3 (or GitHub Action name + version)
- **Vulnerable symbol or precondition:** Func.Method / build-tag / runner OS / config value
- **Reachability:** Not reachable in this codebase. Evidence:
    - `grep -rn 'SymbolName' .` returns no hits.
    - `govulncheck -mode=source ./...` reports under "modules you require,
      but your code doesn't appear to call these vulnerabilities."
- **Decision:** Dismissed (date, dismissing reviewer).
- **Revisit if:** This codebase ever imports <package>, or we switch to <mode>,
  or we add <build-tag-relevant code>.
```

The "revisit if" clause is what turns a one-time dismissal into a durable decision. If a future PR adds the symbol, the dismissal is invalid and the alert reopens.

## The four anti-patterns to avoid

1. **Silent dismiss.** Dismissing an alert in the Dependabot UI without a written justification anywhere. Future you reads the closed alert and has no idea why. Reopens the conversation every time someone re-audits.

2. **Replace-directive silencing.** Using a `replace` directive in `go.mod` to swap the vulnerable module for a fork that has the same code without the advisory. Hides the symptom without changing reachability. If the vulnerable function is still called, you still have the bug. (If it's not, the original dismiss-with-justification path is correct and you don't need the replace.)

3. **Comment-out the import.** Removing the unused-anyway import to make govulncheck stop reporting the transitive surface. Sometimes legitimate (if the dep was genuinely unused, deleting it is correct), often a cosmetic fix that masks the real question.

4. **Bump to satisfy the dashboard.** Bumping a dep solely to clear the alert, with no understanding of the new transitive surface. The fix release for the pgx critical pulled in a newer pgconn that requires Go 1.25 — bumping just to clear the alert would have broken our Go floor for downstream consumers. The two-question test is what tells you whether the bump is worth that cost.

## When to bump anyway: the "fix is one line" case

The test exists to filter noise, not to refuse fixes. There are two cases where bumping is the right call even though the symbol is not currently reachable:

1. **The fix is a patch release with no transitive surface change.** If `pgx v5.7.1 → v5.7.2` fixes the CVE and changes nothing else, take it. The cost is zero and the future-you who is reading the closed alert thanks present-you.

2. **The reachability calculus is fragile.** If the symbol is *almost* reachable — one refactor away, or guarded by a feature flag that someone might flip — and the bump is cheap, take it. The two-question test is a useful filter, not a rule that forbids prudence.

What you *don't* do is bump a dep that drags your Go floor forward, or pulls in a new transitive module, or changes an API your code depends on, solely to clear an alert that the two-question test proved unreachable.

## CI integration

`govulncheck` belongs in CI. Our setup is a Make target that double-duties as the local pre-push gate and the CI step:

```makefile
# Source-level CVE reachability scan. Walks the import graph from main
# and reports only vulnerabilities your code can actually reach — much
# stricter than Dependabot, which flags any version match.
#
# Installs govulncheck on demand so contributors don't need to remember
# the `go install` line. `command -v` makes this idempotent.
.PHONY: govulncheck
govulncheck:
	@command -v govulncheck >/dev/null 2>&1 || \
	    { echo ">> installing govulncheck"; go install golang.org/x/vuln/cmd/govulncheck@latest; }
	govulncheck -mode=source ./...

# Composite gate that mirrors what CI checks plus the source-level vuln
# scan. Run this before `git push` to catch the same things CI will.
.PHONY: security-check
security-check: fmt-check
	go vet ./...
	go vet -tags=integration ./...
	$(MAKE) lint
	$(MAKE) lint-int
	$(MAKE) govulncheck
	@echo ">> security-check passed"
```

In CI, govulncheck runs on every push and PR. If it ever returns a non-empty *reachable* result, the build fails and the PR is blocked. The "modules you require, but your code doesn't appear to call" line is informational and does not fail the build.

This is the right asymmetry. Dependabot's noise is contained (it does not block anything); govulncheck's signal is enforced (it blocks merges). The two tools complement each other: Dependabot gives you the broad version-match view, govulncheck filters that view down to the call-graph reachable subset, and only the second one is allowed to stop a merge.

## The summary

Dependabot version-match is a useful starting signal and a terrible stopping criterion. The two-question test — grep for the symbol, then `govulncheck -mode=source ./...` — turns the noisy dashboard into a tractable inbox. Most alerts dismiss in five minutes with a written justification; the few that don't are real and deserve a thoughtful fix.

Three operational changes that pay for themselves quickly:

1. Add `make govulncheck` and `make security-check` Make targets. Run security-check before every push.
2. Wire govulncheck into CI as a required check. Block on *reachable* findings only.
3. Maintain a SECURITY.md (or `docs/security/dismissals.md`) with one entry per dismissal: CVE number, vulnerable symbol, reachability evidence, decision, revisit-if clause.

The combined effect: the security dashboard stops lying to you, the alerts you do see are the ones that matter, and the Go-floor pin survives the next noisy pgx CVE.

---

The repository is at [github.com/PratikDhanave/wallet-transfer-assignment](https://github.com/PratikDhanave/wallet-transfer-assignment). Follow for the next post in the series.
