package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/ledgerclient"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/budgeting/internal/domain"
	"github.com/BenjaminSRussell/passage/services/budgeting/internal/store"
)

const (
	planID     = "0196a6a4-0000-7000-8000-000000000011"
	scenarioID = "0196a6a4-0000-7000-8000-000000000012"
	orgUnitID  = "0196a6a4-0000-7000-8000-000000000013"
	ledgerID   = "0196a6a4-0000-7000-8000-000000000014"
	entityID   = "0196a6a4-0000-7000-8000-000000000015"
)

type fakePlans struct {
	calls     int
	version   int64
	cells     []domain.PlanCell
	signOff   store.SignOffRequest
	reject    store.RejectRequest
	newScen   store.NewScenario
	owners    []store.StageOwnerInput
	plan      store.Plan
	planError error
	replayed  bool
	fallback  bool
	prior     []domain.PlanCell
}

func (f *fakePlans) Replayed(context.Context, string, string) (bool, error) { return f.replayed, nil }

func (f *fakePlans) CreatePlan(context.Context, string, string, store.NewPlan) (int, []byte, error) {
	f.calls++
	return http.StatusCreated, []byte(`{"id":"p"}`), nil
}

func (f *fakePlans) SetStageOwners(_ context.Context, _, _, _ string, o []store.StageOwnerInput) (int, []byte, error) {
	f.calls++
	f.owners = o
	return http.StatusOK, []byte(`{}`), nil
}

func (f *fakePlans) CreateScenario(_ context.Context, _, _, _ string, in store.NewScenario) (int, []byte, error) {
	f.calls++
	f.newScen = in
	return http.StatusCreated, []byte(`{}`), nil
}

func (f *fakePlans) UpsertCells(_ context.Context, _, _, _, _ string, version int64, cells []domain.PlanCell) (int, []byte, error) {
	f.calls++
	f.version, f.cells = version, cells
	return http.StatusOK, []byte(`{"scenarioId":"s","version":8,"cells":1,"total":"1"}`), nil
}

func (f *fakePlans) SpreadCells(ctx context.Context, key, hash, planID, scenarioID string, version int64, cells []domain.PlanCell, priorYearFallback bool) (int, []byte, error) {
	f.fallback = priorYearFallback
	return f.UpsertCells(ctx, key, hash, planID, scenarioID, version, cells)
}

func (f *fakePlans) SubmitPlan(context.Context, string, string, string) (int, []byte, error) {
	f.calls++
	return http.StatusOK, []byte(`{}`), nil
}

func (f *fakePlans) SignOff(_ context.Context, _, _ string, in store.SignOffRequest) (int, []byte, error) {
	f.calls++
	f.signOff = in
	return http.StatusOK, []byte(`{}`), nil
}

func (f *fakePlans) RejectPlan(_ context.Context, _, _ string, in store.RejectRequest) (int, []byte, error) {
	f.calls++
	f.reject = in
	return http.StatusOK, []byte(`{}`), nil
}

func (f *fakePlans) ListPlans(context.Context, string) ([]store.Plan, error) {
	f.calls++
	return []store.Plan{}, nil
}

func (f *fakePlans) GetPlan(context.Context, string) (store.PlanView, error) {
	f.calls++
	return store.PlanView{Plan: f.plan}, f.planError
}

func (f *fakePlans) GetGrid(context.Context, string, string, store.GridFilter) (store.Grid, error) {
	f.calls++
	return store.Grid{Scenario: store.Scenario{Version: 5}, Cells: []domain.PlanCell{}}, nil
}

func (f *fakePlans) CompareScenarios(_ context.Context, _, baseID, otherID, groupBy string) (store.Comparison, error) {
	f.calls++
	return store.Comparison{GroupBy: groupBy, Base: store.Scenario{ID: baseID}, Other: store.Scenario{ID: otherID}, Rows: []store.ComparisonRow{}}, nil
}

func (f *fakePlans) PriorPlanCells(context.Context, string, []string) ([]domain.PlanCell, error) {
	f.calls++
	return f.prior, nil
}

