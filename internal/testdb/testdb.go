//go:build integration

// Package testdb provides a shared PostgreSQL container for integration
// tests.
//
// One container is started per test process (lazily, on the first
// call to Get) and reused across every test in the package. Tests
// call Reset(t) between subtests to TRUNCATE all tables so they don't
// see leftovers from previous runs.
//
// The package is build-tagged `integration` because it pulls in
// testcontainers-go (which assumes Docker is available); we don't
// want the dependency in the default test path.
package testdb

import (
	"context"
	"database/sql"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/testcontainers/testcontainers-go/modules/postgres"

	"github.com/PratikDhanave/wallet-transfer-assignment/internal/db"
)

// Shared state. `once` guards container startup so the first test
// pays the boot cost (~2s for the first image pull, sub-second
// afterwards) and every later test gets the cached connection.
var (
	once      sync.Once
	sharedDB  *sql.DB
	sharedDSN string
	sharedErr error
)

// Get returns a *sql.DB pointed at a freshly migrated database. The
// container is started lazily on first call; subsequent calls reuse
// the same DB and just hand back the cached handle.
//
// Failure to start the container (or apply migrations) is reported
// via t.Fatalf — there's nothing useful a test can do without a
// database, so failing fast is friendlier than letting a NPE surface
// downstream.
func Get(t *testing.T) *sql.DB {
	t.Helper()
	once.Do(func() {
		sharedDB, sharedErr = start()
	})
	if sharedErr != nil {
		t.Fatalf("start postgres container: %v", sharedErr)
	}
	return sharedDB
}

// DSN returns the connection string for the shared container,
// starting it lazily if needed. Useful for tests that need to spawn
// their own *sql.DB (for example, cmd/server tests that go through
// config.Load).
func DSN(t *testing.T) string {
	t.Helper()
	_ = Get(t)
	return sharedDSN
}

// Reset truncates every table. Call from the start of each test that
// mutates data so tests do not see leftovers from prior runs.
//
// RESTART IDENTITY CASCADE resets the BIGSERIAL counter on
// ledger_entries — without it, ledger ids would creep upward across
// tests and any test that asserts on specific ids would be flaky.
// CASCADE follows FKs so we don't have to worry about ordering.
func Reset(t *testing.T) {
	t.Helper()
	d := Get(t)
	_, err := d.ExecContext(context.Background(),
		`TRUNCATE idempotency_records, ledger_entries, transfers, wallets RESTART IDENTITY CASCADE`)
	if err != nil {
		t.Fatalf("reset db: %v", err)
	}
}

// start brings up the container and applies migrations. Internal —
// only called once, via sync.Once, from Get.
//
// 90 seconds is generous: typical local startup is under 3s, but the
// first run on a fresh machine pulls the postgres:16-alpine image
// which can take a while on slow networks. CI runs tend to land in
// the 10-15s range.
func start() (*sql.DB, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	c, err := postgres.Run(ctx, "postgres:16-alpine",
		postgres.WithDatabase("wallet_test"),
		postgres.WithUsername("wallet"),
		postgres.WithPassword("wallet"),
		// BasicWaitStrategies waits for the container to log
		// "ready to accept connections" twice (Postgres logs it
		// once for the bootstrap, once for the live socket) and
		// then probes the TCP port.
		postgres.BasicWaitStrategies(),
	)
	if err != nil {
		return nil, fmt.Errorf("postgres.Run: %w", err)
	}

	dsn, err := c.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		return nil, fmt.Errorf("connection string: %w", err)
	}
	sharedDSN = dsn
	d, err := db.Open(dsn)
	if err != nil {
		return nil, err
	}
	if err := db.Migrate(d); err != nil {
		return nil, fmt.Errorf("migrate: %w", err)
	}
	return d, nil
}
