package store

import (
	"context"
	"testing"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/costacct/migrations"
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
			Table:       "cost_object",
			Insert:      `insert into cost_object (tenant_id, id, legal_entity_id, kind, code, name) values ($1, $2, $3, 'CostCenter', 'CC-100', 'Plant')`,
			Args:        func(tenantID, entityID string) []any { return []any{tenantID, ids.New(), entityID} },
			LegalEntity: true,
			NoUpdate:    true,
		},
		{
			Table:       "cost_element",
			Insert:      `insert into cost_element (tenant_id, id, legal_entity_id, kind, code, name) values ($1, $2, $3, 'Secondary', 'ALLOC-1', 'Allocation')`,
			Args:        func(tenantID, entityID string) []any { return []any{tenantID, ids.New(), entityID} },
			LegalEntity: true,
			NoUpdate:    true,
		},
	})
}
