# The Go Floor Cascade: How One Dependabot PR Almost Forced Us Off Go 1.24

This is a debugging story. It starts with a security patch, ends with deleting testcontainers, and the lesson is that Go modules have a subtle invariant most teams don't track.

The repo in question is a Go financial service — a wallet-transfer API backed by PostgreSQL — that is deliberately pinned to `go 1.24.0`. A few weeks ago a routine dep bump for `github.com/jackc/pgx/v5` quietly rewrote `go.mod` to `go 1.25.0`. CI on the 1.24 runner failed. Two days of work later, we'd downgraded pgx, downgraded OpenTelemetry, ripped out `testcontainers-go` entirely, and learned more than we wanted to about how `go mod tidy` actually computes the directive.

This post is the writeup. The repo: [github.com/PratikDhanave/wallet-transfer-assignment](https://github.com/PratikDhanave/wallet-transfer-assignment).

## The trigger

A Dependabot PR landed:

```
- github.com/jackc/pgx/v5 v5.8.0
+ github.com/jackc/pgx/v5 v5.9.0
```

`go mod tidy` ran. The diff that actually shipped:

```
- go 1.24.0
+ go 1.25.0
- github.com/jackc/pgx/v5 v5.8.0
+ github.com/jackc/pgx/v5 v5.9.0
```

That second line was unintended. CI failed on the 1.24 runner with the familiar message:

```
go: github.com/jackc/pgx/v5@v5.9.0 requires go >= 1.25.0
note: language version used to build … is lower than the version required
```

The reflex is to bump the runner. We did not. The `go 1.24.0` floor was a deliberate decision (contributor onboarding, base image, distro packaging — that's a separate post). The right move was to back the dep out and understand why the bump cascaded.

## What `go mod tidy` actually writes

The `go` directive in `go.mod` is the **minimum** language version that can compile the module. `go mod tidy` writes it as the maximum of:

- The current `go` directive in your `go.mod`.
- The `go` directive in every selected module's own `go.mod`.

"Every selected module" includes transitive deps. And — this is the subtle bit — it includes the test-time deps of those deps, when those test deps are pulled in to satisfy `go mod tidy`'s completeness rules.

So when pgx 5.9.0 declares `go 1.25.0` in its own `go.mod`, the math becomes `max(1.24.0, 1.25.0) = 1.25.0` and the file is rewritten. There is no warning. There is no flag to suppress. The directive moves.

## Tracing the chain with `go mod graph` and `go mod why`

The first investigation question: which module is actually forcing the bump? `go mod graph` dumps every edge in the module graph; piping through grep narrows it to a single module's requesters:

```sh
go mod graph | grep "jackc/pgx/v5@" | sort -u
```

`go mod why` answers the converse — why is module X in our graph at all:

```sh
go mod why -m github.com/jackc/pgx/v5
# github.com/PratikDhanave/wallet-transfer-assignment/internal/repository
# github.com/jackc/pgx/v5
```

The script I keep around for a fast scan of who declares what floor:

```sh
for m in $(go list -m all | awk 'NR>1 {print $1"@"$2}'); do
  floor=$(grep -E "^go " "$(go env GOMODCACHE)/$m/go.mod" 2>/dev/null)
  case "$floor" in *1.25*|*1.26*|*1.27*) echo "$m -> $floor" ;; esac
done
```

That loop is uglier than it should be — Go really wants a `go mod` subcommand here — but it does the job. Run it after every `go mod tidy` rewrite and the offender is the first line of output.

## The discovery that `replace` doesn't lower the floor

The first thing we tried was a `replace` directive: take pgx 5.9.0's code but lie about its floor. It does not work. The Go toolchain reads the `go` directive from the **originally required** version, not from the replacement target. `replace` swaps code at link time; it does not rewrite module metadata.

The same is true if you fork a module and rewrite its go.mod locally — `replace` will use your fork's code, but the toolchain still consults the original module path to compute the floor. There is no escape hatch at the `replace` layer.

## The testcontainers-go cascade

The pgx fix was easy: pin at v5.8.0, accept that the 5.9.0 security patch (a minor logging fix) was not worth the floor bump, document it in a project skill so the next agent doesn't repeat the dance.

testcontainers-go was harder. We were using it in the integration test suite to spin up an ephemeral PostgreSQL container per test run. v0.41.0 was the latest, and it brought along a chain of test-time deps:

```
testcontainers-go -> docker/docker/client
docker/docker/client.test -> grpc-gateway
grpc-gateway -> google.golang.org/genproto
google.golang.org/genproto -> golang.org/x/text v0.36
golang.org/x/text v0.36 declares go 1.25.0
```

That fifth line was the trap. `x/text` 0.36 needs 1.25 in its own `go.mod`. The chain reached us through a test-binary dep three levels removed from anything we directly imported. Pinning testcontainers at v0.40.0 worked temporarily; the next minor of any link in the chain would re-trigger it.

The structural fix was to delete testcontainers entirely. We replaced it with a tiny `internal/testdb` package that reads `DATABASE_URL` from the env and either uses the configured Postgres or skips the test. Locally, `docker compose up -d postgres` provides the DB; in CI, GitHub Actions' `postgres:16-alpine` service container provides it. The integration suite still runs in the same `go test -tags=integration ./...` invocation. The dep chain is gone.

The shape of the replacement package, abridged:

```go
//go:build integration

package testdb

const envDatabaseURL = "DATABASE_URL"

var (
    once      sync.Once
    sharedDB  *sql.DB
    sharedErr error
)

func Get(tb testing.TB) *sql.DB {
    tb.Helper()
    once.Do(func() { sharedDB, sharedErr = start() })
    if sharedErr == errNoDSN {
        tb.Skipf("integration tests require %s; skipping", envDatabaseURL)
    }
    if sharedErr != nil {
        tb.Fatalf("open postgres: %v", sharedErr)
    }
    return sharedDB
}
```

The test-time dep weight went from "all of testcontainers, all of docker/docker, all of grpc-gateway" to "the standard library plus our existing pgx pin." The integration suite startup time dropped by a few seconds per process. The floor stayed at 1.24.0.

## The dep version table

After two days of this we wrote down what we'd learned so the next dep bump doesn't relitigate it. The table lives in a project skill (`.claude/skills/wallet-deps/SKILL.md`) so any agent run picks it up automatically. The relevant rows:

| Dep | Last 1.24-compatible | First version that forces 1.25 |
|---|---|---|
| `github.com/jackc/pgx/v5` | v5.8.0 | v5.9.0 |
| `go.opentelemetry.io/otel` (+ sdk/trace/metric/stdouttrace) | v1.41.0 | v1.42.0 |
| `go.opentelemetry.io/contrib/.../otelhttp` | v0.66.0 | v0.67.0 |
| `github.com/testcontainers/testcontainers-go` | v0.40.0 | v0.41.0 |
| `github.com/open-policy-agent/opa` | v1.10.0 (`go 1.24.6`) | v1.15.0 |
| `golang.org/x/sys`, `x/text`, `x/sync`, `x/net` | varies — see go.mod | latest minors force 1.25 |

The table is not exhaustive and the values drift; this is what the dep ecosystem looked like at the time we pinned. The point isn't the specific versions; the point is having any table at all. Without it, every Dependabot PR is a fresh investigation.

## The make-gate that catches future regressions

The directive only matters if it stays where you put it. We added a one-line check to the `make` target that runs in every pre-push hook and every CI job:

```make
.PHONY: floor-check
floor-check:
	@grep -q '^go 1.24' go.mod || { echo "Go floor has moved off 1.24"; exit 1; }

.PHONY: security-check
security-check: fmt-check floor-check
	$(MAKE) govulncheck
	@echo ">> security-check passed"
```

Three lines. Catches the next surprise bump the moment it lands. A real implementation might compare against a more specific version, or parse the directive with a tool instead of grep, but the principle is: the floor is a tracked invariant, not a side effect of `go mod tidy`.

The corresponding govulncheck target is what makes the gate useful instead of merely strict — it runs the reachability scan in `-mode=source`, which evaluates whether a CVE's vulnerable symbol is actually reachable from our code, not just present in the dep tree. The combination is what lets us hold an older Go floor without ignoring security signal.

## The philosophical bit: Go's two-major support policy and what pinning costs

Go's security team backports stdlib fixes to the **two most recent majors**. At time of writing those are 1.25 and 1.26. The 1.24 series no longer receives stdlib patches. `govulncheck` against our pinned 1.24.0 toolchain reports the TLS KeyUpdate DoS, x509 name-constraint bypass, and a handful of other stdlib issues as reachable from our code paths.

That is a real cost of pinning. It does not go away.

The honest answer is to separate two things:

- The `go` directive in `go.mod` is the **minimum language version contributors can compile with**. Pinning this low is a compatibility statement: "anyone with a 1.24 toolchain can build this."
- The **toolchain used to actually build production binaries** is a separate decision. If you add `toolchain go1.25.<patch>` to `go.mod`, the floor stays at 1.24 for contributor compatibility, but `go build` auto-downloads the patched toolchain for the actual binary that ships.

We have not enabled the `toolchain` directive yet — that's the next PR on the list. The point for this post is that it's a real escape hatch: you can keep an older floor for contributor compatibility while still building production with a supported toolchain. It's not in `go.mod` because we wanted to make the tradeoff explicit, not because it isn't an option.

## What to take from this

Three rules that would have saved us the two days:

One, treat the `go` directive as a tracked invariant. Add a one-line grep to your pre-push gate. The moment `go mod tidy` moves it, you want to know.

Two, before merging any dep bump, run the loop that finds modules declaring a higher floor. It's twelve lines of shell. Run it. The output is the offender.

Three, when you find that the bump you want has a floor higher than yours, it is a floor bump in disguise. Treat it as such: its own PR, its own review, its own update to the contributor docs and the CI runner version. Don't smuggle it in under a security patch.

The Go module system is honest about what it does. The trap is that "what it does" includes reading transitive go.mod directives you've never heard of and rewriting the line in your file that controls who can build your code. Knowing that is the entire fix.

---

If you found this useful, follow for the next post in the series — on how `make security-check` plus `govulncheck -mode=source` lets you hold an old Go floor without ignoring security signal.

Repo: [github.com/PratikDhanave/wallet-transfer-assignment](https://github.com/PratikDhanave/wallet-transfer-assignment)
