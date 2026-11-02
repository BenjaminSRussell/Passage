package migrations

import (
	"context"
	"testing"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/testkit"
)

const payRunInsert = `insert into pay_run (tenant_id, id, legal_entity_id, pay_date, period_start, period_end, status, calculated_by, idempotency_key, request_hash)
	values ($1, $2, $3, '2025-03-14', '2025-03-01', '2025-03-14', 'CALCULATED', 'u', $4::text, 'h')`

func payRunArgs(tenantID, legalEntityID string) []any {
	return []any{tenantID, ids.New(), legalEntityID, ids.New()}
}

func TestAppRole_CrossTenantRLS(t *testing.T) {
	admin, app := testkit.DBWithApp(t)
	if err := dbx.Migrate(context.Background(), admin, FS); err != nil {
		t.Fatal(err)
	}
	testkit.AssertTenantRLS(t, admin)
	testkit.CrossTenant(t, app, []testkit.RLSCase{
		testkit.OutboxCase(),
		testkit.IdempotencyCase(),
		{Table: "pay_run", Insert: payRunInsert, Args: payRunArgs, LegalEntity: true},
		{
			Table:       "pay_statement",
			Before:      payRunInsert,
			Insert:      `insert into pay_statement (tenant_id, run_id, worker_id, legal_entity_id, rule_set, hash, gross, net, employer_cost, lines) values ($1, $2, $4::uuid, $3, 'r', 'h', 1, 1, 0, '[]')`,
			Args:        payRunArgs,
			LegalEntity: true,
			NoUpdate:    true,
		},
	})
}
