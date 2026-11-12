package api

import (
	"context"
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/budgeting/internal/domain"
	"github.com/BenjaminSRussell/passage/services/budgeting/internal/store"
	"github.com/BenjaminSRussell/passage/services/budgeting/internal/xlsx"
	"github.com/BenjaminSRussell/passage/services/budgeting/migrations"
)

type xlsxEnv struct {
	h        http.Handler
	st       *store.Postgres
	planID   string
	scenario string
}

func newXlsxEnv(t *testing.T) *xlsxEnv {
	t.Helper()
	pool := testkit.DB(t)
	if err := dbx.Migrate(context.Background(), pool, migrations.FS); err != nil {
		t.Fatal(err)
	}
	st := store.New(pool)
	ctx := testkit.Ctx(tenantID, dutyPlanMaintain, dutyView)
	_, raw, err := st.CreateRule(ctx, ids.New(), "h", store.NewRule{LegalEntityID: ids.New(), Code: "PLAN", WarnPct: decimal.Zero, BlockPct: decimal.Zero, Currency: "USD", EffectiveFrom: "2027-01-01"})
	if err != nil {
		t.Fatal(err)
	}
	var rule store.Rule
	if err := json.Unmarshal(raw, &rule); err != nil {
		t.Fatal(err)
	}
	_, raw, err = st.CreatePlan(ctx, ids.New(), "h2", store.NewPlan{LegalEntityID: rule.LegalEntityID, RuleVersionID: rule.ID, Name: "FY27 operating", FiscalYear: 2027})
	if err != nil {
		t.Fatal(err)
	}
	var view store.PlanView
	if err := json.Unmarshal(raw, &view); err != nil {
		t.Fatal(err)
	}
	r := chi.NewRouter()
	New(&fakeStore{}).WithPlanning(st, nil).Register(r)
	return &xlsxEnv{h: r, st: st, planID: view.ID, scenario: view.Scenarios[0].ID}
}

func (e *xlsxEnv) do(method, path string, body []byte, headers map[string]string, duties ...string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, bytes.NewReader(body))
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	req = req.WithContext(testkit.Ctx(tenantID, duties...))
	rec := httptest.NewRecorder()
	e.h.ServeHTTP(rec, req)
	return rec
}

func (e *xlsxEnv) path(suffix string) string {
	return "/v1/budget/plans/" + e.planID + "/scenarios/" + e.scenario + suffix
}

func (e *xlsxEnv) seed(t *testing.T) string {
	t.Helper()
	cells := []domain.PlanCell{
		{Account: "6000", CostCenter: "CC1", Period: "2027-01", Amount: decimal.RequireFromString("1000.5")},
		{Account: "6000", CostCenter: "CC1", Period: "2027-02", Amount: decimal.RequireFromString("2000")},
		{Account: "6100", CostCenter: "CC2", Period: "2027-12", Amount: decimal.RequireFromString("0.1234")},
	}
	if _, _, err := e.st.UpsertCells(testkit.Ctx(tenantID, dutyPlanMaintain), "seed", "h", e.planID, e.scenario, 1, cells); err != nil {
		t.Fatal(err)
	}
	return `"2"`
}

