package migrations

import (
	"context"
	"testing"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/testkit"
)

// Every table carries tenant_id and has row-level security enabled and forced with a
// tenant policy; every business table also has its legal-entity scope policy.
func TestMigrationsApplyWithRLS(t *testing.T) {
	pool := testkit.DB(t)
	ctx := context.Background()
	if err := dbx.Migrate(ctx, pool, FS); err != nil {
		t.Fatal(err)
	}
	rows, err := pool.Query(ctx, `select c.relname, c.relrowsecurity, c.relforcerowsecurity,
		exists (select 1 from pg_attribute a where a.attrelid = c.oid and a.attname = 'tenant_id' and not a.attisdropped),
		exists (select 1 from pg_policies p where p.schemaname = n.nspname and p.tablename = c.relname and p.policyname = c.relname || '_tenant_access')
		from pg_class c join pg_namespace n on n.oid = c.relnamespace
		where n.nspname = current_schema() and c.relkind = 'r' and c.relname <> 'schema_migrations'`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	seen := map[string]bool{}
	for rows.Next() {
		var name string
		var rls, force, tenantCol, policy bool
		if err := rows.Scan(&name, &rls, &force, &tenantCol, &policy); err != nil {
			t.Fatal(err)
		}
		seen[name] = true
		if !rls || !force || !tenantCol || !policy {
			t.Errorf("%s: rls=%v force=%v tenant_id=%v tenant policy=%v; want all true", name, rls, force, tenantCol, policy)
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	for _, table := range []string{"outbox", "idempotency_keys", "expense_report", "expense_line",
		"travel_requisition", "cash_advance", "card_import", "card_transaction"} {
		if !seen[table] {
			t.Errorf("table %s was not created", table)
		}
	}
	var scoped int
	if err := pool.QueryRow(ctx, `select count(*) from pg_policies where schemaname = current_schema()
		and policyname in ('expense_report_legal_entity_scope', 'expense_line_legal_entity_scope',
		  'travel_requisition_legal_entity_scope', 'cash_advance_legal_entity_scope',
		  'card_import_legal_entity_scope', 'card_transaction_legal_entity_scope') and permissive = 'RESTRICTIVE'`).Scan(&scoped); err != nil {
		t.Fatal(err)
	}
	if scoped != 6 {
		t.Fatalf("restrictive legal-entity policies = %d; want 6", scoped)
	}
}
