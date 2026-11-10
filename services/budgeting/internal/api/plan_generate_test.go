package api

import (
	"net/http"
	"strings"
	"testing"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/services/budgeting/internal/domain"
	"github.com/BenjaminSRussell/passage/services/budgeting/internal/store"
)

const generatePath = planPath + "/scenarios/" + scenarioID + ":generate"

func draftPlan() store.Plan {
	return store.Plan{ID: planID, Status: domain.PlanDraft, FiscalYear: 2027, LegalEntityID: entityID, LedgerID: ledgerID}
}

func TestPlanGenerate_PriorPlanMovesTheMonthsToThisYearAndScales(t *testing.T) {
	f := &fakePlans{plan: draftPlan(), prior: []domain.PlanCell{
		{Account: "6100", CostCenter: "CC1", Period: "2026-01", Amount: decimal.RequireFromString("1000")},
		{Account: "6100", CostCenter: "CC1", Period: "2026-12", Amount: decimal.RequireFromString("200.5")},
	}}
	rec := servePlan(t, f, nil, http.MethodPost, generatePath, `{"source":"prior-plan","adjustmentPct":"10"}`, dutyPlanMaintain, map[string]string{"If-Match": `"2"`})
	if rec.Code != http.StatusOK || f.version != 2 || len(f.cells) != 2 {
		t.Fatalf("generate: %d %s version %d cells %d", rec.Code, rec.Body, f.version, len(f.cells))
	}
	if f.cells[0].Period != "2027-01" || !f.cells[0].Amount.Equal(decimal.RequireFromString("1100")) || f.cells[1].Period != "2027-12" || !f.cells[1].Amount.Equal(decimal.RequireFromString("220.55")) {
		t.Fatalf("cells = %+v, want 2027-01 1100 and 2027-12 220.55", f.cells)
	}
}

func TestPlanGenerate_GLActualsSharesLastYearsMonthsOverCostCenters(t *testing.T) {
	f := &fakePlans{plan: draftPlan()}
	gl := &fakeActuals{byPeriod: map[string]string{"2026-01": "100", "2026-02": "300"}}
	body := `{"source":"gl-actuals","adjustmentPct":"-50","accounts":["6000"],"costCenters":[{"costCenter":"CC1","weight":"1"},{"costCenter":"CC2","weight":"1"}]}`
	rec := servePlan(t, f, gl, http.MethodPost, generatePath, body, dutyPlanMaintain, map[string]string{"If-Match": `"1"`})
	if rec.Code != http.StatusOK || len(f.cells) != 24 {
		t.Fatalf("generate: %d %s cells %d, want 24", rec.Code, rec.Body, len(f.cells))
	}
	got := map[string]string{}
	for _, c := range f.cells {
		got[c.CostCenter+c.Period] = c.Amount.String()
	}
	if got["CC12027-01"] != "25" || got["CC22027-01"] != "25" || got["CC12027-02"] != "75" || got["CC12027-03"] != "0" {
		t.Fatalf("cells = %v, want half of last year's months shared evenly", got)
	}
}

func TestPlanGenerate_NothingPostedLastYearWritesNothingAndSaysSo(t *testing.T) {
	f := &fakePlans{plan: draftPlan()}
	body := `{"source":"gl-actuals","accounts":["6000"],"costCenters":[{"costCenter":"CC1","weight":"1"}]}`
	rec := servePlan(t, f, &fakeActuals{byPeriod: map[string]string{}}, http.MethodPost, generatePath, body, dutyPlanMaintain, map[string]string{"If-Match": `"1"`})
	if rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), codeNoSourceData) || len(f.cells) != 0 {
		t.Fatalf("empty source: %d %s cells %d, want 422 %s and no write", rec.Code, rec.Body, len(f.cells), codeNoSourceData)
	}
	f2 := &fakePlans{plan: draftPlan()}
	rec = servePlan(t, f2, nil, http.MethodPost, generatePath, `{"source":"prior-plan"}`, dutyPlanMaintain, map[string]string{"If-Match": `"1"`})
	if rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), codeNoSourceData) {
		t.Fatalf("no approved plan: %d %s", rec.Code, rec.Body)
	}
}

func TestPlanGenerate_ValidatesTheRequestAndNeedsTheMaintainDuty(t *testing.T) {
	cases := map[string]string{
		"unknown source":      `{"source":"magic"}`,
		"adjustment too low":  `{"source":"prior-plan","adjustmentPct":"-100"}`,
		"adjustment too high": `{"source":"prior-plan","adjustmentPct":"1000.5"}`,
		"gl without accounts": `{"source":"gl-actuals","costCenters":[{"costCenter":"CC1","weight":"1"}]}`,
		"gl without centers":  `{"source":"gl-actuals","accounts":["6000"]}`,
		"centers on a plan":   `{"source":"prior-plan","costCenters":[{"costCenter":"CC1","weight":"1"}]}`,
		"duplicate account":   `{"source":"prior-plan","accounts":["6000","6000"]}`,
		"too many cells":      `{"source":"gl-actuals","accounts":["` + strings.Join(manyAccounts(50), `","`) + `"],"costCenters":[` + manyCenters(9) + `]}`,
	}
	for name, body := range cases {
		f := &fakePlans{plan: draftPlan()}
		if rec := servePlan(t, f, &fakeActuals{}, http.MethodPost, generatePath, body, dutyPlanMaintain, map[string]string{"If-Match": `"1"`}); rec.Code != http.StatusBadRequest || len(f.cells) != 0 {
			t.Errorf("%s: %d %s, want 400 and no write", name, rec.Code, rec.Body)
		}
	}
	if rec := servePlan(t, &fakePlans{plan: draftPlan()}, nil, http.MethodPost, generatePath, `{"source":"prior-plan"}`, "budget.view", map[string]string{"If-Match": `"1"`}); rec.Code != http.StatusForbidden {
		t.Errorf("without the maintain duty: %d, want 403", rec.Code)
	}
	if rec := servePlan(t, &fakePlans{plan: draftPlan()}, nil, http.MethodPost, generatePath, `{"source":"prior-plan"}`, dutyPlanMaintain, nil); rec.Code != http.StatusPreconditionRequired {
		t.Errorf("without If-Match: %d, want 428", rec.Code)
	}
}

func manyAccounts(n int) []string {
	out := make([]string, n)
	for i := range out {
		out[i] = "A" + strings.Repeat("x", i)
	}
	return out
}

func manyCenters(n int) string {
	parts := make([]string, n)
	for i := range parts {
		parts[i] = `{"costCenter":"C` + strings.Repeat("x", i) + `","weight":"1"}`
	}
	return strings.Join(parts, ",")
}
