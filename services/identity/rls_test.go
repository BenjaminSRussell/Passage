package identity_test

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/identity/migrations"
)

const (
	userID    = "20000000-0000-0000-0000-000000000001"
	legalA    = "10000000-0000-0000-0000-000000000001"
	legalB    = "10000000-0000-0000-0000-000000000002"
	legalNone = "10000000-0000-0000-0000-000000000099"
)

var identityTables = []string{"identity_tenants", "identity_users", "identity_legal_entities", "identity_role_assignments"}

func inScope(ctx context.Context, tenantID, legalEntityID string) context.Context {
	return tenant.With(ctx, tenant.Principal{TenantID: tenantID, UserID: "test-user", LegalEntityID: legalEntityID})
}

func asTenant(t *testing.T, app *pgxpool.Pool, tenantID, legalEntityID string, fn func(tx pgx.Tx) error) error {
	t.Helper()
	return dbx.WithTenantTx(inScope(context.Background(), tenantID, legalEntityID), app, fn)
}

func count(t *testing.T, app *pgxpool.Pool, tenantID, legalEntityID, query string, args ...any) int {
	t.Helper()
	var n int
	if err := asTenant(t, app, tenantID, legalEntityID, func(tx pgx.Tx) error {
		return tx.QueryRow(context.Background(), query, args...).Scan(&n)
	}); err != nil {
		t.Fatal(err)
	}
	return n
}

func setup(t *testing.T) (*pgxpool.Pool, string, string) {
	t.Helper()
	admin, app := testkit.DBWithApp(t)
	ctx := context.Background()
	if err := dbx.Migrate(ctx, admin, migrations.FS); err != nil {
		t.Fatal(err)
	}
	tenantA, tenantB := testkit.TenantID(1), testkit.TenantID(2)
	seed := func(tenantID string, legalIDs ...string) {
		if _, err := admin.Exec(ctx, `insert into identity_tenants (tenant_id, realm_name, display_name) values ($1, $2, 'Tenant')`, tenantID, "realm-"+tenantID); err != nil {
			t.Fatal(err)
		}
		if err := asTenant(t, app, tenantID, "*", func(tx pgx.Tx) error {
			if _, err := tx.Exec(ctx, `insert into identity_users (tenant_id, user_id, idp_subject) values ($1, $2, $3)`, tenantID, userID, "subject-"+tenantID); err != nil {
				return err
			}
			for i, legalID := range legalIDs {
				if _, err := tx.Exec(ctx, `insert into identity_legal_entities (tenant_id, legal_entity_id, code, display_name) values ($1, $2, $3, 'Legal entity')`, tenantID, legalID, "LE-"+string(rune('A'+i))); err != nil {
					return err
				}
				if _, err := tx.Exec(ctx, `insert into identity_role_assignments (tenant_id, assignment_id, user_id, role, legal_entity_id) values ($1, gen_random_uuid(), $2, 'ap_clerk', $3)`, tenantID, userID, legalID); err != nil {
					return err
				}
			}
			_, err := tx.Exec(ctx, `insert into identity_role_assignments (tenant_id, assignment_id, user_id, role) values ($1, gen_random_uuid(), $2, 'tenant_admin')`, tenantID, userID)
			return err
		}); err != nil {
			t.Fatal(err)
		}
	}
	seed(tenantA, legalA, legalB)
	seed(tenantB, legalA)
	return app, tenantA, tenantB
}

func TestIdentityTablesAreForcedRLSOwnedByMigrator(t *testing.T) {
	admin, app := testkit.DBWithApp(t)
	ctx := context.Background()
	if err := dbx.Migrate(ctx, admin, migrations.FS); err != nil {
		t.Fatal(err)
	}
	var user string
	var super, bypass bool
	if err := app.QueryRow(ctx, `select current_user, rolsuper, rolbypassrls from pg_roles where rolname = current_user`).Scan(&user, &super, &bypass); err != nil {
		t.Fatal(err)
	}
	if user != "passage_app" || super || bypass {
		t.Fatalf("role=%q super=%v bypassrls=%v", user, super, bypass)
	}
	for _, table := range identityTables {
		var owner string
		var enabled, forced bool
		var policies int
		if err := app.QueryRow(ctx, `select pg_get_userbyid(c.relowner), c.relrowsecurity, c.relforcerowsecurity,
  (select count(*) from pg_policies p where p.schemaname = n.nspname and p.tablename = c.relname)
from pg_class c join pg_namespace n on n.oid = c.relnamespace
where n.nspname = current_schema() and c.relname = $1`, table).Scan(&owner, &enabled, &forced, &policies); err != nil {
			t.Fatal(err)
		}
		if owner == user || !enabled || !forced || policies < 1 {
			t.Errorf("%s: owner=%q enabled=%v forced=%v policies=%d", table, owner, enabled, forced, policies)
		}
	}
}