func TestPlanXlsx_ExportEditImportRoundTrip(t *testing.T) {
	e := newXlsxEnv(t)
	e.seed(t)
	rec := e.do(http.MethodGet, e.path(":export"), nil, nil, dutyView)
	if rec.Code != http.StatusOK || rec.Header().Get("Content-Type") != xlsxContentType || rec.Header().Get("ETag") != `"2"` || !strings.Contains(rec.Header().Get("Content-Disposition"), ".xlsx") {
		t.Fatalf("export = %d %v", rec.Code, rec.Header())
	}
	sheets, err := xlsx.Read(context.Background(), rec.Body.Bytes())
	if err != nil || len(sheets) != 2 {
		t.Fatalf("read export: %v %d", err, len(sheets))
	}
	grid := &sheets[0]
	if grid.Name != "Plan" || grid.At(0, 2).Text != "2027-01" || grid.At(0, 13).Text != "2027-12" || grid.At(0, 14).Text != "Total" || len(grid.Rows) != 3 {
		t.Fatalf("plan sheet = %+v", grid.Rows)
	}
	if grid.At(1, 0).Text != "6000" || grid.At(1, 2).Text != "1000.5" || grid.At(1, 14).Text != "3000.5" || grid.At(2, 13).Text != "0.1234" {
		t.Errorf("rows = %+v", grid.Rows)
	}

	// Edit: raise January, clear February, add a new row, leave a blank amount; then import.
	grid.Rows[1][2] = xlsx.Num("1500.25")
	grid.Rows[1][3] = xlsx.Num("0")
	row := make([]xlsx.Cell, 15)
	row[0], row[1], row[2], row[4] = xlsx.Str("6200"), xlsx.Str("CC3"), xlsx.Num("75"), xlsx.Num("1E2")
	grid.Rows = append(grid.Rows, row)
	edited, err := xlsx.Write(sheets)
	if err != nil {
		t.Fatal(err)
	}
	hdr := map[string]string{"If-Match": `"2"`, "Idempotency-Key": "imp-1"}
	dry := e.do(http.MethodPost, e.path(":import")+"?dryRun=true", edited, hdr, dutyPlanMaintain)
	var dr importResult
	if err := json.Unmarshal(dry.Body.Bytes(), &dr); err != nil || dry.Code != http.StatusOK || !dr.Valid || dr.Rows != 3 || dr.Cells != 36 || !dr.Total.Equal(decimal.RequireFromString("1675.3734")) {
		t.Fatalf("dry run = %d %s", dry.Code, dry.Body)
	}
	if g, _ := e.st.GetGrid(testkit.Ctx(tenantID, dutyView), e.planID, e.scenario, store.GridFilter{}); g.Scenario.Version != 2 {
		t.Fatalf("a dry run wrote: version %d", g.Scenario.Version)
	}
	imp := e.do(http.MethodPost, e.path(":import"), edited, hdr, dutyPlanMaintain)
	if imp.Code != http.StatusOK || imp.Header().Get("ETag") != `"3"` {
		t.Fatalf("import = %d %s %v", imp.Code, imp.Body, imp.Header())
	}
	g, err := e.st.GetGrid(testkit.Ctx(tenantID, dutyView), e.planID, e.scenario, store.GridFilter{})
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, c := range g.Cells {
		got[c.Account+"/"+c.CostCenter+"/"+c.Period] = c.Amount.String()
	}
	if got["6000/CC1/2027-01"] != "1500.25" || got["6000/CC1/2027-02"] != "0" || got["6200/CC3/2027-01"] != "75" || got["6200/CC3/2027-03"] != "100" || got["6100/CC2/2027-12"] != "0.1234" {
		t.Fatalf("grid after import = %v", got)
	}
	// The same file under the same key replays; a stale If-Match is refused.
	if again := e.do(http.MethodPost, e.path(":import"), edited, hdr, dutyPlanMaintain); again.Code != http.StatusOK || again.Header().Get("ETag") != `"3"` {
		t.Fatalf("replay = %d %v", again.Code, again.Header())
	}
	if g, _ := e.st.GetGrid(testkit.Ctx(tenantID, dutyView), e.planID, e.scenario, store.GridFilter{}); g.Scenario.Version != 3 {
		t.Fatalf("a replay wrote again: version %d", g.Scenario.Version)
	}
	stale := e.do(http.MethodPost, e.path(":import"), edited, map[string]string{"If-Match": `"2"`, "Idempotency-Key": "imp-2"}, dutyPlanMaintain)
	if stale.Code != http.StatusPreconditionFailed {
		t.Fatalf("stale = %d %s", stale.Code, stale.Body)
	}
}

func TestPlanXlsx_ImportReportsEveryProblemWithItsCell(t *testing.T) {
	e := newXlsxEnv(t)
	e.seed(t)
	rec := e.do(http.MethodGet, e.path(":export"), nil, nil, dutyView)
	sheets, _ := xlsx.Read(context.Background(), rec.Body.Bytes())
	g := &sheets[0]
	g.Rows[1][2] = xlsx.Str("lots")                                 // C2 not a number
	g.Rows[1][3] = xlsx.Num("-5")                                   // D2 negative
	g.Rows[2][4] = xlsx.Num("1.23456")                              // E3 five decimals
	g.Rows[2][1] = xlsx.Str("")                                     // B3 no cost center
	g.Rows = append(g.Rows, append([]xlsx.Cell(nil), g.Rows[1]...)) // a duplicate of row 2
	g.Rows[3][2] = xlsx.Num("1")
	bad, _ := xlsx.Write(sheets)
	hdr := map[string]string{"If-Match": `"2"`, "Idempotency-Key": "bad-1"}
	rec = e.do(http.MethodPost, e.path(":import"), bad, hdr, dutyPlanMaintain)
	var problem struct {
		Code   string
		Errors map[string]string
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &problem); err != nil || rec.Code != http.StatusBadRequest {
		t.Fatalf("import = %d %s", rec.Code, rec.Body)
	}
	for ref, want := range map[string]string{"Plan!C2": "number", "Plan!D2": "negative", "Plan!E3": "4 decimal", "Plan!B3": "1 to 100", "Plan!A4": "already in row 2"} {
		if !strings.Contains(problem.Errors[ref], want) {
			t.Errorf("%s = %q, want it to say %q (errors %v)", ref, problem.Errors[ref], want, problem.Errors)
		}
	}
	if g, _ := e.st.GetGrid(testkit.Ctx(tenantID, dutyView), e.planID, e.scenario, store.GridFilter{}); g.Scenario.Version != 2 {
		t.Fatal("a workbook with problems must write nothing")
	}
}

