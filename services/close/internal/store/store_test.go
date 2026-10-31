package store_test

import (
	"context"
	"testing"
	"time"

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
	return tenant.With(context.Background(), tenant.Principal{TenantID: testkit.TenantID(n), Duties: map[string]struct{}{tenant.DutyAllScope: {}}})
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

func TestPeriodSpans_ResolvesByDateWithinLegalEntityAndTenant(t *testing.T) {
	admin, app := testkit.DBWithApp(t)
	if err := dbx.Migrate(context.Background(), admin, migrations.FS); err != nil {
		t.Fatal(err)
	}
	otherEntity := "00000000-0000-0000-0000-0000000000c2"
	insert := func(tenantN int, entity, period, module, status, start, end string) {
		t.Helper()
		if _, err := admin.Exec(context.Background(),
			`insert into period_status(tenant_id, legal_entity_id, ledger_id, fiscal_period_id, module, status, start_date, end_date, changed_by) values($1,$2,$3,$4,$5,$6,nullif($7,'')::date,nullif($8,'')::date,'test')`,
			testkit.TenantID(tenantN), entity, ledgerID, period, module, status, start, end); err != nil {
			t.Fatal(err)
		}
	}
	jan, feb := "00000000-0000-0000-0000-0000000000b1", "00000000-0000-0000-0000-0000000000b2"
	insert(1, entityID, jan, "all", "open", "2026-01-01", "2026-01-31")
	insert(1, entityID, jan, "ap", "closed", "2026-01-01", "2026-01-31")
	insert(1, entityID, feb, "all", "open", "2026-02-01", "2026-02-28")
	insert(1, otherEntity, "00000000-0000-0000-0000-0000000000b3", "all", "open", "2026-01-01", "2026-01-31")
	insert(1, entityID, "00000000-0000-0000-0000-0000000000b4", "all", "open", "", "")
	insert(2, entityID, "00000000-0000-0000-0000-0000000000b5", "all", "open", "2026-01-01", "2026-01-31")
	s := store.New(app)
	day := func(d string) time.Time { v, _ := time.Parse("2006-01-02", d); return v }

	spans, err := s.PeriodSpans(principal(1), entityID, ledgerID, day("2026-01-31"))
	if err != nil || len(spans) != 1 || spans[0].FiscalPeriodID != jan || len(spans[0].Rows) != 2 {
		t.Fatalf("got %+v, %v; want only January with its 2 module rows", spans, err)
	}
	if status, _ := domain.Decide(spans[0].Rows, domain.ModuleAP, ""); status != domain.StatusClosed {
		t.Fatalf("AP status %s, want closed", status)
	}
	if spans, err = s.PeriodSpans(principal(1), entityID, ledgerID, day("2026-02-01")); err != nil || len(spans) != 1 || spans[0].FiscalPeriodID != feb {
		t.Fatalf("got %+v, %v; want only February", spans, err)
	}
	if spans, err = s.PeriodSpans(principal(1), entityID, ledgerID, day("2026-03-01")); err != nil || len(spans) != 0 {
		t.Fatalf("got %+v, %v; want none", spans, err)
	}
	if spans, err = s.PeriodSpans(principal(1), otherEntity, ledgerID, day("2026-01-10")); err != nil || len(spans) != 1 {
		t.Fatalf("other entity: got %+v, %v", spans, err)
	}
	scoped := tenant.With(context.Background(), tenant.Principal{TenantID: testkit.TenantID(1), LegalEntityID: entityID})
	if spans, err = s.PeriodSpans(scoped, otherEntity, ledgerID, day("2026-01-10")); err != nil || len(spans) != 0 {
		t.Fatalf("RLS must hide an entity outside the caller scope, got %+v, %v", spans, err)
	}
	if spans, err = s.PeriodSpans(principal(2), entityID, ledgerID, day("2026-01-10")); err != nil || len(spans) != 1 || spans[0].FiscalPeriodID == jan {
		t.Fatalf("tenant 2 sees only its own rows, got %+v, %v", spans, err)
	}
}

func TestRegisterPeriod_WritesDatesTheResolverReads(t *testing.T) {
	admin, app := testkit.DBWithApp(t)
	if err := dbx.Migrate(context.Background(), admin, migrations.FS); err != nil {
		t.Fatal(err)
	}
	s := store.New(app)
	day := func(d string) time.Time { v, _ := time.Parse("2006-01-02", d); return v }
	reg := func(period, start, end string) (bool, error) {
		return s.RegisterPeriod(principal(1), store.PeriodRegistration{LegalEntityID: entityID, LedgerID: ledgerID, FiscalPeriodID: period, Start: day(start), End: day(end), Actor: "test"})
	}
	jan, feb := "00000000-0000-0000-0000-0000000000b1", "00000000-0000-0000-0000-0000000000b2"
	if created, err := reg(jan, "2026-01-01", "2026-01-31"); err != nil || !created {
		t.Fatalf("first registration: %v %v", created, err)
	}
	if created, err := reg(jan, "2026-01-01", "2026-01-31"); err != nil || created {
		t.Fatalf("same dates replay: %v %v", created, err)
	}
	if _, err := reg(jan, "2026-01-01", "2026-01-30"); err == nil {
		t.Fatal("moving registered dates must be refused")
	}
	if _, err := reg(feb, "2026-01-31", "2026-02-28"); err == nil {
		t.Fatal("overlapping periods must be refused")
	}
	if created, err := reg(feb, "2026-02-01", "2026-02-28"); err != nil || !created {
		t.Fatalf("february: %v %v", created, err)
	}
	spans, err := s.PeriodSpans(principal(1), entityID, ledgerID, day("2026-01-31"))
	if err != nil || len(spans) != 1 || spans[0].FiscalPeriodID != jan {
		t.Fatalf("resolver must find January: %+v %v", spans, err)
	}
	if status, err := domain.Decide(spans[0].Rows, domain.ModuleAP, ""); err != nil || status != domain.StatusOpen {
		t.Fatalf("a registered period opens: %v %v", status, err)
	}
	if _, err := admin.Exec(context.Background(), `update period_status set start_date = null, end_date = null where fiscal_period_id = $1`, feb); err != nil {
		t.Fatal(err)
	}
	if created, err := reg(feb, "2026-02-01", "2026-02-28"); err != nil || created {
		t.Fatalf("undated period gets its dates: %v %v", created, err)
	}
	if spans, err = s.PeriodSpans(principal(1), entityID, ledgerID, day("2026-02-10")); err != nil || len(spans) != 1 {
		t.Fatalf("backfilled dates resolve: %+v %v", spans, err)
	}
}
