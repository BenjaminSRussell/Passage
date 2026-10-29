package migrations

import (
	"context"
	"testing"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/testkit"
)

func TestReassertedReversalFKsLandOnCurrentSchemaTables(t *testing.T) {
	pool := testkit.DB(t)
	ctx := context.Background()
	decoy := "decoy_inventory"
	for _, stmt := range []string{
		"create schema " + decoy,
		"create table " + decoy + ".inventory_journal (a int, constraint inventory_journal_reverses_fk check (a > 0))",
		"create table " + decoy + ".inventory_journal_line (a int, constraint inventory_journal_line_reverses_fk check (a > 0))",
	} {
		if _, err := pool.Exec(ctx, stmt); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() { _, _ = pool.Exec(context.Background(), "drop schema "+decoy+" cascade") })
	if err := dbx.Migrate(ctx, pool, FS); err != nil {
		t.Fatal(err)
	}
	for _, stmt := range []string{
		"alter table inventory_journal drop constraint inventory_journal_reverses_fk",
		"alter table inventory_journal_line drop constraint inventory_journal_line_reverses_fk",
		"delete from schema_migrations where name = '0010_reassert_reversal_fks.sql'",
	} {
		if _, err := pool.Exec(ctx, stmt); err != nil {
			t.Fatal(err)
		}
	}
	if err := dbx.Migrate(ctx, pool, FS); err != nil {
		t.Fatal(err)
	}
	for table, name := range map[string]string{
		"inventory_journal":      "inventory_journal_reverses_fk",
		"inventory_journal_line": "inventory_journal_line_reverses_fk",
	} {
		var n int
		if err := pool.QueryRow(ctx, `select count(*) from pg_constraint c
			join pg_class r on r.oid = c.conrelid
			where r.relnamespace = current_schema()::regnamespace and r.relname = $1
			and c.conname = $2 and c.contype = 'f'`, table, name).Scan(&n); err != nil {
			t.Fatal(err)
		}
		if n != 1 {
			t.Fatalf("%s: foreign key %s count = %d, want 1", table, name, n)
		}
	}
}
