package store_test

import (
	"context"
	"testing"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/expense/migrations"
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
			Table:  "expense_report",
			Insert: `insert into expense_report (tenant_id, id, legal_entity_id, worker_id, purpose, currency, create_key, request_hash) values ($1, $2, $3, $4, 'Client visit', 'USD', $5, 'hash')`,
			Args: func(tenantID, entityID string) []any {
				return []any{tenantID, ids.New(), entityID, ids.New(), ids.New()}
			},
			LegalEntity: true,
		},
		{
			Table:  "expense_line",
			Before: `insert into expense_report (tenant_id, id, legal_entity_id, worker_id, purpose, currency, create_key, request_hash) values ($1, $2, $3, $4, 'Client visit', 'USD', $5, 'hash')`,
			Insert: `insert into expense_line (tenant_id, id, report_id, line_no, category, txn_date, amount, create_key, request_hash) select tenant_id, id, id, 1, 'MEALS', '2026-09-01', 10, $5, 'hash' from expense_report where tenant_id = $1 and id = $2 and legal_entity_id = $3 and worker_id = $4`,
			Args: func(tenantID, entityID string) []any {
				return []any{tenantID, ids.New(), entityID, ids.New(), ids.New()}
			},
			LegalEntity: true,
			Delete:      true,
		},
		{
			Table:  "travel_requisition",
			Insert: `insert into travel_requisition (tenant_id, id, legal_entity_id, worker_id, purpose, currency, trip_from, trip_to, estimated_total, create_key, request_hash) values ($1, $2, $3, $4, 'Workshop', 'USD', '2025-03-03', '2025-03-10', 0, $5, 'hash')`,
			Args: func(tenantID, entityID string) []any {
				return []any{tenantID, ids.New(), entityID, ids.New(), ids.New()}
			},
			LegalEntity: true,
		},
		{
			Table:  "cash_advance",
			Insert: `insert into cash_advance (tenant_id, id, legal_entity_id, worker_id, amount, currency, purpose, trip_end, due_settlement_date, create_key, request_hash) values ($1, $2, $3, $4, 100, 'USD', 'Trip', '2025-03-10', '2025-04-09', $5, 'hash')`,
			Args: func(tenantID, entityID string) []any {
				return []any{tenantID, ids.New(), entityID, ids.New(), ids.New()}
			},
			LegalEntity: true,
		},
		{
			Table:  "card_import",
			Insert: `insert into card_import (tenant_id, id, legal_entity_id, format, file_hash, create_key, request_hash) values ($1, $2, $3, 'CSV', encode(sha256(convert_to($4::text, 'UTF8')), 'hex'), $5, 'hash')`,
			Args: func(tenantID, entityID string) []any {
				return []any{tenantID, ids.New(), entityID, ids.New(), ids.New()}
			},
			LegalEntity: true,
			NoUpdate:    true,
		},
		{
			Table:  "card_transaction",
			Before: `insert into card_import (tenant_id, id, legal_entity_id, format, file_hash, create_key, request_hash) values ($1, $2, $3, 'CSV', encode(sha256(convert_to($4::text, 'UTF8')), 'hex'), $5, 'hash')`,
			Insert: `insert into card_transaction (tenant_id, id, import_id, legal_entity_id, worker_id, source_line, card_last4, txn_date, post_date, merchant, amount, currency) values ($1, $2, $2, $3, $4, 1, '4242', '2025-03-03', '2025-03-04', $5::text, 12.5, 'USD')`,
			Args: func(tenantID, entityID string) []any {
				return []any{tenantID, ids.New(), entityID, ids.New(), ids.New()}
			},
			LegalEntity: true,
			NoUpdate:    true,
		},
	})
}