func TestUnscopedAndResetSessionsSeeNoRows(t *testing.T) {
	app, tenantA, _ := setup(t)
	ctx := context.Background()
	conn, err := app.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Release()
	for _, stage := range []string{"unset", "reset after commit"} {
		if stage == "reset after commit" {
			tx, err := conn.Begin(ctx)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := tx.Exec(ctx, `select set_config('passage.tenant_id', $1, true)`, tenantA); err != nil {
				t.Fatal(err)
			}
			if err := tx.Commit(ctx); err != nil {
				t.Fatal(err)
			}
		}
		for _, table := range identityTables {
			var n int
			if err := conn.QueryRow(ctx, `select count(*) from `+table).Scan(&n); err != nil {
				t.Fatalf("%s %s: %v", stage, table, err)
			}
			if n != 0 {
				t.Errorf("%s %s: saw %d rows without tenant scope", stage, table, n)
			}
		}
	}
}

func TestCrossTenantReadIsolation(t *testing.T) {
	app, tenantA, tenantB := setup(t)
	for _, table := range identityTables {
		if got := count(t, app, tenantA, "*", `select count(*) from `+table+` where tenant_id = $1`, tenantB); got != 0 {
			t.Errorf("tenant A read %d tenant B rows in %s", got, table)
		}
		if got := count(t, app, tenantA, "*", `select count(*) from `+table); got == 0 {
			t.Errorf("tenant A sees none of its own rows in %s", table)
		}
	}
}

func TestCrossTenantWriteIsRejected(t *testing.T) {
	app, tenantA, tenantB := setup(t)
	ctx := context.Background()
	inserts := map[string]string{
		"identity_tenants":          `insert into identity_tenants (tenant_id, realm_name, display_name) values ($1, 'x', 'x')`,
		"identity_users":            `insert into identity_users (tenant_id, user_id, idp_subject) values ($1, gen_random_uuid(), 'x')`,
		"identity_legal_entities":   `insert into identity_legal_entities (tenant_id, legal_entity_id, code, display_name) values ($1, gen_random_uuid(), 'X', 'x')`,
		"identity_role_assignments": `insert into identity_role_assignments (tenant_id, assignment_id, user_id, role) values ($1, gen_random_uuid(), '` + userID + `', 'x')`,
	}
	for table, stmt := range inserts {
		err := asTenant(t, app, tenantA, "*", func(tx pgx.Tx) error {
			_, err := tx.Exec(ctx, stmt, tenantB)
			return err
		})
		if err == nil {
			t.Errorf("tenant A inserted a tenant B row into %s", table)
		}
	}
	for _, table := range identityTables[1:] {
		var updated, deleted int64
		if err := asTenant(t, app, tenantA, "*", func(tx pgx.Tx) error {
			u, err := tx.Exec(ctx, `update `+table+` set tenant_id = tenant_id where tenant_id = $1`, tenantB)
			if err != nil {
				return err
			}
			d, err := tx.Exec(ctx, `delete from `+table+` where tenant_id = $1`, tenantB)
			updated, deleted = u.RowsAffected(), d.RowsAffected()
			return err
		}); err != nil {
			t.Fatal(err)
		}
		if updated != 0 || deleted != 0 {
			t.Errorf("%s: tenant A updated %d / deleted %d tenant B rows", table, updated, deleted)
		}
		if got := count(t, app, tenantB, "*", `select count(*) from `+table); got == 0 {
			t.Errorf("tenant B lost its %s rows", table)
		}
	}
}

func TestLegalEntityScopeRestrictsEntitiesAndRoleAssignments(t *testing.T) {
	app, tenantA, _ := setup(t)
	cases := []struct {
		scope           string
		entities, roles int
	}{
		{"*", 2, 3},
		{legalA, 1, 1},
		{legalB, 1, 1},
		{legalA + "," + legalB, 2, 2},
		{legalNone, 0, 0},
	}
	for _, c := range cases {
		if got := count(t, app, tenantA, c.scope, `select count(*) from identity_legal_entities`); got != c.entities {
			t.Errorf("scope %q sees %d legal entities, want %d", c.scope, got, c.entities)
		}
		if got := count(t, app, tenantA, c.scope, `select count(*) from identity_role_assignments`); got != c.roles {
			t.Errorf("scope %q sees %d role assignments, want %d", c.scope, got, c.roles)
		}
	}
	err := asTenant(t, app, tenantA, legalA, func(tx pgx.Tx) error {
		_, err := tx.Exec(context.Background(), `insert into identity_legal_entities (tenant_id, legal_entity_id, code, display_name) values ($1, $2, 'OUT', 'x')`, tenantA, legalNone)
		return err
	})
	if err == nil {
		t.Error("legal-entity-scoped caller inserted an out-of-scope legal entity")
	}
}

