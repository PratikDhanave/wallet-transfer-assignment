`go get github.com/jackc/pgx/v5@v5.9.0` rewrote our `go.mod` from `go 1.24.0` to `go 1.25.0`. We didn't realize until CI failed. Here's the chain reaction and how to prevent it.

The `go` directive in `go.mod` is not a comment, and it's not a wish. It is the minimum language version every contributor and every CI runner must use to compile the module. `go mod tidy` writes it as the **maximum** of the current directive and the `go` directive in every selected module's own `go.mod` — including transitive deps and the test-time deps of those deps.

That last clause is where the trouble lives. A single `go get` for a security patch can read like a one-dep bump and behave like a toolchain bump.

Here is the chain we walked through.

pgx 5.9.0 declares `go 1.25.0` in its own `go.mod`. So `go get github.com/jackc/pgx/v5@v5.9.0` followed by `go mod tidy` rewrites our directive to 1.25.0. Same trap with `go.opentelemetry.io/otel` from v1.42 onward (declares 1.25), `otelhttp` from v0.67 onward (declares 1.25), `testcontainers-go` from v0.41 onward (declares 1.25 plus pulls grpc-gateway, which pulls genproto, which pulls `x/text` 0.36, which needs 1.25), and `open-policy-agent/opa` from v1.15 onward.

Detecting it after the fact is a one-liner: `grep ^go go.mod` in CI and fail if it ever moves off the pinned floor. Detecting it before commit is the same line in a pre-push hook. We folded it into a `make security-check` target along with govulncheck so the gate is one command for contributors and one command in CI.

The dep table we now keep in a project skill, so the next agent or contributor doesn't re-derive it:

- `pgx/v5` — last 1.24-compatible: v5.8.0. First 1.25-forcing: v5.9.0.
- `go.opentelemetry.io/otel` (plus sdk/trace/metric/stdouttrace) — last 1.24-compatible: v1.41.0. First 1.25-forcing: v1.42.0.
- `go.opentelemetry.io/contrib/.../otelhttp` — last 1.24-compatible: v0.66.0. First 1.25-forcing: v0.67.0.
- `testcontainers-go` — last 1.24-compatible: v0.40.0. First 1.25-forcing: v0.41.0.
- `open-policy-agent/opa` — last 1.24-compatible: v1.10.0 (declares `go 1.24.6`). First 1.25-forcing: v1.15.0.

The point of pinning these is not nostalgia. The point is that a Go toolchain pin is part of your contributor onboarding contract, your container base image choice, your distro package availability, and your downstream consumers' compile target. Moving it deserves its own PR with its own review, not a Dependabot bump hidden in a security patch.

A common reflex is to reach for a `replace` directive. `replace` does not help here. The Go toolchain reads the `go` directive from the originally required version, not from the replacement target — `replace` swaps code at link time, not metadata. If `require github.com/jackc/pgx/v5 v5.9.0` is in your file, the floor moves to 1.25 even if you `replace` it with a fork that declares 1.24.

Three things that do work:

One: pin one dep at a time, with explicit versions. `go get -u ./...` is too aggressive for any repo with a floor it cares about.

Two: when you take a security patch, check whether the patch is on the 1.24-side of the line first. `go list -m -versions <module>` gives you the version list; `go mod download <module>@<candidate>` followed by reading the cached `go.mod` tells you the floor each version declares. Most projects backport at least one minor.

Three: if you genuinely need a floor bump, run it as its own PR. `go.mod`, `go.sum`, the `go-version` field in your CI workflow, the runtime line in your contributor docs, and the prerequisites line in your README all move together. Anything less means a contributor on Friday will be unable to build what passed CI on Thursday.

The chain reaction is preventable. The cost of preventing it is one greppable line in your gate.

Which deps have surprise-bumped your Go floor recently?
