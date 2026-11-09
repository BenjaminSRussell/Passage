package migrations

import (
	"context"
	"io/fs"
	"testing"
	"testing/fstest"

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

// originalAddressSearch is 0026 as it stood before it was edited in place: databases that ran it
// keep this form, because dbx.Migrate does not checksum applied files.
const originalAddressSearch = `CREATE EXTENSION IF NOT EXISTS pg_trgm;
CREATE INDEX IF NOT EXISTS postal_address_formatted_trgm ON postal_address USING gin (formatted gin_trgm_ops);
`

const convergence = "0034_address_search_converge.sql"

// historicFS is the migrations up to 0033 with the original 0026 text; withConvergence adds 0034.
func historicFS(t *testing.T, withConvergence bool) fs.FS {
	t.Helper()
	out := fstest.MapFS{}
	entries, err := fs.ReadDir(FS, ".")
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		name := e.Name()
		if name > "0033_" && name != convergence {
			continue
		}
		if name == convergence && !withConvergence {
			continue
		}
		body, err := fs.ReadFile(FS, name)
		if err != nil {
			t.Fatal(err)
		}
		if name == "0026_address_search.sql" {
			body = []byte(originalAddressSearch)
		}
		out[name] = &fstest.MapFile{Data: body}
	}
	return out
}

func assertTrigramIndexWorks(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	ctx := context.Background()
	var valid bool
	if err := pool.QueryRow(ctx, `select i.indisvalid from pg_index i join pg_class c on c.oid = i.indexrelid
		where c.relnamespace = current_schema()::regnamespace and c.relname = 'postal_address_formatted_trgm'`).Scan(&valid); err != nil {
		t.Fatalf("trigram index missing: %v", err)
	}
	if !valid {
		t.Fatal("trigram index is invalid")
	}
	var n int
	if err := pool.QueryRow(ctx, `select count(*) from postal_address where formatted ilike '%main%'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
}

// ensurePublicTrigram installs pg_trgm in public before the original 0026 runs. Its unqualified
// CREATE EXTENSION would otherwise, on a database without the extension, put it in this test's
// throwaway schema: tests running alongside (in this and other packages sharing the database) would
// then miss public.gin_trgm_ops, and dropping the schema would drop the extension with their indexes.
// Where the extension lands when absent is the 0034 code path checked by hand as the database owner.
func ensurePublicTrigram(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	if _, err := pool.Exec(context.Background(), `DO $$
BEGIN
  CREATE EXTENSION IF NOT EXISTS pg_trgm WITH SCHEMA public;
EXCEPTION WHEN unique_violation THEN
  NULL;
END
$$`); err != nil {
		t.Fatal(err)
	}
}

func TestAddressSearchConvergesDatabaseThatRanOriginal0026(t *testing.T) {
	pool := testkit.DB(t)
	ctx := context.Background()
	ensurePublicTrigram(t, pool)
	if err := dbx.Migrate(ctx, pool, historicFS(t, false)); err != nil {
		t.Fatal(err)
	}
	assertTrigramIndexWorks(t, pool)
	// Already healthy: the convergence migration is a no-op.
	if err := dbx.Migrate(ctx, pool, historicFS(t, true)); err != nil {
		t.Fatal(err)
	}
	assertTrigramIndexWorks(t, pool)
	// A database that lost the index converges when 0034 runs again.
	if _, err := pool.Exec(ctx, `drop index postal_address_formatted_trgm`); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `delete from schema_migrations where name = $1`, convergence); err != nil {
		t.Fatal(err)
	}
	if err := dbx.Migrate(ctx, pool, historicFS(t, true)); err != nil {
		t.Fatal(err)
	}
	assertTrigramIndexWorks(t, pool)
}

func TestAddressSearchFreshDatabaseConvergenceIsNoOp(t *testing.T) {
	pool := testkit.DB(t)
	ctx := context.Background()
	if err := dbx.Migrate(ctx, pool, FS); err != nil {
		t.Fatal(err)
	}
	assertTrigramIndexWorks(t, pool)
	var oid1, oid2 uint32
	q := `select c.oid from pg_class c where c.relnamespace = current_schema()::regnamespace and c.relname = 'postal_address_formatted_trgm'`
	if err := pool.QueryRow(ctx, q).Scan(&oid1); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `delete from schema_migrations where name = $1`, convergence); err != nil {
		t.Fatal(err)
	}
	if err := dbx.Migrate(ctx, pool, FS); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, q).Scan(&oid2); err != nil {
		t.Fatal(err)
	}
	if oid1 != oid2 {
		t.Fatal("convergence migration rebuilt a healthy index")
	}
}

// An index left invalid (a failed CREATE INDEX CONCURRENTLY) is dropped and rebuilt. Marking it
// invalid needs a catalog write, so the case runs only when the test role is a superuser.
func TestAddressSearchConvergenceRebuildsInvalidIndex(t *testing.T) {
	pool := testkit.DB(t)
	ctx := context.Background()
	var super bool
	if err := pool.QueryRow(ctx, `select rolsuper from pg_roles where rolname = current_user`).Scan(&super); err != nil {
		t.Fatal(err)
	}
	if !super {
		t.Skip("marking an index invalid needs a superuser test role")
	}
	if err := dbx.Migrate(ctx, pool, FS); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `update pg_index set indisvalid = false
		where indexrelid = (select c.oid from pg_class c
			where c.relnamespace = current_schema()::regnamespace and c.relname = 'postal_address_formatted_trgm')`); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `delete from schema_migrations where name = $1`, convergence); err != nil {
		t.Fatal(err)
	}
	if err := dbx.Migrate(ctx, pool, FS); err != nil {
		t.Fatal(err)
	}
	assertTrigramIndexWorks(t, pool)
}