func TestRoleAssignmentEffectiveDating(t *testing.T) {
	app, tenantA, _ := setup(t)
	ctx := context.Background()
	insert := func(validFrom, validTo string) error {
		return asTenant(t, app, tenantA, "*", func(tx pgx.Tx) error {
			_, err := tx.Exec(ctx, `insert into identity_role_assignments (tenant_id, assignment_id, user_id, role, valid_from, valid_to)
values ($1, gen_random_uuid(), $2, 'dated', `+validFrom+`, `+validTo+`)`, tenantA, userID)
			return err
		})
	}
	if err := insert("now()", "now() - interval '1 day'"); err == nil {
		t.Fatal("valid_to before valid_from was accepted")
	}
	if err := insert("now() + interval '1 day'", "null"); err != nil {
		t.Fatal(err)
	}
	if err := insert("now() - interval '2 days'", "now() - interval '1 day'"); err != nil {
		t.Fatal(err)
	}
	const effective = `select count(*) from identity_role_assignments where valid_from <= now() and (valid_to is null or valid_to > now())`
	if got := count(t, app, tenantA, "*", effective); got != 3 {
		t.Errorf("effective assignments = %d, want 3", got)
	}
	if got := count(t, app, tenantA, "*", `select count(*) from identity_role_assignments where role = 'dated'`); got != 2 {
		t.Errorf("dated assignments = %d, want 2", got)
	}
}

func TestRoleAssignmentOverlapIsRejectedAndAdjacentAllowed(t *testing.T) {
	app, tenantA, _ := setup(t)
	ctx := context.Background()
	insert := func(entity any, validFrom, validTo string) error {
		return asTenant(t, app, tenantA, "*", func(tx pgx.Tx) error {
			_, err := tx.Exec(ctx, `insert into identity_role_assignments (tenant_id, assignment_id, user_id, role, legal_entity_id, valid_from, valid_to)
values ($1, gen_random_uuid(), $2, 'overlap', $3, `+validFrom+`, `+validTo+`)`, tenantA, userID, entity)
			return err
		})
	}
	if err := insert(nil, "'2030-01-01'", "'2030-02-01'"); err != nil {
		t.Fatal(err)
	}
	if err := insert(nil, "'2030-01-15'", "'2030-03-01'"); err == nil {
		t.Error("overlapping tenant-wide assignment was accepted")
	}
	if err := insert(nil, "'2029-01-01'", "null"); err == nil {
		t.Error("open-ended assignment overlapping an existing one was accepted")
	}
	if err := insert(nil, "'2030-02-01'", "'2030-03-01'"); err != nil {
		t.Errorf("adjacent assignment rejected: %v", err)
	}
	if err := insert(legalA, "'2030-01-15'", "'2030-03-01'"); err != nil {
		t.Errorf("same role on a different legal entity rejected: %v", err)
	}
}

func TestRoleMustBeNonEmpty(t *testing.T) {
	app, tenantA, _ := setup(t)
	err := asTenant(t, app, tenantA, "*", func(tx pgx.Tx) error {
		_, err := tx.Exec(context.Background(), `insert into identity_role_assignments (tenant_id, assignment_id, user_id, role) values ($1, gen_random_uuid(), $2, '')`, tenantA, userID)
		return err
	})
	if err == nil {
		t.Error("empty role was accepted")
	}
}

func TestAppRoleCannotWriteTenants(t *testing.T) {
	app, tenantA, _ := setup(t)
	ctx := context.Background()
	for name, stmt := range map[string]string{
		"insert":      `insert into identity_tenants (tenant_id, realm_name, display_name) values ($1, 'new-realm', 'x')`,
		"update":      `update identity_tenants set status = 'active' where tenant_id = $1`,
		"update-self": `update identity_tenants set realm_name = 'renamed' where tenant_id = $1`,
		"delete":      `delete from identity_tenants where tenant_id = $1`,
	} {
		err := asTenant(t, app, tenantA, "*", func(tx pgx.Tx) error {
			_, err := tx.Exec(ctx, stmt, tenantA)
			return err
		})
		if err == nil {
			t.Errorf("passage_app could %s identity_tenants", name)
		}
	}
	if got := count(t, app, tenantA, "*", `select count(*) from identity_tenants`); got != 1 {
		t.Errorf("tenant A sees %d tenant rows, want 1", got)
	}
}

func TestUpdateCannotMoveRowToAnotherTenant(t *testing.T) {
	app, tenantA, tenantB := setup(t)
	err := asTenant(t, app, tenantA, "*", func(tx pgx.Tx) error {
		_, err := tx.Exec(context.Background(), `update identity_legal_entities set tenant_id = $1 where tenant_id = $2`, tenantB, tenantA)
		return err
	})
	if err == nil {
		t.Error("tenant A moved its legal entity to tenant B")
	}
}
