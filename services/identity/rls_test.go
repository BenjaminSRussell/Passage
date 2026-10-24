package identity_test

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/identity/migrations"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	migratorURL = "PASSAGE_TEST_DATABASE_URL"
	appURL      = "PASSAGE_TEST_APP_DATABASE_URL"
)

// This integration test requires two real PostgreSQL logins. The migrator
// applies DDL and owns the tables; passage_app only receives schema/table
// privileges and exercises RLS through its own connection pool.
func TestIdentityTablesEnforceTenantAndLegalEntityRLS(t *testing.T) {
	migratorDSN := os.Getenv(migratorURL)
	appDSN := os.Getenv(appURL)
	if migratorDSN == "" || appDSN == "" {
		t.Skipf("%s and %s are required for the two-role RLS integration test", migratorURL, appURL)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	schema := "identity_test_" + strings.ReplaceAll(ids.New(), "-", "")
	migrator, err := pgxpool.New(ctx, migratorDSN)
	if err != nil {
		t.Fatal(err)
	}
	defer migrator.Close()
	if _, err := migrator.Exec(ctx, "create schema "+schema); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = migrator.Exec(context.Background(), "drop schema "+schema+" cascade")
	})

	migratorCfg, err := pgxpool.ParseConfig(migratorDSN)
	if err != nil {
		t.Fatal(err)
	}
	migratorCfg.ConnConfig.RuntimeParams["search_path"] = schema
	migratorScoped, err := pgxpool.NewWithConfig(ctx, migratorCfg)
	if err != nil {
		t.Fatal(err)
	}
	defer migratorScoped.Close()
	if err := dbx.Migrate(ctx, migratorScoped, migrations.FS()); err != nil {
		t.Fatal(err)
	}

	// Role provisioning belongs to the shared platform bootstrap. The isolated
	// test schema grants only what the pre-provisioned passage_app role needs.
	if _, err := migrator.Exec(ctx, fmt.Sprintf("grant usage on schema %s to passage_app", schema)); err != nil {
		t.Fatal(err)
	}
	if _, err := migrator.Exec(ctx, fmt.Sprintf("grant select, insert, update, delete on all tables in schema %s to passage_app", schema)); err != nil {
		t.Fatal(err)
	}

	appCfg, err := pgxpool.ParseConfig(appDSN)
	if err != nil {
		t.Fatal(err)
	}
	appCfg.ConnConfig.RuntimeParams["search_path"] = schema
	app, err := pgxpool.NewWithConfig(ctx, appCfg)
	if err != nil {
		t.Fatal(err)
	}
	defer app.Close()
	if err := app.Ping(ctx); err != nil {
		t.Fatalf("connect as passage_app: %v", err)
	}

	var currentUser string
	var superuser, bypassRLS bool
	if err := app.QueryRow(ctx, `select current_user, rolsuper, rolbypassrls
from pg_roles where rolname = current_user`).Scan(&currentUser, &superuser, &bypassRLS); err != nil {
		t.Fatal(err)
	}
	if currentUser != "passage_app" || superuser || bypassRLS {
		t.Fatalf("app role must be passage_app NOSUPERUSER NOBYPASSRLS; got role=%q superuser=%v bypassrls=%v", currentUser, superuser, bypassRLS)
	}
	var migratorUser string
	if err := migratorScoped.QueryRow(ctx, `select current_user`).Scan(&migratorUser); err != nil {
		t.Fatal(err)
	}
	if migratorUser == currentUser {
		t.Fatalf("migrations and application must use distinct logins; both connected as %q", currentUser)
	}

	for _, table := range []string{"identity_tenants", "identity_users", "identity_legal_entities"} {
		var owner string
		var enabled, forced bool
		var policies int
		err := app.QueryRow(ctx, `select pg_get_userbyid(c.relowner), c.relrowsecurity, c.relforcerowsecurity,
       (select count(*) from pg_policies p where p.schemaname = n.nspname and p.tablename = c.relname and p.policyname = $2 || '_tenant_access')
from pg_class c join pg_namespace n on n.oid = c.relnamespace
where n.nspname = $1 and c.relname = $2`, schema, table).Scan(&owner, &enabled, &forced, &policies)
		if err != nil {
			t.Fatal(err)
		}
		if owner != migratorUser || owner == currentUser || !enabled || !forced || policies != 1 {
			t.Errorf("%s catalog: owner=%q migrator=%q enabled=%v forced=%v tenant policies=%d", table, owner, migratorUser, enabled, forced, policies)
		}
	}
	var legalEntityPolicy bool
	if err := app.QueryRow(ctx, `select exists(select 1 from pg_policies where schemaname=$1 and tablename='identity_legal_entities' and policyname='identity_legal_entities_legal_entity_scope' and permissive='RESTRICTIVE')`, schema).Scan(&legalEntityPolicy); err != nil {
		t.Fatal(err)
	}
	if !legalEntityPolicy {
		t.Fatal("identity_legal_entities missing its restrictive legal-entity policy")
	}

	const tenantA = "00000000-0000-0000-0000-000000000001"
	const tenantB = "00000000-0000-0000-0000-000000000002"
	const legalA = "10000000-0000-0000-0000-000000000001"
	const legalB = "10000000-0000-0000-0000-000000000002"
	seed := func(tenantID string, legalIDs ...string) {
		t.Helper()
		seedCtx := tenant.With(ctx, tenant.Principal{TenantID: tenantID, UserID: "test-user", LegalEntityID: "*"})
		if err := dbx.WithTenantTx(seedCtx, app, func(tx pgx.Tx) error {
			if _, err := tx.Exec(ctx, `insert into identity_tenants (tenant_id, realm_name, display_name) values ($1, $2, $3)`, tenantID, "realm-"+tenantID, "Tenant"); err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `insert into identity_users (tenant_id, user_id, idp_subject, display_name) values ($1, $2, $3, $4)`, tenantID, "20000000-0000-0000-0000-000000000001", "subject-"+tenantID, "User"); err != nil {
				return err
			}
			for i, legalID := range legalIDs {
				if _, err := tx.Exec(ctx, `insert into identity_legal_entities (tenant_id, legal_entity_id, code, display_name) values ($1, $2, $3, $4)`, tenantID, legalID, fmt.Sprintf("LE-%d", i), "Legal entity"); err != nil {
					return err
				}
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
	}
	seed(tenantA, legalA, legalB)
	seed(tenantB, legalA)

	// A fresh app connection with no GUC sees no rows, despite seeded data.
	probeCfg, err := pgxpool.ParseConfig(appDSN)
	if err != nil {
		t.Fatal(err)
	}
	probeCfg.ConnConfig.RuntimeParams["search_path"] = schema
	probeCfg.MaxConns = 1
	probe, err := pgxpool.NewWithConfig(ctx, probeCfg)
	if err != nil {
		t.Fatal(err)
	}
	defer probe.Close()
	assertNoTenantRows := func(stage string) {
		t.Helper()
		var count int
		if err := probe.QueryRow(ctx, `select count(*) from identity_users`).Scan(&count); err != nil {
			t.Fatalf("%s unscoped query: %v", stage, err)
		}
		if count != 0 {
			t.Fatalf("%s without passage.tenant_id saw %d rows, want 0", stage, count)
		}
	}
	assertNoTenantRows("unset GUC")
	// Force a transaction-local GUC to be used and then reset on this same
	// pooled connection; reset custom GUCs may read back as an empty string.
	if err := dbx.WithTenantTx(tenant.With(ctx, tenant.Principal{TenantID: tenantA, UserID: "test-user", LegalEntityID: legalA}), probe, func(tx pgx.Tx) error {
		var count int
		return tx.QueryRow(ctx, `select count(*) from identity_users`).Scan(&count)
	}); err != nil {
		t.Fatal(err)
	}
	assertNoTenantRows("empty GUC after commit")

	readCount := func(tenantID, legalEntityID, query string, args ...any) int {
		t.Helper()
		readCtx := tenant.With(ctx, tenant.Principal{TenantID: tenantID, UserID: "test-user", LegalEntityID: legalEntityID})
		var count int
		if err := dbx.WithTenantTx(readCtx, app, func(tx pgx.Tx) error {
			return tx.QueryRow(ctx, query, args...).Scan(&count)
		}); err != nil {
			t.Fatal(err)
		}
		return count
	}
	if got := readCount(tenantA, legalA, `select count(*) from identity_users`); got != 1 {
		t.Errorf("tenant A visible users = %d, want 1", got)
	}
	if got := readCount(tenantA, legalA, `select count(*) from identity_users where tenant_id=$1`, tenantB); got != 0 {
		t.Errorf("tenant A saw %d tenant B users, want 0", got)
	}
	if got := readCount(tenantA, legalA, `select count(*) from identity_legal_entities`); got != 1 {
		t.Errorf("legal-entity A visible entities = %d, want 1", got)
	}
	if got := readCount(tenantA, legalB, `select count(*) from identity_legal_entities`); got != 1 {
		t.Errorf("legal-entity B visible entities = %d, want 1", got)
	}
	if got := readCount(tenantA, "10000000-0000-0000-0000-000000000099", `select count(*) from identity_legal_entities`); got != 0 {
		t.Errorf("out-of-scope legal entity saw %d entities, want 0", got)
	}
}