type fakeActuals struct {
	byPeriod map[string]string
	queries  []ledgerclient.BalanceQuery
	err      error
}

func (f *fakeActuals) Balances(_ context.Context, q ledgerclient.BalanceQuery) (ledgerclient.AccountBalances, error) {
	f.queries = append(f.queries, q)
	if f.err != nil {
		return ledgerclient.AccountBalances{}, f.err
	}
	amount, ok := f.byPeriod[q.PeriodFrom]
	if !ok {
		return ledgerclient.AccountBalances{}, nil
	}
	return ledgerclient.AccountBalances{Rows: []ledgerclient.AccountBalance{{Account: q.Account, Debit: decimal.RequireFromString(amount)}}}, nil
}

func servePlan(t *testing.T, ps PlanStore, gl PriorActuals, method, path, body, duties string, headers map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	r := chi.NewRouter()
	New(&fakeStore{}).WithPlanning(ps, gl).Register(r)
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(idempotencyKey, "k-1")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	req = req.WithContext(testkit.Ctx(tenantID, strings.Split(duties, ",")...))
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	return rec
}

const planPath = "/v1/budget/plans/" + planID
const gridPath = planPath + "/scenarios/" + scenarioID + "/grid"

func TestPlan_RoutesNeedTheirOwnDuty(t *testing.T) {
	body := `{"cells":[{"account":"6000","costCenter":"CC1","period":"2027-01","amount":"1"}]}`
	cases := []struct {
		method, path, body, duty string
	}{
		{http.MethodPut, gridPath, body, dutyPlanMaintain},
		{http.MethodPost, planPath + ":submit", ``, dutyPlanMaintain},
		{http.MethodPost, planPath + "/stages/review:sign-off", `{"orgUnitId":"` + orgUnitID + `","submission":1}`, dutyPlanWorkflow},
		{http.MethodPost, planPath + ":reject", `{"reason":"no","submission":1}`, dutyPlanWorkflow},
		{http.MethodGet, planPath, ``, dutyView},
		{http.MethodGet, gridPath, ``, dutyView},
	}
	for _, c := range cases {
		t.Run(c.method+" "+c.path, func(t *testing.T) {
			h := map[string]string{"If-Match": `"1"`}
			if rec := servePlan(t, &fakePlans{}, nil, c.method, c.path, c.body, "budget.check", h); rec.Code != http.StatusForbidden {
				t.Fatalf("without the duty: %d, want 403", rec.Code)
			}
			if rec := servePlan(t, &fakePlans{}, nil, c.method, c.path, c.body, c.duty, h); rec.Code != http.StatusOK {
				t.Fatalf("with %s: %d %s, want 200", c.duty, rec.Code, rec.Body)
			}
		})
	}
}

func TestPlan_HumanDutyCannotCallTheWorkflowRoutes(t *testing.T) {
	rec := servePlan(t, &fakePlans{}, nil, http.MethodPost, planPath+"/stages/approve:sign-off", `{"orgUnitId":"`+orgUnitID+`","scenarioId":"`+scenarioID+`","submission":1}`, "budget.plan.maintain,budget.view", nil)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("a maintainer signing off directly: %d, want 403", rec.Code)
	}
}

func TestPlanGrid_PutRequiresIfMatchAndPassesTheVersion(t *testing.T) {
	body := `{"cells":[{"account":"6000","costCenter":"CC1","period":"2027-01","amount":"12.5"}]}`
	f := &fakePlans{}
	rec := servePlan(t, f, nil, http.MethodPut, gridPath, body, dutyPlanMaintain, nil)
	if rec.Code != http.StatusPreconditionRequired || problemCode(t, rec) != codePreconditionReq || f.calls != 0 {
		t.Fatalf("without If-Match: %d %s, want 428 and no write", rec.Code, rec.Body)
	}
	rec = servePlan(t, f, nil, http.MethodPut, gridPath, body, dutyPlanMaintain, map[string]string{"If-Match": `"7"`})
	if rec.Code != http.StatusOK || f.version != 7 || len(f.cells) != 1 || !f.cells[0].Amount.Equal(decimal.RequireFromString("12.5")) {
		t.Fatalf("with If-Match: %d version %d cells %+v", rec.Code, f.version, f.cells)
	}
	if got := rec.Header().Get("ETag"); got != `"8"` {
		t.Fatalf("ETag = %q, want the new grid version \"8\"", got)
	}
	rec = servePlan(t, f, nil, http.MethodPut, gridPath, body, dutyPlanMaintain, map[string]string{"If-Match": "abc"})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("malformed If-Match: %d, want 400", rec.Code)
	}
}

