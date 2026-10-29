package store

import (
	"context"
	"testing"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/quality/migrations"
)

func TestAppRole_CrossTenantRLS(t *testing.T) {
	admin, app := testkit.DBWithApp(t)
	if err := dbx.Migrate(context.Background(), admin, migrations.FS); err != nil {
		t.Fatal(err)
	}
	testkit.AssertTenantRLS(t, admin)
	testkit.CrossTenant(t, app, []testkit.RLSCase{
		testkit.OutboxCase(),
		{
			Table:  "quality_instrument",
			Insert: `insert into quality_instrument (tenant_id, id, code, name, calibration_interval_days, last_calibrated_at, due_at) values ($1, $2, 'CAL-1', 'Caliper', 30, now(), now() + interval '30 days')`,
			Args:   func(tenantID, _ string) []any { return []any{tenantID, ids.New()} },
		},
		{
			Table:    "quality_test_group",
			Insert:   `insert into quality_test_group (tenant_id, id, code, version, tests) values ($1, $2, 'TG-1', 1, '[]')`,
			Args:     func(tenantID, _ string) []any { return []any{tenantID, ids.New()} },
			NoUpdate: true,
		},
	})
}
