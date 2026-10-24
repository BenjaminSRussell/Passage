package store_test

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/receiving/internal/store"
	"github.com/BenjaminSRussell/passage/services/receiving/migrations"
)

const (
	vendorID = "00000000-0000-0000-0000-0000000000a1"
	poID     = "00000000-0000-0000-0000-0000000000b1"
)

func principal(n int) tenant.Principal {
	return tenant.Principal{TenantID: testkit.TenantID(n), LegalEntityID: testkit.TenantID(9)}
}

func TestPing(t *testing.T) {
	pool := testkit.DB(t)
	if err := store.New(pool).Ping(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestMigrationsApplyWithRLS(t *testing.T) {
	admin, pool := testkit.DBWithApp(t)
	ctx := tenant.With(context.Background(), principal(1))
	if err := dbx.Migrate(ctx, admin, migrations.FS); err != nil {
		t.Fatal(err)
	}
	var currentUser string
	var roleSuper, roleBypass bool
	metadataErr := pool.QueryRow(ctx, `select current_user, r.rolsuper, r.rolbypassrls from pg_roles r where r.rolname = current_user`).Scan(&currentUser, &roleSuper, &roleBypass)
	if metadataErr != nil || currentUser != "passage_app" || roleSuper || roleBypass {
		t.Fatalf("RLS test must use non-bypass passage_app role: user=%q super=%v bypass=%v err=%v", currentUser, roleSuper, roleBypass, metadataErr)
	}
	tables := []string{"receiving_receipt", "receiving_receipt_line", "receiving_serial", "receiving_grir_open_item", "idempotency_keys", "outbox", "consumer_inbox"}
	for _, table := range tables {
		var enabled, forced bool
		err := pool.QueryRow(ctx, `select relrowsecurity, relforcerowsecurity from pg_class where relname = $1 and relnamespace = current_schema()::regnamespace`, table).Scan(&enabled, &forced)
		if err != nil || !enabled || !forced {
			t.Errorf("%s: rls enabled=%v forced=%v err=%v", table, enabled, forced, err)
		}
	}

	insert := func(p tenant.Principal, slip string) error {
		return dbx.WithTenantTx(tenant.With(ctx, p), pool, func(tx pgx.Tx) error {
			_, err := tx.Exec(ctx, `insert into receiving_receipt (tenant_id, id, legal_entity_id, vendor_id, purchase_order_id, packing_slip, state, receipt_date)
				values ($1, gen_random_uuid(), $2, $3, $4, $5, 'ARRIVED', current_date)`, p.TenantID, p.LegalEntityID, vendorID, poID, slip)
			return err
		})
	}
	if err := insert(principal(1), "PS-1"); err != nil {
		t.Fatal(err)
	}
	if err := insert(principal(1), "PS-1"); err == nil {
		t.Fatal("duplicate packing slip for one vendor must be rejected")
	}
	count := func(p tenant.Principal) (n int) {
		err := dbx.WithTenantTx(tenant.With(ctx, p), pool, func(tx pgx.Tx) error {
			return tx.QueryRow(ctx, `select count(*) from receiving_receipt`).Scan(&n)
		})
		if err != nil {
			t.Fatal(err)
		}
		return n
	}
	if got := count(principal(1)); got != 1 {
		t.Fatalf("tenant 1 sees %d rows, want 1", got)
	}
	if got := count(principal(2)); got != 0 {
		t.Fatalf("tenant 2 sees %d rows, want 0", got)
	}
}
