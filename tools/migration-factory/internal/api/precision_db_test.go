package api

import (
	"fmt"
	"net/http"
	"strings"
	"testing"
)

// legacy_xref.amount and the project_opening_line amounts are numeric(28,6). An open item or an
// opening line with a seventh decimal used to reconcile against its unrounded value in memory
// while Passage stored it rounded, so the first run reported RECONCILED and a re-run of the same
// extract read the rounded value back and reported MISMATCH (AC-49.4: totals must be unchanged).
// Such a value is now refused before anything is stored, as gl_balances already did.
func TestPackRun_AmountFinerThanStoredScaleRefused(t *testing.T) {
	h, admin := dbServer(t)
	fine := `{"legacyId":"OI-FINE","party":"V001","account":"2100","currency":"USD","amount":"10.2500004"}`
	rec := do(t, h, request{method: "POST", path: runsPath, tenant: tenantA, duties: allDuties, body: openItemsBody(3, fine)})
	if rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), "more than 6 decimal places") {
		t.Fatalf("open item with 7 decimals: %d %s", rec.Code, rec.Body)
	}
	if n := count(t, admin, `select count(*) from pack_run`); n != 0 {
		t.Fatalf("a refused open_items extract stores nothing: %d runs", n)
	}
	// Six decimals is the stored scale and still loads, reconciles and re-runs unchanged.
	six := `{"legacyId":"OI-SIX","party":"V001","account":"2100","currency":"USD","amount":"10.250001"}`
	first := decode[runDetailDTO](t, do(t, h, request{method: "POST", path: runsPath, tenant: tenantA, duties: allDuties, body: openItemsBody(3, six)}))
	again := decode[runDetailDTO](t, do(t, h, request{method: "POST", path: runsPath, tenant: tenantA, duties: allDuties, body: openItemsBody(3, six)}))
	if first.Status != "RECONCILED" || again.Status != "RECONCILED" || again.RecordsLoaded != 0 {
		t.Fatalf("six decimals: first %s, re-run %s loaded %d", first.Status, again.Status, again.RecordsLoaded)
	}
	for i := range first.ControlTotals {
		a, b := first.ControlTotals[i], again.ControlTotals[i]
		if a.Name != b.Name || a.Source != b.Source || a.Target != b.Target || a.Status != b.Status {
			t.Fatalf("AC-49.4 totals changed on re-run: %+v -> %+v", first.ControlTotals[i], again.ControlTotals[i])
		}
	}

	line := func(cost string) string {
		return fmt.Sprintf(`{"sourceSystem":"AX2012","sourceCompany":"USMF","cutoffTs":"2026-12-31T00:00:00Z","freezeTs":"2026-12-31T06:00:00Z",
			"projects":[{"projectId":"P-FINE","legacyTotals":[{"category":"HOUR","cost":%q,"sales":"0","wipCost":"0","wipSales":"0"}],
			"lines":[{"legacyId":"PL-1","category":"HOUR","transactionDate":"2026-12-01","qty":"1","costAmount":%q,"salesAmount":"0","currency":"USD","billingStatus":"NON_CHARGEABLE","wipCost":"0","wipSales":"0"}]}]}`, cost, cost)
	}
	rec = do(t, h, request{method: "POST", path: projectRunsPath, tenant: tenantA, duties: allDuties, body: line("12.0000004")})
	if rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), "more than 6 decimal places") {
		t.Fatalf("opening line with 7 decimals: %d %s", rec.Code, rec.Body)
	}
	if n := count(t, admin, `select count(*) from pack_run where pack = 'project_wip'`); n != 0 {
		t.Fatalf("a refused project_wip extract stores nothing: %d runs", n)
	}
}