func TestPlanGrid_GetCarriesTheETag(t *testing.T) {
	rec := servePlan(t, &fakePlans{}, nil, http.MethodGet, gridPath, ``, dutyView, nil)
	if rec.Code != http.StatusOK || rec.Header().Get("ETag") != `"5"` {
		t.Fatalf("grid read: %d ETag %q, want \"5\"", rec.Code, rec.Header().Get("ETag"))
	}
}

func TestPlanGrid_PutRejectsBadCells(t *testing.T) {
	cases := map[string]string{
		"empty":            `{"cells":[]}`,
		"negative":         `{"cells":[{"account":"6000","costCenter":"CC1","period":"2027-01","amount":"-1"}]}`,
		"five decimals":    `{"cells":[{"account":"6000","costCenter":"CC1","period":"2027-01","amount":"1.00001"}]}`,
		"bad period":       `{"cells":[{"account":"6000","costCenter":"CC1","period":"2027-1","amount":"1"}]}`,
		"missing account":  `{"cells":[{"costCenter":"CC1","period":"2027-01","amount":"1"}]}`,
		"duplicate":        `{"cells":[{"account":"6000","costCenter":"CC1","period":"2027-01","amount":"1"},{"account":"6000","costCenter":"CC1","period":"2027-01","amount":"2"}]}`,
		"unknown property": `{"cells":[{"account":"6000","costCenter":"CC1","period":"2027-01","amount":"1","note":"x"}]}`,
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			f := &fakePlans{}
			rec := servePlan(t, f, nil, http.MethodPut, gridPath, body, dutyPlanMaintain, map[string]string{"If-Match": `"1"`})
			if rec.Code != http.StatusBadRequest || f.calls != 0 {
				t.Fatalf("%d %s calls %d, want 400 and no write", rec.Code, rec.Body, f.calls)
			}
		})
	}
}

func TestPlanSpread_EvenAllocatesTheTotalExactly(t *testing.T) {
	f := &fakePlans{plan: store.Plan{ID: planID, Status: domain.PlanDraft, FiscalYear: 2027}}
	body := `{"account":"6000","total":"1000","method":"even","costCenters":[{"costCenter":"CC1","weight":"1"},{"costCenter":"CC2","weight":"2"}]}`
	rec := servePlan(t, f, nil, http.MethodPost, planPath+"/scenarios/"+scenarioID+":spread", body, dutyPlanMaintain, map[string]string{"If-Match": `"3"`})
	if rec.Code != http.StatusOK || f.version != 3 || len(f.cells) != 24 {
		t.Fatalf("spread: %d %s version %d cells %d, want 24 cells", rec.Code, rec.Body, f.version, len(f.cells))
	}
	totals := map[string]decimal.Decimal{}
	sum := decimal.Zero
	for _, c := range f.cells {
		totals[c.CostCenter] = totals[c.CostCenter].Add(c.Amount)
		sum = sum.Add(c.Amount)
	}
	if !sum.Equal(decimal.NewFromInt(1000)) || !totals["CC1"].Equal(decimal.RequireFromString("333.3334")) && !totals["CC1"].Equal(decimal.RequireFromString("333.3333")) {
		t.Fatalf("spread totals %v sum %s, want 1000 split 1:2", totals, sum)
	}
}

