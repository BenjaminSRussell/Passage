// Package testdb serialises the database tests of this service. The migrations create the fixed schema "migration"
// (not the throwaway schema testkit puts on the search path), so two test binaries that `go test ./...` runs at the
// same time (store and syncer) would drop and re-create the same schema under each other. Fresh takes a
// cluster-wide advisory lock for the test's lifetime, so those tests run one at a time across packages.
package testdb

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// lockKey is the advisory lock every database test of the migration service holds while it owns the schema.
const lockKey = 0x6d6967726174 // "migrat"

// Fresh locks the migration schema for the test, drops it, and drops it again when the test ends (before the lock is
// released). The caller then applies the migrations on admin.
func Fresh(t *testing.T, admin *pgxpool.Pool) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	conn, err := admin.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Exec(ctx, `select pg_advisory_lock($1)`, int64(lockKey)); err != nil {
		conn.Release()
		t.Fatal(err)
	}
	drop := func() { _, _ = admin.Exec(context.Background(), "drop schema if exists migration cascade") }
	// Cleanups run last-in first-out: the schema is dropped, then the lock released.
	t.Cleanup(func() {
		_, _ = conn.Exec(context.Background(), `select pg_advisory_unlock($1)`, int64(lockKey))
		conn.Release()
	})
	drop()
	t.Cleanup(drop)
}
