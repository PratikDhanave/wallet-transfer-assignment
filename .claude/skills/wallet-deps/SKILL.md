---
name: wallet-deps
description: Use this skill when changing Go module dependencies in this wallet-transfer repo — adding a new import, bumping a version, downgrading to escape a transitive constraint, removing a dep, or interpreting `go mod tidy` rewrites. Trigger when the user asks to "bump pgx", "upgrade OpenTelemetry", "add <package>", "go mod tidy", "why did go.mod change", "fix the Go version", or when a Dependabot/Renovate PR is being reviewed. Trigger especially when editing go.mod / go.sum, when running `go get`, when CI fails with "language version used to build … is lower than", or when triaging a vulnerability that wants a dep bump (see also `wallet-vuln-triage`). Do NOT trigger for code-only changes that don't add an import, for pure stdlib usage, or for documentation-only edits.
---

# wallet-deps

The Go floor in this repo is **`go 1.24.0`** by deliberate choice — see
[AGENTS.md §0](../../../AGENTS.md). Every dep change must preserve that
floor. This skill is the workflow for keeping it that way without losing
the ability to take security patches.

If anything here conflicts with `AGENTS.md`, `AGENTS.md` wins.

---

## How a single dep can force the Go floor up

`go mod tidy` writes `go <X.Y.Z>` as the **maximum** of:

- The current `go` directive in `go.mod`.
- The `go` directive in every selected module's own `go.mod` —
  including transitive deps and test-time deps of deps.

So bumping `pgx/v5 v5.8.0 → v5.9.2` doesn't just upgrade pgx — it
upgrades **our** Go floor to `1.25.0` because pgx 5.9.x declares
`go 1.25.0`. Same trap for `go.opentelemetry.io/otel ≥ v1.42` and
`testcontainers-go ≥ v0.41`.

The constraint chains we already discovered (do not forget):

| Dep | Last 1.24-compatible | First version that forces 1.25 |
|---|---|---|
| `github.com/jackc/pgx/v5` | v5.8.0 | v5.9.0 |
| `go.opentelemetry.io/otel` (+ sdk/trace/metric/stdouttrace) | v1.41.0 | v1.42.0 |
| `go.opentelemetry.io/contrib/.../otelhttp` | v0.66.0 | v0.67.0 |
| `github.com/testcontainers/testcontainers-go` | v0.40.0 | v0.41.0 — and pulls grpc-gateway + genproto chains that also need 1.25 |
| `golang.org/x/sys`, `x/text`, `x/sync`, `x/net` | varies — see go.mod | latest minors force 1.25 |
| `github.com/open-policy-agent/opa` | v1.10.0 (`go 1.24.6`) | v1.15.0 |

We currently do **not** depend on testcontainers-go (replaced by the
`DATABASE_URL`-backed `internal/testdb`).

---

## Template A — add a new dep

```sh
# 1. Add the dep + tidy.
go get github.com/example/foo@v1.2.3
go mod tidy

# 2. Check that the go directive did NOT move.
grep "^go " go.mod   # MUST still say "go 1.24.0"

# 3. If it moved, find the offender and pick a lower version.
go list -m all | head -30  # look for any module declaring go 1.25+
for m in $(go list -m all | awk 'NR>1 {print $1"@"$2}'); do
  floor=$(grep -E "^go " "$(go env GOMODCACHE)/$m/go.mod" 2>/dev/null)
  case "$floor" in *1.25*|*1.26*|*1.27*) echo "$m -> $floor" ;; esac
done

# 4. If the new dep itself is the offender, pick the latest version
#    whose go.mod still declares `go 1.24.x`.
go list -m -versions github.com/example/foo | tr ' ' '\n' | tail -20
go mod download github.com/example/foo@<candidate>
grep -E "^go " "$(go env GOMODCACHE)/github.com/example/foo@<candidate>/go.mod"

# 5. Re-tidy with the chosen version and confirm the floor.
go get github.com/example/foo@<candidate>
go mod tidy
grep "^go " go.mod

# 6. Run the full gate.
make security-check
```

If no version of the new dep is 1.24-compatible, the change is a
floor-bump in disguise — escalate per "Template D" below.

---

## Template B — bump an existing dep

```sh
# 1. Check whether the patch you want is on the 1.24-side of the line.
go list -m -versions <module> | tr ' ' '\n' | tail -10
for v in v.X.Y v.X.Z; do
  go mod download "<module>@$v" 2>/dev/null
  printf "%s -> " "$v"
  grep -E "^go " "$(go env GOMODCACHE)/<module>@$v/go.mod"
done

# 2. If the desired version is 1.24-compatible, just bump.
go get <module>@<desired>
go mod tidy
grep "^go " go.mod                      # must stay at 1.24.0
make security-check
```