func TestPlanSpread_PriorYearActualsWeighsPeriodsByTheLedger(t *testing.T) {
	f := &fakePlans{plan: store.Plan{ID: planID, Status: domain.PlanDraft, FiscalYear: 2027, LegalEntityID: entityID, LedgerID: ledgerID}}
	gl := &fakeActuals{byPeriod: map[string]string{"2026-01": "100", "2026-02": "300"}}
	body := `{"account":"6000","total":"400","method":"prior-year-actuals","costCenters":[{"costCenter":"CC1","weight":"1"}]}`
	rec := servePlan(t, f, gl, http.MethodPost, planPath+"/scenarios/"+scenarioID+":spread", body, dutyPlanMaintain, map[string]string{"If-Match": `"1"`})
	if rec.Code != http.StatusOK {
		t.Fatalf("spread: %d %s", rec.Code, rec.Body)
	}
	want := map[string]string{"2027-01": "100", "2027-02": "300", "2027-03": "0"}
	for _, c := range f.cells {
		if w, ok := want[c.Period]; ok && !c.Amount.Equal(decimal.RequireFromString(w)) {
			t.Errorf("%s = %s, want %s", c.Period, c.Amount, w)
		}
	}
	if len(gl.queries) != 12 || gl.queries[0].PeriodFrom != "2026-01" || gl.queries[0].LedgerID != ledgerID || gl.queries[0].LegalEntityID != entityID {
		t.Fatalf("ledger queries = %+v, want the 12 months of 2026 on the plan's ledger", gl.queries)
	}
}

func TestPlanSpread_PriorYearNeedsLedgerAndPlanLedger(t *testing.T) {
	body := `{"account":"6000","total":"400","method":"prior-year-actuals","costCenters":[{"costCenter":"CC1","weight":"1"}]}`
	h := map[string]string{"If-Match": `"1"`}
	noLedgerOnPlan := &fakePlans{plan: store.Plan{ID: planID, Status: domain.PlanDraft, FiscalYear: 2027}}
	if rec := servePlan(t, noLedgerOnPlan, &fakeActuals{}, http.MethodPost, planPath+"/scenarios/"+scenarioID+":spread", body, dutyPlanMaintain, h); rec.Code != http.StatusUnprocessableEntity || problemCode(t, rec) != codeLedgerNotSet {
		t.Fatalf("plan without a ledger: %d %s, want 422 %s", rec.Code, rec.Body, codeLedgerNotSet)
	}
	withLedger := &fakePlans{plan: store.Plan{ID: planID, Status: domain.PlanDraft, FiscalYear: 2027, LedgerID: ledgerID}}
	if rec := servePlan(t, withLedger, nil, http.MethodPost, planPath+"/scenarios/"+scenarioID+":spread", body, dutyPlanMaintain, h); rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("service without a ledger client: %d, want 503", rec.Code)
	}
	failing := &fakeActuals{err: context.DeadlineExceeded}
	rec := servePlan(t, withLedger, failing, http.MethodPost, planPath+"/scenarios/"+scenarioID+":spread", body, dutyPlanMaintain, h)
	if rec.Code != http.StatusServiceUnavailable || problemCode(t, rec) != codeLedgerUnreadable || withLedger.version != 0 {
		t.Fatalf("ledger down: %d %s, want 503 %s and no write", rec.Code, rec.Body, codeLedgerUnreadable)
	}
}

func TestPlanSpread_ARetryOfAStoredAnswerDoesNotReadTheLedger(t *testing.T) {
	f := &fakePlans{plan: store.Plan{ID: planID, Status: domain.PlanDraft, FiscalYear: 2027, LedgerID: ledgerID}, replayed: true}
	gl := &fakeActuals{err: context.DeadlineExceeded}
	body := `{"account":"6000","total":"400","method":"prior-year-actuals","costCenters":[{"costCenter":"CC1","weight":"1"}]}`
	rec := servePlan(t, f, gl, http.MethodPost, planPath+"/scenarios/"+scenarioID+":spread", body, dutyPlanMaintain, map[string]string{"If-Match": `"1"`})
	if rec.Code != http.StatusOK || len(gl.queries) != 0 || f.calls != 1 {
		t.Fatalf("replayed spread: %d %s, ledger queries %d, store calls %d, want 200, 0 and 1", rec.Code, rec.Body, len(gl.queries), f.calls)
	}
}

