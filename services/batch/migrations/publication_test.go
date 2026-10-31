package migrations_test

import (
	"context"
	"testing"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/batch/migrations"
)

func TestCDCPublicationCoversOutbox(t *testing.T) {
	pool := testkit.DB(t)
	ctx := context.Background()
	for range 2 {
		if err := dbx.Migrate(ctx, pool, migrations.FS); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := pool.Exec(ctx, "delete from schema_migrations where name = '0004_cdc_publication.sql'"); err != nil {
		t.Fatal(err)
	}
	if err := dbx.Migrate(ctx, pool, migrations.FS); err != nil {
		t.Fatalf("rerun: %v", err)
	}
	var tables int
	err := pool.QueryRow(ctx, `select count(*) from pg_publication_tables
		where pubname = 'passage_outbox_batch' and schemaname = current_schema() and tablename = 'outbox'`).Scan(&tables)
	if err != nil {
		t.Fatal(err)
	}
	if tables != 1 {
		t.Fatalf("outbox published %d times, want 1", tables)
	}
	var all bool
	if err := pool.QueryRow(ctx, "select puballtables from pg_publication where pubname = 'passage_outbox_batch'").Scan(&all); err != nil {
		t.Fatal(err)
	}
	if all {
		t.Fatal("publication must list tables explicitly, not FOR ALL TABLES")
	}
}

func TestCDCRoleCanReadOutboxButNotWriteIt(t *testing.T) {
	pool := testkit.DB(t)
	ctx := context.Background()
	if _, err := pool.Exec(ctx, `do $$ begin create role debezium_cdc nologin; exception when duplicate_object or unique_violation then null; end $$`); err != nil {
		t.Skipf("cannot create the debezium_cdc role: %v", err)
	}
	if err := dbx.Migrate(ctx, pool, migrations.FS); err != nil {
		t.Fatal(err)
	}
	var read, write bool
	err := pool.QueryRow(ctx, `select has_table_privilege('debezium_cdc', 'outbox', 'SELECT'), has_table_privilege('debezium_cdc', 'outbox', 'INSERT')`).Scan(&read, &write)
	if err != nil {
		t.Fatal(err)
	}
	if !read || write {
		t.Fatalf("debezium_cdc select=%v insert=%v, want select only", read, write)
	}
}
