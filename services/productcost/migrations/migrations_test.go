package migrations

import (
	"context"
	"testing"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/testkit"
)

// TestMigrationsApplyWithRLS applies the costing schema to an empty schema and
// checks that every table leads with tenant_id, enables and forces row-level
// security, and carries the tenant policy; tables with legal_entity_id also
// carry the legal-entity scope policy.
func TestMigrationsApplyWithRLS(t *testing.T) {
	pool := testkit.DB(t)
	ctx := context.Background()
	if err := dbx.Migrate(ctx, pool, FS); err != nil {
		t.Fatal(err)
	}
	// Applying twice must be a no-op (dbx records applied files).
	if err := dbx.Migrate(ctx, pool, FS); err != nil {
		t.Fatalf("second migrate: %v", err)
	}
	rows, err := pool.Query(ctx, `
		select c.relname, c.relrowsecurity, c.relforcerowsecurity,
		       (select a.attname from pg_attribute a where a.attrelid = c.oid and a.attnum = 1),
		       exists (select 1 from pg_attribute a where a.attrelid = c.oid and a.attname = 'legal_entity_id' and not a.attisdropped),
		       exists (select 1 from pg_policies p where p.schemaname = n.nspname and p.tablename = c.relname and p.policyname = c.relname || '_tenant_access'),
		       exists (select 1 from pg_policies p where p.schemaname = n.nspname and p.tablename = c.relname and p.policyname = c.relname || '_legal_entity_scope')
		from pg_class c join pg_namespace n on n.oid = c.relnamespace
		where n.nspname = current_schema() and c.relkind = 'r' and c.relname <> 'schema_migrations'`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	want := map[string]bool{"costing_version": false, "cost_component": false, "cost_calculation": false, "cost_component_split": false, "variance_entry": false}
	for rows.Next() {
		var name, first string
		var rls, force, hasLE, tenantPolicy, scopePolicy bool
		if err := rows.Scan(&name, &rls, &force, &first, &hasLE, &tenantPolicy, &scopePolicy); err != nil {
			t.Fatal(err)
		}
		if _, ok := want[name]; !ok {
			continue
		}
		want[name] = true
		if first != "tenant_id" {
			t.Errorf("%s: first column %s, want tenant_id", name, first)
		}
		if !rls || !force {
			t.Errorf("%s: row-level security enabled=%v forced=%v", name, rls, force)
		}
		if !tenantPolicy {
			t.Errorf("%s: no tenant access policy", name)
		}
		if hasLE && !scopePolicy {
			t.Errorf("%s: carries legal_entity_id but has no legal-entity scope policy", name)
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	for name, seen := range want {
		if !seen {
			t.Errorf("table %s was not created", name)
		}
	}
}