func TestPlanXlsx_ImportRefusesOtherWorkbooksAndNonWorkbooks(t *testing.T) {
	e := newXlsxEnv(t)
	e.seed(t)
	rec := e.do(http.MethodGet, e.path(":export"), nil, nil, dutyView)
	sheets, _ := xlsx.Read(context.Background(), rec.Body.Bytes())
	hdr := func(k string) map[string]string { return map[string]string{"If-Match": `"2"`, "Idempotency-Key": k} }

	other := append([]xlsx.Sheet(nil), sheets...)
	other[1] = xlsx.Sheet{Name: "Info", Rows: append([][]xlsx.Cell(nil), sheets[1].Rows...)}
	other[1].Rows[0] = []xlsx.Cell{xlsx.Str("Plan"), xlsx.Str(ids.New())}
	raw, _ := xlsx.Write(other)
	if rec := e.do(http.MethodPost, e.path(":import"), raw, hdr("o1"), dutyPlanMaintain); rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), codeWorkbookOtherPlan) {
		t.Fatalf("other plan = %d %s", rec.Code, rec.Body)
	}
	wrongYear := append([]xlsx.Sheet(nil), sheets...)
	wrongYear[0] = xlsx.Sheet{Name: "Plan", Rows: append([][]xlsx.Cell(nil), sheets[0].Rows...)}
	wrongYear[0].Rows[0] = append([]xlsx.Cell(nil), sheets[0].Rows[0]...)
	wrongYear[0].Rows[0][3] = xlsx.Str("2027-03")
	raw, _ = xlsx.Write(wrongYear)
	if rec := e.do(http.MethodPost, e.path(":import"), raw, hdr("o2"), dutyPlanMaintain); rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "Plan!D1") {
		t.Fatalf("shifted header = %d %s", rec.Code, rec.Body)
	}
	if rec := e.do(http.MethodPost, e.path(":import"), []byte("account,cc\n6000,CC1\n"), hdr("o3"), dutyPlanMaintain); rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "xlsx") {
		t.Fatalf("a CSV = %d %s", rec.Code, rec.Body)
	}
	if rec := e.do(http.MethodPost, e.path(":import"), raw, hdr("o4"), dutyView); rec.Code != http.StatusForbidden {
		t.Fatalf("view duty can import: %d", rec.Code)
	}
	if rec := e.do(http.MethodGet, e.path(":export"), nil, nil, dutyPlanMaintain); rec.Code != http.StatusForbidden {
		t.Fatalf("maintain duty only can export: %d", rec.Code)
	}
}

func TestPlanXlsx_ImportRefusesAnOldVersionAndAnAmountTheColumnCannotHold(t *testing.T) {
	e := newXlsxEnv(t)
	e.seed(t)
	rec := e.do(http.MethodGet, e.path(":export"), nil, nil, dutyView)
	sheets, _ := xlsx.Read(context.Background(), rec.Body.Bytes())
	raw, _ := xlsx.Write(sheets)
	// The workbook says version 2; importing with another If-Match is a stale grid.
	stale := e.do(http.MethodPost, e.path(":import"), raw, map[string]string{"If-Match": `"5"`, "Idempotency-Key": "v1"}, dutyPlanMaintain)
	if stale.Code != http.StatusPreconditionFailed || !strings.Contains(stale.Body.String(), "BUD-0050") {
		t.Fatalf("version mismatch = %d %s", stale.Code, stale.Body)
	}
	sheets[0].Rows[1][2] = xlsx.Num("1E15")
	big, _ := xlsx.Write(sheets)
	rec = e.do(http.MethodPost, e.path(":import"), big, map[string]string{"If-Match": `"2"`, "Idempotency-Key": "v2"}, dutyPlanMaintain)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "Plan!C2") {
		t.Fatalf("huge amount = %d %s", rec.Code, rec.Body)
	}
	if rec := e.do(http.MethodPut, e.path("/grid"), []byte(`{"cells":[{"account":"1","costCenter":"c","period":"2027-01","amount":"1000000000000000"}]}`),
		map[string]string{"If-Match": `"2"`, "Idempotency-Key": "v3", "Content-Type": "application/json"}, dutyPlanMaintain); rec.Code != http.StatusBadRequest {
		t.Fatalf("JSON grid huge amount = %d %s", rec.Code, rec.Body)
	}
}
