package migrations

import (
	"context"
	"testing"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/testkit"
)

func TestMigrationsApplyWithRLS(t *testing.T) {
	pool := testkit.DB(t)
	ctx := context.Background()
	if err := dbx.Migrate(ctx, pool, FS); err != nil {
		t.Fatal(err)
	}
	var policies int
	if err := pool.QueryRow(ctx, `select count(*) from pg_policies
		where schemaname = current_schema()
		and policyname in ('outbox_tenant_access', 'idempotency_keys_tenant_access')`).Scan(&policies); err != nil {
		t.Fatal(err)
	}
	if policies != 2 {
		t.Fatalf("want tenant RLS policies on outbox and idempotency_keys, found %d", policies)
	}
}