func TestPlanSpread_ANetCreditMonthWeighsNothing(t *testing.T) {
	f := &fakePlans{plan: store.Plan{ID: planID, Status: domain.PlanDraft, FiscalYear: 2027, LedgerID: ledgerID}}
	gl := &fakeActuals{byPeriod: map[string]string{"2026-01": "100", "2026-02": "-300"}}
	body := `{"account":"6000","total":"100","method":"prior-year-actuals","costCenters":[{"costCenter":"CC1","weight":"1"}]}`
	rec := servePlan(t, f, gl, http.MethodPost, planPath+"/scenarios/"+scenarioID+":spread", body, dutyPlanMaintain, map[string]string{"If-Match": `"1"`})
	if rec.Code != http.StatusOK {
		t.Fatalf("spread: %d %s", rec.Code, rec.Body)
	}
	for _, c := range f.cells {
		if c.Period == "2027-01" && !c.Amount.Equal(decimal.NewFromInt(100)) || c.Period == "2027-02" && !c.Amount.IsZero() {
			t.Errorf("%s = %s, want the credit month at 0 and all of the total in January", c.Period, c.Amount)
		}
	}
}

func TestPlanSpread_RefusesWhatItWouldSpreadWrongBeforeReadingTheLedger(t *testing.T) {
	path := planPath + "/scenarios/" + scenarioID + ":spread"
	h := map[string]string{"If-Match": `"1"`}
	cases := []struct {
		name, body string
		plan       store.Plan
		code       int
		problem    string
	}{
		{"duplicate cost centers", `{"account":"6000","total":"400","method":"prior-year-actuals","costCenters":[{"costCenter":"CC1","weight":"1"},{"costCenter":"CC1","weight":"2"}]}`,
			store.Plan{ID: planID, Status: domain.PlanDraft, FiscalYear: 2027, LedgerID: ledgerID}, http.StatusBadRequest, "COM-0001"},
		{"weights summing to zero", `{"account":"6000","total":"400","method":"prior-year-actuals","costCenters":[{"costCenter":"CC1","weight":"0"},{"costCenter":"CC2","weight":"0"}]}`,
			store.Plan{ID: planID, Status: domain.PlanDraft, FiscalYear: 2027, LedgerID: ledgerID}, http.StatusUnprocessableEntity, codeZeroWeights},
		{"a plan that is not a Draft", `{"account":"6000","total":"400","method":"prior-year-actuals","costCenters":[{"costCenter":"CC1","weight":"1"}]}`,
			store.Plan{ID: planID, Status: domain.PlanSubmitted, FiscalYear: 2027, LedgerID: ledgerID}, http.StatusConflict, store.CodePlanNotDraft},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f, gl := &fakePlans{plan: c.plan}, &fakeActuals{}
			rec := servePlan(t, f, gl, http.MethodPost, path, c.body, dutyPlanMaintain, h)
			if rec.Code != c.code || problemCode(t, rec) != c.problem || len(gl.queries) != 0 || f.version != 0 {
				t.Fatalf("%s: %d %s, ledger queries %d, want %d %s with no ledger read and no write", c.name, rec.Code, rec.Body, len(gl.queries), c.code, c.problem)
			}
		})
	}
}

