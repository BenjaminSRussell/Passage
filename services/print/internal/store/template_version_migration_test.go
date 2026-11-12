package store_test

import (
	"context"
	"io/fs"
	"net/url"
	"os"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/print/migrations"
)

// Migration 0008 backfills the version history of the templates saved before it. Deployed, the migrations run as the
// tables' owner, which is not a superuser, so the forced tenant policies apply to it and a backfill that sets no tenant
// would see no template and copy nothing. Run here as such an owner.
func TestTemplateVersionBackfillRunsAsANonSuperuserOwner(t *testing.T) {
	base := os.Getenv(testkit.EnvURL)
	if base == "" {
		t.Skip("no test database")
	}
	admin := testkit.DB(t)
	ctx := context.Background()
	role := "print_owner_" + strings.ReplaceAll(ids.New(), "-", "")[:12]
	schema := "o_" + role
	for _, stmt := range []string{
		`create role ` + role + ` login password 'owner-pw' nosuperuser nobypassrls`,
		`create schema ` + schema + ` authorization ` + role,
	} {
		if _, err := admin.Exec(ctx, stmt); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() {
		_, _ = admin.Exec(context.Background(), `drop schema `+schema+` cascade`)
		_, _ = admin.Exec(context.Background(), `drop owned by `+role)
		_, _ = admin.Exec(context.Background(), `drop role `+role)
	})
	u, err := url.Parse(base)
	if err != nil {
		t.Fatal(err)
	}
	u.User = url.UserPassword(role, "owner-pw")
	cfg, err := pgxpool.ParseConfig(u.String())
	if err != nil {
		t.Fatal(err)
	}
	cfg.ConnConfig.RuntimeParams["search_path"] = schema
	owner, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer owner.Close()

	before := fstest.MapFS{}
	names, _ := fs.Glob(migrations.FS, "*.sql")
	for _, n := range names {
		if n < "0008" {
			b, _ := fs.ReadFile(migrations.FS, n)
			before[n] = &fstest.MapFile{Data: b}
		}
	}
	if err := dbx.Migrate(ctx, owner, before); err != nil {
		t.Fatalf("migrations up to 0007 as the owner: %v", err)
	}
	tenant := testkit.TenantID(1)
	// The tenant is set for this transaction only, as the service does; the migration then runs with none.
	tx, err := owner.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `select set_config('passage.tenant_id', $1, true)`, tenant); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `insert into print_document_template (tenant_id, code, name, document_type, version, config)
		values ($1, 'INV-OLD', 'Old invoice', 'CUSTOMER_INVOICE', 4, '{"title":"Invoice"}')`, tenant); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if err := dbx.Migrate(ctx, owner, migrations.FS); err != nil {
		t.Fatalf("migration 0008 as the owner: %v", err)
	}
	var n, version int
	if err := admin.QueryRow(ctx, `select count(*), coalesce(max(version), 0) from `+schema+`.print_document_template_version where code = 'INV-OLD'`).Scan(&n, &version); err != nil {
		t.Fatal(err)
	}
	if n != 1 || version != 4 {
		t.Fatalf("backfilled versions: %d (version %d), want the template's version 4", n, version)
	}
	var forced bool
	if err := admin.QueryRow(ctx, `select relforcerowsecurity from pg_class c join pg_namespace s on s.oid = c.relnamespace
		where s.nspname = $1 and c.relname = 'print_document_template'`, schema).Scan(&forced); err != nil || !forced {
		t.Fatalf("row-level security is forced again: %v %v", forced, err)
	}
}
