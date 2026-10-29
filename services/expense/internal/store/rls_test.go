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
	})
}
