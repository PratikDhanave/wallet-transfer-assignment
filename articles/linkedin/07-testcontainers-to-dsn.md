We deleted testcontainers from a Go financial service. The binary got smaller, `go.mod` went from 86 deps to 41, and our `go 1.24.0` floor stopped getting silently rewritten.

The replacement is 130 lines.

---

The problem started as a Go version bump nobody asked for. The repo pinned `go 1.24.0` in `go.mod` and a CI job that ran `go vet ./...` started failing because the module graph was demanding `go 1.25`. The culprit was buried four levels deep in the dependency tree:

testcontainers-go → docker/docker → docker/cli → grpc-gateway → demands go 1.25

Run `go mod tidy` once and `go.mod` quietly rewrites your floor. You either accept the bump or you stop using testcontainers. Removing testcontainers is the smaller blast radius.

The replacement is a single file, `internal/testdb/testdb.go`, which:

1. Reads a `DATABASE_URL` env var (the same one the production server reads).
2. If unset, every integration test calls `t.Skip` with a message that names the variable and the make target. The suite passes without Docker.
3. If set, opens a `*sql.DB`, applies migrations once via `sync.Once`, bounds the pool to 25 connections, and hands the shared handle to every test.
4. Exposes a `Reset(tb)` helper that `TRUNCATE ... RESTART IDENTITY CASCADE`s the four tables, called from the start of any test that mutates data.

Locally: `make db-up && export DATABASE_URL=postgres://... && make test-int`. In CI: GitHub Actions' `services.postgres` block declares a `postgres:16-alpine` container, sets `DATABASE_URL`, the integration tests pick it up.

The one thing we got wrong on the first try was `go test ./...`. When tests across multiple packages share a database, two test binaries can run in parallel and one will `TRUNCATE` mid-way through the other. The fix is `-p 1` (one test binary at a time) on the integration suite. We codified it in the Makefile so contributors don't have to remember.

Measured wins from the swap:

- `go.mod` dropped from 86 transitive dependencies to 41.
- The Go floor stopped getting rewritten — `go 1.24.0` stays pinned through `go mod tidy`.
- `go test` startup time dropped because there is no `docker.NewClient` chain to initialise.
- govulncheck runs faster because there is less to scan.
- `make test` (unit) and `make test-int` (integration) are now clean modes — the unit suite needs nothing, the integration suite needs Postgres.

The tradeoff is real. With testcontainers, every contributor running `go test ./...` automatically got a fresh container. With the env-var contract, every contributor needs to `make db-up` first, or skip the integration tests entirely. We chose "skip if unset" over "fail loud" because the unit suite has 88.1% coverage already; integration tests are additive verification, not gating verification.

When is the in-process container actually worth the dep weight? Three cases:

1. **Your codebase already needs Docker** for other tests (real Kafka, real Redis, real Elasticsearch). The marginal cost of testcontainers is small if you're already running containers from Go.
2. **You can't trust the contributor environment** to have a database. Open-source projects with many drive-by contributors. Pinning the version inside the test code is more reliable than asking everyone to run `docker compose`.
3. **You need per-test isolation** that's cheaper to express by recreating the container than by truncating tables. Most teams don't, but some do — particularly when tests need to assert on database-level state (active connections, autovacuum, etc.) that survives TRUNCATE.

For everything else, the env-var contract is 130 lines, zero deps, and matches what your CI is going to do anyway.

When does an in-process container actually pay for itself in your stack?
