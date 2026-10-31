package store

import (
	"context"
	"testing"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/payables/migrations"
)

func TestAppRole_CrossTenantRLS(t *testing.T) {
	admin, app := testkit.DBWithApp(t)
	if err := dbx.Migrate(context.Background(), admin, migrations.FS); err != nil {
		t.Fatal(err)
	}
	testkit.AssertTenantRLS(t, admin)
	testkit.CrossTenant(t, app, []testkit.RLSCase{
		testkit.OutboxCase(),
		testkit.IdempotencyCase(),
		{
			Table: "ap_open_item",
			Insert: `insert into ap_open_item (tenant_id, id, legal_entity_id, vendor_id, source_invoice_id, invoice_number, invoice_date, due_date, currency, amount, open_amount, status)
				values ($1, $2, $3, $4, $5, 'INV-1', '2027-03-01', '2027-03-31', 'USD', 10, 10, 'OPEN')`,
			Args: func(tenantID, entityID string) []any {
				return []any{tenantID, ids.New(), entityID, ids.New(), ids.New()}
			},
			LegalEntity: true,
		},
		{
			Table:       "ap_vendor_block",
			Insert:      `insert into ap_vendor_block (tenant_id, legal_entity_id, vendor_id, reason) values ($1, $2, $3, 'sanctions review')`,
			Args:        func(tenantID, entityID string) []any { return []any{tenantID, entityID, ids.New()} },
			LegalEntity: true,
		},
		{
			Table: "ap_payment_proposal",
			Insert: `insert into ap_payment_proposal (tenant_id, id, legal_entity_id, run_date, run_id, due_by, rate_date, legal_entity_currency, payment_method, request_hash, status, created_by)
				values ($1, $2, $3, '2027-03-31', 'run-1', '2027-03-31', '2027-03-31', 'USD', 'TRANSFER', 'hash', 'DRAFT', 'alice')`,
			Args:        func(tenantID, entityID string) []any { return []any{tenantID, ids.New(), entityID} },
			LegalEntity: true,
			NoUpdate:    true,
		},
	})
}
