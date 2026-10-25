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
	tables := []string{"receiving_receipt", "receiving_receipt_line", "receiving_serial", "receiving_grir_open_item", "idempotency_keys", "outbox", "consumer_inbox", "receiving_number_counter"}
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

func TestMigration0002_TenantAndLegalEntityIsolation(t *testing.T) {
	admin, pool := testkit.DBWithApp(t)
	ctx := context.Background()
	if err := dbx.Migrate(tenant.With(ctx, principal(1)), admin, migrations.FS); err != nil {
		t.Fatal(err)
	}
	if err := dbx.Migrate(tenant.With(ctx, principal(1)), admin, migrations.FS); err != nil {
		t.Fatalf("repeat apply must be a no-op: %v", err)
	}
	const receiptID = "00000000-0000-0000-0000-0000000000c1"
	run := func(p tenant.Principal, sql string, args ...any) error {
		return dbx.WithTenantTx(tenant.With(ctx, p), pool, func(tx pgx.Tx) error {
			_, err := tx.Exec(ctx, sql, args...)
			return err
		})
	}
	count := func(p tenant.Principal, table string) (n int) {
		err := dbx.WithTenantTx(tenant.With(ctx, p), pool, func(tx pgx.Tx) error {
			return tx.QueryRow(ctx, `select count(*) from `+table).Scan(&n)
		})
		if err != nil {
			t.Fatal(err)
		}
		return n
	}
	one := principal(1)
	seed := []struct {
		sql  string
		args []any
	}{
		{`insert into receiving_receipt (tenant_id, id, legal_entity_id, vendor_id, purchase_order_id, packing_slip, state, receipt_date, receipt_number, exchange_rate)
			values ($1, $2, $3, $4, $5, 'PS-1', 'ARRIVED', current_date, 'RCV-000001', 1)`, []any{one.TenantID, receiptID, one.LegalEntityID, vendorID, poID}},
		{`insert into receiving_receipt_line (tenant_id, receipt_id, line_no, legal_entity_id, product_id, qty, unit_price, currency)
			values ($1, $2, 1, $3, $4, 5, 2, 'USD')`, []any{one.TenantID, receiptID, one.LegalEntityID, poID}},
		{`insert into receiving_grir_open_item (tenant_id, id, legal_entity_id, receipt_id, line_no, received_qty, received_amount, currency, vendor_id, po_line_id)
			values ($1, gen_random_uuid(), $2, $3, 1, 5, 10, 'USD', $4, $5)`, []any{one.TenantID, one.LegalEntityID, receiptID, vendorID, poID}},
		{`insert into receiving_number_counter (tenant_id, prefix, next_value) values ($1, 'RCV-', 2)`, []any{one.TenantID}},
	}
	for _, step := range seed {
		if err := run(one, step.sql, step.args...); err != nil {
			t.Fatal(err)
		}
	}

	otherLegalEntity := tenant.Principal{TenantID: one.TenantID, LegalEntityID: testkit.TenantID(8)}
	for _, table := range []string{"receiving_receipt", "receiving_receipt_line", "receiving_grir_open_item", "receiving_number_counter"} {
		if got := count(one, table); got != 1 {
			t.Errorf("%s: owner sees %d rows, want 1", table, got)
		}
		if got := count(principal(2), table); got != 0 {
			t.Errorf("%s: other tenant sees %d rows, want 0", table, got)
		}
		if table == "receiving_number_counter" {
			continue
		}
		if got := count(otherLegalEntity, table); got != 0 {
			t.Errorf("%s: other legal entity sees %d rows, want 0", table, got)
		}
	}

	if err := run(principal(2), `insert into receiving_number_counter (tenant_id, prefix, next_value) values ($1, 'RCV-', 1)`, one.TenantID); err == nil {
		t.Error("another tenant must not write the first tenant's counter")
	}
	if err := run(principal(2), `update receiving_number_counter set next_value = 99`); err != nil {
		t.Fatal(err)
	}
	var next int
	_ = dbx.WithTenantTx(tenant.With(ctx, one), pool, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `select next_value from receiving_number_counter where prefix = 'RCV-'`).Scan(&next)
	})
	if next != 2 {
		t.Errorf("counter changed by another tenant: %d", next)
	}

	insertReceipt := func(p tenant.Principal, number string) error {
		return run(p, `insert into receiving_receipt (tenant_id, id, legal_entity_id, vendor_id, purchase_order_id, packing_slip, state, receipt_date, receipt_number)
			values ($1, gen_random_uuid(), $2, $3, $4, $5, 'ARRIVED', current_date, $6)`, p.TenantID, p.LegalEntityID, vendorID, poID, "PS-"+number+p.TenantID, number)
	}
	if err := insertReceipt(one, "RCV-000001"); err == nil {
		t.Error("receipt_number must be unique per tenant")
	}
	if err := insertReceipt(principal(2), "RCV-000001"); err != nil {
		t.Errorf("same receipt_number in another tenant must be allowed: %v", err)
	}
	if err := run(one, `update receiving_receipt_line set inventory_status = 'LOST' where tenant_id = $1`, one.TenantID); err == nil {
		t.Error("inventory_status CHECK must reject unknown values")
	}
	if err := run(one, `update receiving_receipt_line set inventory_status = 'QUARANTINE' where tenant_id = $1`, one.TenantID); err != nil {
		t.Errorf("QUARANTINE must be accepted: %v", err)
	}
	if err := run(one, `update receiving_grir_open_item set status = 'LOST' where tenant_id = $1`, one.TenantID); err == nil {
		t.Error("GR/IR status CHECK must reject unknown values")
	}
	if err := run(one, `update receiving_grir_open_item set status = 'PARTIAL' where tenant_id = $1`, one.TenantID); err != nil {
		t.Errorf("PARTIAL must be accepted: %v", err)
	}
}
