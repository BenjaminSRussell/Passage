package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/budgeting/internal/sources"
)

type fakeSources struct {
	assets    sources.Months
	positions []sources.PositionCost
	projects  map[string]sources.Months
	demand    sources.Months
	err       error
	gotYear   int
	gotLE     string
	gotBook   string
	gotSales  bool
}

func (f *fakeSources) Depreciation(_ context.Context, le string, year int, _, book string) (sources.Months, int, error) {
	f.gotLE, f.gotYear, f.gotBook = le, year, book
	return f.assets, 3, f.err
}

func (f *fakeSources) Positions(_ context.Context, _ string, _ int, rates []sources.PositionRate) ([]sources.PositionCost, error) {
	if len(rates) == 0 {
		panic("no rates")
	}
	return f.positions, f.err
}

func (f *fakeSources) ProjectForecast(_ context.Context, _, projectID, _ string, _ int, _ string, sales bool) (sources.Months, error) {
	f.gotSales = sales
	return f.projects[projectID], f.err
}

func (f *fakeSources) Demand(context.Context, string, string, int, []sources.ItemPrice) (sources.Months, error) {
	return f.demand, f.err
}

func serveSources(t *testing.T, ps PlanStore, src PlanSources, body string) *httptest.ResponseRecorder {
	t.Helper()
	r := chi.NewRouter()
	a := New(&fakeStore{}).WithPlanning(ps, nil)
	if src != nil {
		a.WithPlanSources(src)
	}
	a.Register(r)
	req := httptest.NewRequest(http.MethodPost, generatePath, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(idempotencyKey, "k-1")
	req.Header.Set("If-Match", `"1"`)
	req = req.WithContext(testkit.Ctx(tenantID, dutyPlanMaintain))
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	return rec
}

func eur2027() *fakePlans {
	p := draftPlan()
	p.Currency = "EUR"
	return &fakePlans{plan: p}
}

func months(kv ...string) sources.Months {
	m := sources.Months{}
	for i := 0; i < len(kv); i += 2 {
		m[kv[i]] = decimal.RequireFromString(kv[i+1])
	}
	return m
}

func TestPlanGenerate_FixedAssetsSharesTheScheduleOverCostCenters(t *testing.T) {
	f := eur2027()
	src := &fakeSources{assets: months("2027-01", "1000.01", "2027-02", "500")}
	body := `{"source":"fixed-assets","account":"6500","book":"IFRS","adjustmentPct":"10","costCenters":[{"costCenter":"CC1","weight":"1"},{"costCenter":"CC2","weight":"1"}]}`
	rec := serveSources(t, f, src, body)
	if rec.Code != http.StatusOK || len(f.cells) != 4 || src.gotYear != 2027 || src.gotLE != entityID || src.gotBook != "IFRS" {
		t.Fatalf("generate: %d %s cells %+v source saw %d %s %s", rec.Code, rec.Body, f.cells, src.gotYear, src.gotLE, src.gotBook)
	}
	sum := decimal.Zero
	for _, c := range f.cells {
		if c.Account != "6500" || c.Period != "2027-01" && c.Period != "2027-02" {
			t.Fatalf("cell = %+v", c)
		}
		if c.Period == "2027-01" {
			sum = sum.Add(c.Amount)
		}
	}
	// 1000.01 plus 10 percent is 1100.011, rounded once to four decimals and shared without losing or inventing a unit of it.
	if !sum.Equal(decimal.RequireFromString("1100.011")) {
		t.Fatalf("January shares add to %s, want 1100.011", sum)
	}
}

func TestPlanGenerate_PositionsGoToTheCostCenterOfTheirDepartment(t *testing.T) {
	const dept1, dept2 = "0196a6a4-0000-7000-8000-0000000000d1", "0196a6a4-0000-7000-8000-0000000000d2"
	f := eur2027()
	src := &fakeSources{positions: []sources.PositionCost{
		{DepartmentID: dept1, Months: months("2027-01", "6500", "2027-02", "6500")},
		{DepartmentID: dept2, Months: months("2027-02", "3250.5")},
	}}
	body := `{"source":"positions","account":"6000","rates":[{"jobCode":"ENG","monthlyCost":"6500"}],"departments":[{"departmentId":"` + dept1 + `","costCenter":"CC-ENG"},{"departmentId":"` + dept2 + `","costCenter":"CC-OPS"}]}`
	rec := serveSources(t, f, src, body)
	if rec.Code != http.StatusOK || len(f.cells) != 3 {
		t.Fatalf("generate: %d %s cells %+v", rec.Code, rec.Body, f.cells)
	}
	got := map[string]string{}
	for _, c := range f.cells {
		got[c.CostCenter+c.Period] = c.Amount.String()
	}
	if got["CC-ENG2027-01"] != "6500" || got["CC-OPS2027-02"] != "3250.5" {
		t.Fatalf("cells = %v", got)
	}
	// A department of HR that the request does not map is named.
	unmapped := strings.Replace(body, `,{"departmentId":"`+dept2+`","costCenter":"CC-OPS"}`, "", 1)
	if rec := serveSources(t, eur2027(), src, unmapped); rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), codeSourceUnmapped) || !strings.Contains(rec.Body.String(), dept2) {
		t.Fatalf("unmapped department = %d %s", rec.Code, rec.Body)
	}
}