If the desired version forces `go 1.25.0`, you have three real options:

1. **Hold at the older version** if the bump is not security-driven.
2. **Bump and accept the floor change** — but only with the user's
   explicit consent, since they chose `go 1.24.0` deliberately. Open a
   PR that documents *why* the floor moves and updates AGENTS.md §0
   and the README "Prerequisites" line together.
3. **Find a backport** if the upstream maintains an LTS branch for the
   prior major (rare in Go ecosystem; common for k8s.io and OTel).

---

## Template C — interpret a `go mod tidy` rewrite

When `go mod tidy` rewrites go.mod / go.sum and you're not sure why:

```sh
# Show every module's "why am I here" path back to our module.
go mod why -m <module>

# Show the highest version of every requester for a module.
go mod graph | grep "<module>@" | sort -u
```

Common surprises:

- **A test-time dep of a dep is in our graph.** Example: testcontainers
  pulled `docker/docker/client.test`, which imported `grpc-gateway`,
  which pulled `x/text` 0.36 (requires 1.25). We solved this by
  removing testcontainers entirely (see `internal/testdb`).
- **A `replace` directive doesn't lower the floor.** The Go toolchain
  reads the `go` directive from the *originally required* version, not
  the replacement. `replace` swaps code at link time, not metadata.
- **`go get -u` is too aggressive.** Avoid in this repo. Prefer named
  bumps so you know exactly what changed.

---

## Template D — escalate a floor change

A floor change is not a routine bump. Procedure:

1. Open a PR that **only** changes go.mod / go.sum / go-version. No
   feature code mixed in.
2. In the PR description, state the trigger explicitly: which dep,
   which version, why we can't stay on the prior minor.
3. Update in the same PR:
   - `go.mod` `go X.Y.Z` directive.
   - `.github/workflows/ci.yml` `go-version` field.
   - `AGENTS.md §0` Language / runtime line.
   - `README.md` Prerequisites line.
4. Run `make security-check` and paste the output in the PR.
5. Require explicit user approval before merging. The user chose
   `go 1.24.0` deliberately; this needs sign-off.

---

## What about the Go *stdlib* security backport policy?

Go's security team backports fixes to the **two most recent majors**.
As of this writing those are 1.25 and 1.26. The 1.24 series no longer
receives stdlib patches — `govulncheck` on the latest 1.24.x patch
still reports the TLS KeyUpdate DoS, x509 name-constraint bypass, and
similar issues as reachable from our code.

This is a known tradeoff of pinning `go 1.24.0`:

- The `go` directive in go.mod is the **minimum language version**
  contributors can compile with.
- The **toolchain** used to actually build production binaries should
  ideally be a supported version (1.25.x or 1.26.x).
- If you want to formalise that gap, add a `toolchain go1.25.<patch>`
  directive — the floor stays at 1.24 for contributor compatibility,
  but `go build` auto-downloads a patched toolchain. This currently is
  NOT enabled (intentionally, per user direction). If a future PR adds
  it, update SECURITY.md and AGENTS.md §0 together.

---

## Anti-patterns

| Pattern | Why it's wrong | Fix |
|---|---|---|
| `go get -u ./...` | Picks up every available upgrade including ones that bump the Go floor | Bump one dep at a time with explicit version |
| Editing `go 1.24.0` to suppress a tidy rewrite | Hides the real signal; the floor will get bumped again on the next tidy | Find which dep forces the bump; pin lower; document in PR |
| Adding a `replace` directive to silence a CVE banner | Replace doesn't change which version govulncheck evaluates | Bump the require line, or dismiss the CVE per `wallet-vuln-triage` if not reachable |
| Bumping deps in a feature PR | Mixes two reviews; hides the diff that matters | Floor / dep changes get their own PR |
| Skipping `make security-check` after a bump | A bump can re-introduce a reachable CVE through a transitive chain | Always re-run the gate |
| Pinning `golang.org/x/*` modules without reading their go.mod | Most recent minors of `x/sys`, `x/text`, `x/sync` now declare `go 1.25.0` | Check the floor before pinning; pick the highest 1.24-compatible patch |

---

## Verification after any dep change

```sh
grep "^go " go.mod                                  # still 1.24.0
go mod verify                                       # checksums match
make security-check                                 # fmt + vet + lint + govulncheck
DATABASE_URL=... make test-int                      # integration suite still green
```
