package api

import (
	"context"
	"net/http"
	"testing"

	"github.com/BenjaminSRussell/passage/platform/ledgerclient"

	"github.com/BenjaminSRussell/passage/tools/migration-factory/internal/domain"
	"github.com/BenjaminSRussell/passage/tools/migration-factory/internal/store"
)

func (u unreachableStore) RunStaged(context.Context, store.Idem, store.StagedRun) (domain.PackRun, bool, error) {
	u.fail()
	return domain.PackRun{}, false, nil
}
func (u unreachableStore) PostCompensations(context.Context, ledgerclient.Ledger, store.CompensationRun) (store.CompensationReport, error) {
	u.fail()
	return store.CompensationReport{}, nil
}
func (u unreachableStore) CompensationReport(context.Context, string) (store.CompensationReport, error) {
	u.fail()
	return store.CompensationReport{}, nil
}
func (u unreachableStore) ListCoexistence(context.Context, domain.SourceSystem, string) ([]domain.CoexistFlag, error) {
	u.fail()
	return nil, nil
}
func (u unreachableStore) SetCoexistence(context.Context, store.Idem, store.CoexistChange) (domain.CoexistFlag, bool, error) {
	u.fail()
	return domain.CoexistFlag{}, false, nil
}

// Slice 17 endpoints refuse a missing ledger, missing duties and bad input before the store.
func TestSlice17_RefusedBeforeTheStore(t *testing.T) {
	h := New(unreachableStore{t}).Routes()
	plan := "/api/v1/cutover/plans/00000000-0000-7000-8000-000000000001/rollback/compensations"
	cases := []struct {
		rq     request
		status int
		code   string
	}{
		{request{method: "POST", path: "/api/v1/migration/parallel-runs/replay", tenant: tenantA, duties: allDuties, body: `{}`}, http.StatusServiceUnavailable, CodeLedgerNotConfigured},
		{request{method: "POST", path: plan, tenant: tenantA, duties: allDuties, body: `{}`}, http.StatusServiceUnavailable, CodeLedgerNotConfigured},
		{request{method: "POST", path: "/api/v1/migration/parallel-runs/replay", tenant: tenantA, duties: "MigrationView", body: `{}`}, http.StatusForbidden, ""},
		{request{method: "GET", path: "/api/v1/migration/coexistence?sourceSystem=AX2012", tenant: tenantA, duties: allDuties}, http.StatusUnprocessableEntity, CodeInvalid},
		{request{method: "PUT", path: "/api/v1/migration/coexistence/AX2012/USMF/planet", tenant: tenantA, duties: allDuties, body: `{"mode":"PASSAGE"}`}, http.StatusUnprocessableEntity, CodeInvalid},
		{request{method: "PUT", path: "/api/v1/migration/coexistence/AX2012/USMF/customer", tenant: tenantA, duties: allDuties, body: `{"mode":"BOTH"}`}, http.StatusUnprocessableEntity, CodeInvalid},
		{request{method: "PUT", path: "/api/v1/migration/coexistence/AX2012/USMF/customer", tenant: tenantA, duties: "MigrationView", body: `{"mode":"PASSAGE"}`}, http.StatusForbidden, ""},
		{request{method: "POST", path: "/api/v1/migration/packs/fixed_assets/runs", tenant: tenantA, duties: allDuties,
			body: `{"sourceSystem":"AX2012","sourceCompany":"USMF","cutoffTs":"2026-12-31T00:00:00Z","freezeTs":"2026-12-31T06:00:00Z","master":[],"records":[{"legacyId":"A"}]}`}, http.StatusUnprocessableEntity, CodeInvalid},
	}
	for i, c := range cases {
		rec := do(t, h, c.rq)
		if rec.Code != c.status || (c.code != "" && problemCode(t, rec) != c.code) {
			t.Fatalf("case %d %s %s: %d %s", i, c.rq.method, c.rq.path, rec.Code, rec.Body)
		}
	}
	withLedger := New(unreachableStore{t}).WithLedger(ledgerclient.NewFake()).Routes()
	bad := `{"period":"2026-03","sourceSystem":"AX2012","legalEntityId":"00000000-0000-7000-8000-0000000000e1","ledgerId":"L1","revenue":"1",
		"documents":[{"seq":1,"documentId":"GJ-1","postingDate":"2026-03-01","currency":"USD","lines":[{"account":"1","amount":"1"},{"account":"2","amount":"-2"}]}]}`
	if rec := do(t, withLedger, request{method: "POST", path: "/api/v1/migration/parallel-runs/replay", tenant: tenantA, duties: allDuties, body: bad,
		le: "00000000-0000-7000-8000-0000000000e1"}); rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("unbalanced replay document: %d %s", rec.Code, rec.Body)
	}
	other := `{"period":"2026-03","sourceSystem":"AX2012","legalEntityId":"00000000-0000-7000-8000-0000000000e2","ledgerId":"L1","revenue":"1","documents":[]}`
	if rec := do(t, withLedger, request{method: "POST", path: "/api/v1/migration/parallel-runs/replay", tenant: tenantA, duties: allDuties, body: other,
		le: "00000000-0000-7000-8000-0000000000e1"}); rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("legal entity outside the caller's scope: %d %s", rec.Code, rec.Body)
	}
}