func TestPlanSpread_NoPriorYearActualsFallsBackToEvenAndSaysSo(t *testing.T) {
	f := &fakePlans{plan: store.Plan{ID: planID, Status: domain.PlanDraft, FiscalYear: 2027, LedgerID: ledgerID}}
	body := `{"account":"6000","total":"1200","method":"prior-year-actuals","costCenters":[{"costCenter":"CC1","weight":"1"}]}`
	rec := servePlan(t, f, &fakeActuals{}, http.MethodPost, planPath+"/scenarios/"+scenarioID+":spread", body, dutyPlanMaintain, map[string]string{"If-Match": `"1"`})
	if rec.Code != http.StatusOK || !f.fallback || len(f.cells) != 12 || !f.cells[0].Amount.Equal(decimal.NewFromInt(100)) {
		t.Fatalf("spread without actuals: %d %s fallback %v cells %v, want the even split flagged", rec.Code, rec.Body, f.fallback, f.cells)
	}
	f2 := &fakePlans{plan: f.plan}
	gl := &fakeActuals{byPeriod: map[string]string{"2026-01": "100"}}
	if rec := servePlan(t, f2, gl, http.MethodPost, planPath+"/scenarios/"+scenarioID+":spread", body, dutyPlanMaintain, map[string]string{"If-Match": `"1"`}); rec.Code != http.StatusOK || f2.fallback {
		t.Fatalf("spread with actuals: %d %s fallback %v, want no fallback", rec.Code, rec.Body, f2.fallback)
	}
}

func TestPlanCreate_RefusesAFiscalYearThatDoesNotStartInJanuary(t *testing.T) {
	f := &fakePlans{}
	body := `{"legalEntityId":"` + entityID + `","ruleVersionId":"` + scenarioID + `","name":"FY27","fiscalYear":2027,"fiscalYearStartMonth":7}`
	rec := servePlan(t, f, nil, http.MethodPost, "/v1/budget/plans", body, dutyPlanMaintain, nil)
	if rec.Code != http.StatusUnprocessableEntity || problemCode(t, rec) != codeFiscalCalendar || f.calls != 0 {
		t.Fatalf("july fiscal year: %d %s, want 422 %s and no plan", rec.Code, rec.Body, codeFiscalCalendar)
	}
	ok := strings.Replace(body, `,"fiscalYearStartMonth":7`, `,"fiscalYearStartMonth":1`, 1)
	if rec := servePlan(t, f, nil, http.MethodPost, "/v1/budget/plans", ok, dutyPlanMaintain, nil); rec.Code != http.StatusCreated {
		t.Fatalf("january fiscal year: %d %s, want 201", rec.Code, rec.Body)
	}
}

func TestPlanSignOff_ValidatesStageAndCarriesTheSubmission(t *testing.T) {
	f := &fakePlans{}
	body := `{"orgUnitId":"` + orgUnitID + `","scenarioId":"` + scenarioID + `","submission":4}`
	rec := servePlan(t, f, nil, http.MethodPost, planPath+"/stages/approve:sign-off", body, dutyPlanWorkflow, nil)
	if rec.Code != http.StatusOK || f.signOff.Stage != domain.StageApprove || f.signOff.Submission != 4 || f.signOff.ScenarioID != scenarioID || f.signOff.PlanID != planID {
		t.Fatalf("approve sign-off: %d %+v", rec.Code, f.signOff)
	}
	for name, c := range map[string]struct{ stage, body string }{
		"approve without a scenario": {"approve", `{"orgUnitId":"` + orgUnitID + `","submission":1}`},
		"review with a scenario":     {"review", `{"orgUnitId":"` + orgUnitID + `","scenarioId":"` + scenarioID + `","submission":1}`},
		"no submission":              {"review", `{"orgUnitId":"` + orgUnitID + `"}`},
		"bad org unit":               {"review", `{"orgUnitId":"x","submission":1}`},
	} {
		t.Run(name, func(t *testing.T) {
			g := &fakePlans{}
			if rec := servePlan(t, g, nil, http.MethodPost, planPath+"/stages/"+c.stage+":sign-off", c.body, dutyPlanWorkflow, nil); rec.Code != http.StatusBadRequest || g.calls != 0 {
				t.Fatalf("%d %s, want 400", rec.Code, rec.Body)
			}
		})
	}
	if rec := servePlan(t, &fakePlans{}, nil, http.MethodPost, planPath+"/stages/final:sign-off", body, dutyPlanWorkflow, nil); rec.Code != http.StatusNotFound {
		t.Fatalf("unknown stage: %d, want 404", rec.Code)
	}
}

