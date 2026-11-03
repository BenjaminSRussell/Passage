// Package testdb migrates the throwaway schemas of the scheduling database
// tests. Migration 0001 runs CREATE EXTENSION IF NOT EXISTS btree_gist, and an
// extension is one object per database: when `go test ./...` runs the store and
// cmd/scheduling test binaries at the same time, two schemas create it at once
// (duplicate key in pg_extension_name_index), and one created in a throwaway
// schema (the first entry of testkit's search_path) is dropped with that schema,
// taking the other package's exclusion constraints with it. Migrate installs the
// extension in public under a cluster-wide advisory lock before applying the
// migrations, so the migration's own CREATE EXTENSION finds it and does nothing.
package testdb

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/services/scheduling/migrations"
)

// lockKey is the advisory lock held while the extension is set up.
const lockKey = 0x7363686564 // "sched"

// Migrate ensures btree_gist lives in public, then applies the scheduling
// migrations to admin's schema.
func Migrate(t *testing.T, admin *pgxpool.Pool) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	tx, err := admin.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `select pg_advisory_xact_lock($1)`, int64(lockKey)); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `create extension if not exists btree_gist with schema public`); err != nil {
		t.Fatal(err)
	}
	// One left in a throwaway schema by an earlier run would go with that schema.
	var schema string
	if err := tx.QueryRow(ctx, `select extnamespace::regnamespace::text from pg_extension where extname = 'btree_gist'`).Scan(&schema); err != nil {
		t.Fatal(err)
	}
	if schema != "public" {
		if _, err := tx.Exec(ctx, `alter extension btree_gist set schema public`); err != nil {
			t.Fatal(err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if err := dbx.Migrate(ctx, admin, migrations.FS); err != nil {
		t.Fatal(err)
	}
}
