package migrations

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/testkit"
)

const caPostalRegex = `^[ABCEGHJ-NPRSTVXY][0-9][ABCEGHJ-NPRSTV-Z] ?[0-9][ABCEGHJ-NPRSTV-Z][0-9]$`

func TestCountryCABackfillRewritesStaleRegex(t *testing.T) {
	pool := testkit.DB(t)
	ctx := context.Background()
	if err := dbx.Migrate(ctx, pool, FS); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `insert into country (tenant_id, code, name, region_required, postal_regex)
		values ('00000000-0000-0000-0000-000000000000', 'CA', 'Canada', true, '^[A-Z][0-9][A-Z] ?[0-9][A-Z][0-9]$')`); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `delete from schema_migrations where name = '0030_country_ca_postal_regex.sql'`); err != nil {
		t.Fatal(err)
	}
	if err := dbx.Migrate(ctx, pool, FS); err != nil {
		t.Fatal(err)
	}
	var got string
	if err := pool.QueryRow(ctx, `select postal_regex from country where code = 'CA'`).Scan(&got); err != nil {
		t.Fatal(err)
	}
	if got != caPostalRegex {
		t.Fatalf("CA postal_regex = %q, want %q", got, caPostalRegex)
	}
}

func TestReassertedConstraintsAndPoliciesIgnoreOtherSchemas(t *testing.T) {
	pool := testkit.DB(t)
	ctx := context.Background()
	decoy := "decoy_org"
	for _, stmt := range []string{
		"create schema " + decoy,
		"create table " + decoy + ".legal_entity (a int, constraint legal_entity_fiscal_calendar_fk check (a > 0))",
		"create table " + decoy + ".site (a int, constraint site_fiscal_calendar_fk check (a > 0), constraint site_default_warehouse_fk check (a > 0))",
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
		"alter table legal_entity drop constraint legal_entity_fiscal_calendar_fk",
		"alter table site drop constraint site_fiscal_calendar_fk",
		"alter table site drop constraint site_default_warehouse_fk",
		"drop policy org_unit_translation_tenant_access on org_unit_translation",
		"drop policy org_unit_translation_legal_entity_scope on org_unit_translation",
		"delete from schema_migrations where name = '0031_reassert_schema_scoped_guards.sql'",
	} {
		if _, err := pool.Exec(ctx, stmt); err != nil {
			t.Fatal(err)
		}
	}
	if err := dbx.Migrate(ctx, pool, FS); err != nil {
		t.Fatal(err)
	}
	for table, name := range map[string]string{
		"legal_entity": "legal_entity_fiscal_calendar_fk",
		"site":         "site_fiscal_calendar_fk",
	} {
		assertForeignKey(t, pool, table, name)
	}
	assertForeignKey(t, pool, "site", "site_default_warehouse_fk")
	var policies int
	if err := pool.QueryRow(ctx, `select count(*) from pg_policies where schemaname = current_schema()
		and tablename = 'org_unit_translation'`).Scan(&policies); err != nil {
		t.Fatal(err)
	}
	if policies != 2 {
		t.Fatalf("org_unit_translation policies = %d, want 2", policies)
	}
}

func assertForeignKey(t *testing.T, pool *pgxpool.Pool, table, name string) {
	t.Helper()
	var n int
	if err := pool.QueryRow(context.Background(), `select count(*) from pg_constraint c
		join pg_class r on r.oid = c.conrelid
		where r.relnamespace = current_schema()::regnamespace and r.relname = $1
		and c.conname = $2 and c.contype = 'f'`, table, name).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("%s: foreign key %s count = %d, want 1", table, name, n)
	}
}