func TestPlanReject_NeedsReasonAndSubmission(t *testing.T) {
	f := &fakePlans{}
	if rec := servePlan(t, f, nil, http.MethodPost, planPath+":reject", `{"reason":" too high ","submission":2}`, dutyPlanWorkflow, nil); rec.Code != http.StatusOK || f.reject.Reason != "too high" || f.reject.Submission != 2 {
		t.Fatalf("reject: %d %+v", rec.Code, f.reject)
	}
	if rec := servePlan(t, &fakePlans{}, nil, http.MethodPost, planPath+":reject", `{"reason":"","submission":2}`, dutyPlanWorkflow, nil); rec.Code != http.StatusBadRequest {
		t.Fatalf("empty reason: %d, want 400", rec.Code)
	}
}

func TestPlanScenario_ValidatesKindAndAdjustment(t *testing.T) {
	f := &fakePlans{}
	ok := `{"name":"Optimistic","kind":"Optimistic","copyFromScenarioId":"` + scenarioID + `","adjustmentPct":"10"}`
	if rec := servePlan(t, f, nil, http.MethodPost, planPath+"/scenarios", ok, dutyPlanMaintain, nil); rec.Code != http.StatusCreated || f.newScen.CopyFromID != scenarioID || !f.newScen.AdjustmentPct.Equal(decimal.NewFromInt(10)) {
		t.Fatalf("copy scenario: %d %+v", rec.Code, f.newScen)
	}
	for name, body := range map[string]string{
		"unknown kind":              `{"name":"x","kind":"Wild"}`,
		"adjustment without a copy": `{"name":"x","kind":"Custom","adjustmentPct":"5"}`,
		"adjustment of -100":        `{"name":"x","kind":"Custom","copyFromScenarioId":"` + scenarioID + `","adjustmentPct":"-100"}`,
	} {
		t.Run(name, func(t *testing.T) {
			g := &fakePlans{}
			if rec := servePlan(t, g, nil, http.MethodPost, planPath+"/scenarios", body, dutyPlanMaintain, nil); rec.Code != http.StatusBadRequest || g.calls != 0 {
				t.Fatalf("%d %s, want 400", rec.Code, rec.Body)
			}
		})
	}
}

func TestPlanOwners_ValidatesEntries(t *testing.T) {
	f := &fakePlans{}
	ok := `{"owners":[{"stage":"Review","orgUnitId":"` + orgUnitID + `","owner":"alex"}]}`
	if rec := servePlan(t, f, nil, http.MethodPut, planPath+"/stage-owners", ok, dutyPlanMaintain, nil); rec.Code != http.StatusOK || len(f.owners) != 1 {
		t.Fatalf("owners: %d %+v", rec.Code, f.owners)
	}
	if rec := servePlan(t, &fakePlans{}, nil, http.MethodPut, planPath+"/stage-owners", `{"owners":[{"stage":"Audit","orgUnitId":"x","owner":""}]}`, dutyPlanMaintain, nil); rec.Code != http.StatusBadRequest {
		t.Fatalf("bad owner: %d, want 400", rec.Code)
	}
}

func TestPlanCreate_ValidatesBody(t *testing.T) {
	ok := `{"legalEntityId":"` + entityID + `","ruleVersionId":"` + ruleID + `","name":"FY27","fiscalYear":2027}`
	if rec := servePlan(t, &fakePlans{}, nil, http.MethodPost, "/v1/budget/plans", ok, dutyPlanMaintain, nil); rec.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", rec.Code, rec.Body)
	}
	if rec := servePlan(t, &fakePlans{}, nil, http.MethodPost, "/v1/budget/plans", `{"legalEntityId":"x","name":"","fiscalYear":12}`, dutyPlanMaintain, nil); rec.Code != http.StatusBadRequest {
		t.Fatalf("bad create: %d, want 400", rec.Code)
	}
}

func TestPlan_RoutesAbsentWithoutPlanning(t *testing.T) {
	if rec := serve(t, &fakeStore{}, http.MethodGet, "/v1/budget/plans", ``, dutyView, ""); rec.Code != http.StatusNotFound && rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("plans route without a plan store: %d", rec.Code)
	}
}

