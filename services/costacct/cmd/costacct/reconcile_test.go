package main

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/costacct/internal/domain"
	"github.com/BenjaminSRussell/passage/services/costacct/internal/store"
)

const (
	tenantA  = "00000000-0000-4000-8000-0000000000a1"
	entityA  = "0196a6a4-0000-7000-8000-00000000000a"
	entityB  = "0196a6a4-0000-7000-8000-00000000000b"
	ledgerA  = "0196a6a4-0000-7000-8000-0000000000a1"
	ledgerB  = "0196a6a4-0000-7000-8000-0000000000b1"
	elemCost = "0196a6a4-0000-7000-8000-0000000000c1"
)

type fakeReconciler struct {
	entities   []string
	results    map[string]domain.Reconciliation
	failFor    map[string]bool
	calls      []store.GLReconcileRequest
	principals []tenant.Principal
}

func (f *fakeReconciler) LegalEntities(ctx context.Context) ([]string, error) {
	p, _ := tenant.FromContext(ctx)
	f.principals = append(f.principals, p)
	return f.entities, nil
}

func (f *fakeReconciler) ReconcileToGL(_ context.Context, _ store.GLBalances, in store.GLReconcileRequest) (domain.Reconciliation, error) {
	f.calls = append(f.calls, in)
	if f.failFor[in.LedgerID] {
		return domain.Reconciliation{}, errors.New("ledger down")
	}
	return f.results[in.LegalEntityID], nil
}

func reconciled() domain.Reconciliation {
	return domain.Reconciliation{Reconciled: true, Lines: []domain.ReconLine{{ElementID: elemCost}}}
}

func differing() domain.Reconciliation {
	return domain.Reconciliation{Lines: []domain.ReconLine{{ElementID: elemCost, Journal: decimal.RequireFromString("150.00"), Difference: decimal.RequireFromString("150.00")}}}
}

func TestReconcileTenants_ReconcilesEveryEntityAndPeriodAsTheJobPrincipal(t *testing.T) {
	st := &fakeReconciler{entities: []string{entityA, entityB}, results: map[string]domain.Reconciliation{entityA: reconciled(), entityB: reconciled()}}
	err := reconcileTenants(context.Background(), st, nil, map[string]string{entityA: ledgerA, entityB: ledgerB}, []string{tenantA}, []string{"2026-08", "2026-09"})
	if err != nil || len(st.calls) != 4 {
		t.Fatalf("err %v, %d reconciliations, want nil and 4", err, len(st.calls))
	}
	if p := st.principals[0]; p.TenantID != tenantA || p.UserID != reconcileActor || !p.HasDuty(tenant.DutyAllScope) {
		t.Errorf("principal %+v", p)
	}
	if st.calls[0].Currency != "" || st.calls[0].LedgerID != ledgerA {
		t.Errorf("first call %+v takes the ledger's currency and the mapped ledger", st.calls[0])
	}
}

func TestReconcileTenants_FailsOnADifferenceAnUnmappedEntityAndALedgerError(t *testing.T) {
	cases := []struct {
		name      string
		st        *fakeReconciler
		ledgerIDs map[string]string
		wantCalls int
	}{
		{"difference", &fakeReconciler{entities: []string{entityA}, results: map[string]domain.Reconciliation{entityA: differing()}}, map[string]string{entityA: ledgerA}, 2},
		{"unmapped entity", &fakeReconciler{entities: []string{entityA}, results: map[string]domain.Reconciliation{entityA: reconciled()}}, map[string]string{}, 0},
		{"ledger error keeps going", &fakeReconciler{entities: []string{entityA, entityB}, failFor: map[string]bool{ledgerA: true},
			results: map[string]domain.Reconciliation{entityB: reconciled()}}, map[string]string{entityA: ledgerA, entityB: ledgerB}, 4},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := reconcileTenants(context.Background(), c.st, nil, c.ledgerIDs, []string{tenantA}, []string{"2026-08", "2026-09"})
			if err == nil || len(c.st.calls) != c.wantCalls {
				t.Fatalf("err %v after %d reconciliations, want an error after %d", err, len(c.st.calls), c.wantCalls)
			}
		})
	}
}

func TestReconcilePeriods_TheMonthBeforeAndTheCurrentOne(t *testing.T) {
	for now, want := range map[time.Time][]string{
		time.Date(2026, 1, 31, 23, 0, 0, 0, time.UTC): {"2025-12", "2026-01"},
		time.Date(2026, 3, 31, 1, 0, 0, 0, time.UTC):  {"2026-02", "2026-03"},
	} {
		if got := reconcilePeriods(now); got[0] != want[0] || got[1] != want[1] {
			t.Errorf("%s: got %v, want %v", now.Format(time.DateOnly), got, want)
		}
	}
}

func TestParseLedgerIDs_RejectsAMalformedPair(t *testing.T) {
	if got, err := parseLedgerIDs(entityA + "=" + ledgerA + " , " + entityB + "=" + ledgerB); err != nil || got[entityB] != ledgerB {
		t.Fatalf("valid pairs: %v, %v", got, err)
	}
	if _, err := parseLedgerIDs(entityA + "=nope"); err == nil {
		t.Fatal("a ledger that is not a UUID was accepted")
	}
}

func TestCostGLReconcile_RequiresTenantsDatabaseAndLedger(t *testing.T) {
	none := func(string) string { return "" }
	if err := costGLReconcile(context.Background(), none, nil); err == nil {
		t.Error("no tenant accepted")
	}
	if err := costGLReconcile(context.Background(), none, []string{"nope"}); err == nil {
		t.Error("a tenant that is not a UUID accepted")
	}
	if err := costGLReconcile(context.Background(), none, []string{tenantA}); err == nil {
		t.Error("a missing DATABASE_URL accepted")
	}
	env := map[string]string{"DATABASE_URL": "postgres://unused"}
	if err := costGLReconcile(context.Background(), func(k string) string { return env[k] }, []string{tenantA}); err == nil {
		t.Error("a missing LEDGER_URL accepted")
	}
}
