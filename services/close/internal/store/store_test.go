package store_test

import (
	"context"
	"testing"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/close/internal/domain"
	"github.com/BenjaminSRussell/passage/services/close/internal/store"
	"github.com/BenjaminSRussell/passage/services/close/migrations"
)

const (
	ledgerID = "00000000-0000-0000-0000-0000000000a1"
	periodID = "00000000-0000-0000-0000-0000000000b1"
	entityID = "00000000-0000-0000-0000-0000000000c1"
)

func principal(n int) context.Context {
	return tenant.With(context.Background(), tenant.Principal{TenantID: testkit.TenantID(n)})
}

func seeded(t *testing.T) *store.Postgres {
	t.Helper()
	admin, app := testkit.DBWithApp(t)
	if err := dbx.Migrate(context.Background(), admin, migrations.FS); err != nil {
		t.Fatal(err)
	}
	for _, row := range []struct{ module, status, group string }{{"all", "closed", ""}, {"ap", "on_hold", "controllers"}} {
		if _, err := admin.Exec(context.Background(),
			`insert into period_status(tenant_id, legal_entity_id, ledger_id, fiscal_period_id, module, status, allowed_group_id, changed_by) values($1,$2,$3,$4,$5,$6,nullif($7,''),'test')`,
			testkit.TenantID(1), entityID, ledgerID, periodID, row.module, row.status, row.group); err != nil {
			t.Fatal(err)
		}
	}
	return store.New(app)
}

func TestPeriodRows_ReadsOwnTenantAndDecides(t *testing.T) {
	s := seeded(t)
	rows, err := s.PeriodRows(principal(1), ledgerID, periodID)
	if err != nil || len(rows) != 2 {
		t.Fatalf("got %v, %v; want 2 rows", rows, err)
	}
	if _, err := domain.Decide(rows, domain.ModuleAP, "controllers"); err != nil {
		t.Fatalf("controllers may post to a held AP period: %v", err)
	}
	if _, err := domain.Decide(rows, domain.ModuleGL, "controllers"); err == nil {
		t.Fatal("GL falls back to the closed ALL row and must be rejected")
	}
}

func TestPeriodRows_TenantIsolation(t *testing.T) {
	s := seeded(t)
	rows, err := s.PeriodRows(principal(2), ledgerID, periodID)
	if err != nil || len(rows) != 0 {
		t.Fatalf("tenant 2 must see no rows, got %v, %v", rows, err)
	}
}