func TestPlanSpread_CustomKeyFollowsTheSeasonalWeights(t *testing.T) {
	f := &fakePlans{plan: store.Plan{ID: planID, Status: domain.PlanDraft, FiscalYear: 2027}}
	body := `{"account":"6000","total":"1400","method":"custom","periodWeights":[3,1,1,1,1,1,1,1,1,1,1,1],"costCenters":[{"costCenter":"CC1","weight":"1"}]}`
	rec := servePlan(t, f, nil, http.MethodPost, planPath+"/scenarios/"+scenarioID+":spread", body, dutyPlanMaintain, map[string]string{"If-Match": `"3"`})
	if rec.Code != http.StatusOK || len(f.cells) != 12 {
		t.Fatalf("custom spread: %d %s cells %d, want 12 cells", rec.Code, rec.Body, len(f.cells))
	}
	if !f.cells[0].Amount.Equal(decimal.NewFromInt(300)) || !f.cells[1].Amount.Equal(decimal.NewFromInt(100)) {
		t.Fatalf("first periods %s and %s, want 300 and 100", f.cells[0].Amount, f.cells[1].Amount)
	}
}

func TestPlanSpread_CustomKeyIsValidated(t *testing.T) {
	cases := map[string]string{
		"eleven weights":      `{"account":"6000","total":"10","method":"custom","periodWeights":[1,1,1,1,1,1,1,1,1,1,1],"costCenters":[{"costCenter":"CC1","weight":"1"}]}`,
		"all zero":            `{"account":"6000","total":"10","method":"custom","periodWeights":[0,0,0,0,0,0,0,0,0,0,0,0],"costCenters":[{"costCenter":"CC1","weight":"1"}]}`,
		"negative":            `{"account":"6000","total":"10","method":"custom","periodWeights":[-1,1,1,1,1,1,1,1,1,1,1,1],"costCenters":[{"costCenter":"CC1","weight":"1"}]}`,
		"weights without key": `{"account":"6000","total":"10","method":"even","periodWeights":[1,1,1,1,1,1,1,1,1,1,1,1],"costCenters":[{"costCenter":"CC1","weight":"1"}]}`,
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			f := &fakePlans{plan: store.Plan{ID: planID, Status: domain.PlanDraft, FiscalYear: 2027}}
			rec := servePlan(t, f, nil, http.MethodPost, planPath+"/scenarios/"+scenarioID+":spread", body, dutyPlanMaintain, map[string]string{"If-Match": `"1"`})
			if rec.Code != http.StatusBadRequest || len(f.cells) != 0 {
				t.Fatalf("%d %s cells %d, want 400 and no write", rec.Code, rec.Body, len(f.cells))
			}
		})
	}
}

func TestPlanCompare_ReadsTwoScenariosWithTheViewDuty(t *testing.T) {
	other := "0196a6a4-0000-7000-8000-000000000099"
	path := planPath + "/scenarios:compare?base=" + scenarioID + "&other=" + other + "&groupBy=costCenter"
	if rec := servePlan(t, &fakePlans{}, nil, http.MethodGet, path, "", "budget.check", nil); rec.Code != http.StatusForbidden {
		t.Fatalf("without the view duty: %d, want 403", rec.Code)
	}
	rec := servePlan(t, &fakePlans{}, nil, http.MethodGet, path, "", dutyView, nil)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"groupBy":"costCenter"`) {
		t.Fatalf("compare: %d %s", rec.Code, rec.Body)
	}
	if rec := servePlan(t, &fakePlans{}, nil, http.MethodGet, planPath+"/scenarios:compare?base=x&other="+other, "", dutyView, nil); rec.Code != http.StatusBadRequest {
		t.Fatalf("bad base id: %d, want 400", rec.Code)
	}
	if rec := servePlan(t, &fakePlans{}, nil, http.MethodGet, planPath+"/scenarios:compare?base="+scenarioID+"&other="+other+"&groupBy=nope", "", dutyView, nil); rec.Code != http.StatusBadRequest {
		t.Fatalf("bad groupBy: %d, want 400", rec.Code)
	}
}