func TestPlanGenerate_ProjectsAndDemandWriteTheirMonths(t *testing.T) {
	const p1, m1 = "0196a6a4-0000-7000-8000-0000000000e1", "0196a6a4-0000-7000-8000-0000000000e2"
	f := eur2027()
	src := &fakeSources{projects: map[string]sources.Months{p1: months("2027-03", "12000")}, demand: months("2027-01", "250.5")}
	body := `{"source":"projects","account":"5100","projects":[{"projectId":"` + p1 + `","modelId":"` + m1 + `","costCenter":"CC-PRJ","amount":"sales"}]}`
	if rec := serveSources(t, f, src, body); rec.Code != http.StatusOK || len(f.cells) != 1 || f.cells[0].CostCenter != "CC-PRJ" || !src.gotSales {
		t.Fatalf("projects: %d %s cells %+v sales %v", rec.Code, rec.Body, f.cells, src.gotSales)
	}
	f = eur2027()
	body = `{"source":"demand","account":"4000","modelCode":"FC-2027","unitPrices":[{"itemId":"0196a6a4-0000-7000-8000-0000000000f1","price":"12.5"}],"costCenters":[{"costCenter":"CC1","weight":"3"},{"costCenter":"CC2","weight":"1"}]}`
	rec := serveSources(t, f, src, body)
	if rec.Code != http.StatusOK || len(f.cells) != 2 || !f.cells[0].Amount.Add(f.cells[1].Amount).Equal(decimal.RequireFromString("250.5")) {
		t.Fatalf("demand: %d %s cells %+v", rec.Code, rec.Body, f.cells)
	}
}

func TestPlanGenerate_SourceProblems(t *testing.T) {
	good := `{"source":"fixed-assets","account":"6500","costCenters":[{"costCenter":"CC1","weight":"1"}]}`
	// Not configured, a source that holds nothing, and a service that refused.
	if rec := serveSources(t, eur2027(), nil, good); rec.Code != http.StatusServiceUnavailable || !strings.Contains(rec.Body.String(), codeSourceOff) {
		t.Fatalf("off = %d %s", rec.Code, rec.Body)
	}
	if rec := serveSources(t, eur2027(), &fakeSources{assets: sources.Months{}}, good); rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), codeNoSourceData) {
		t.Fatalf("empty = %d %s", rec.Code, rec.Body)
	}
	if rec := serveSources(t, eur2027(), &fakeSources{err: &sources.Error{Detail: "the assets service did not answer"}}, good); rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), codeSourceUnavailable) {
		t.Fatalf("down = %d %s", rec.Code, rec.Body)
	}
	for name, body := range map[string]string{
		"no account":       `{"source":"fixed-assets","costCenters":[{"costCenter":"CC1","weight":"1"}]}`,
		"no cost centers":  `{"source":"fixed-assets","account":"6500"}`,
		"accounts listed":  `{"source":"fixed-assets","account":"6500","accounts":["1"],"costCenters":[{"costCenter":"CC1","weight":"1"}]}`,
		"no rates":         `{"source":"positions","account":"6000","departments":[{"departmentId":"0196a6a4-0000-7000-8000-0000000000d1","costCenter":"C"}]}`,
		"bad project":      `{"source":"projects","account":"1","projects":[{"projectId":"x","modelId":"y","costCenter":"C"}]}`,
		"repeated project": `{"source":"projects","account":"1","projects":[{"projectId":"0196a6a4-0000-7000-8000-0000000000e1","modelId":"0196a6a4-0000-7000-8000-0000000000e2","costCenter":"C"},{"projectId":"0196a6a4-0000-7000-8000-0000000000e1","modelId":"0196a6a4-0000-7000-8000-0000000000e2","costCenter":"D"}]}`,
		"no model code":    `{"source":"demand","account":"1","unitPrices":[{"itemId":"0196a6a4-0000-7000-8000-0000000000f1","price":"1"}],"costCenters":[{"costCenter":"C","weight":"1"}]}`,
		"cost centers+pos": `{"source":"positions","account":"1","costCenters":[{"costCenter":"C","weight":"1"}]}`,
	} {
		f := eur2027()
		if rec := serveSources(t, f, &fakeSources{assets: months("2027-01", "1")}, body); rec.Code != http.StatusBadRequest || len(f.cells) != 0 {
			t.Errorf("%s = %d %s", name, rec.Code, rec.Body)
		}
	}
}
