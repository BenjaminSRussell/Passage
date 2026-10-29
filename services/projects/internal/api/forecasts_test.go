package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/projects/internal/domain"
)

const sliceWBS = `{"nodes":[
	{"wbsCode":"1","name":"Project"},
	{"wbsCode":"1.1","parentWbsCode":"1","name":"Design","durationDays":5},
	{"wbsCode":"1.2","parentWbsCode":"1","name":"Build","durationDays":10},
	{"wbsCode":"1.4","parentWbsCode":"1","name":"Handover","durationDays":1}],
	"dependencies":[{"predecessorWbsCode":"1.1","successorWbsCode":"1.2","type":"FS","lagDays":0}]}`

func transferOf(t *testing.T, rec *httptest.ResponseRecorder) transferView {
	t.Helper()
	var v transferView
	if err := json.Unmarshal(rec.Body.Bytes(), &v); err != nil {
		t.Fatalf("decode transfer: %v (%d %s)", err, rec.Code, rec.Body.String())
	}
	return v
}

func putWBSAs(t *testing.T, h http.Handler, project, plan, version, body string) *httptest.ResponseRecorder {
	t.Helper()
	return as(t, h, []string{DutyPlan}, http.MethodPut, "/v1/projects/"+project+"/plans/"+plan+"/wbs", body, map[string]string{"If-Match": version})
}

// TestTransfer_PreviewResolveAndLock drives AC-41.15 over HTTP: the preview
// shows the applied node and the conflict, the transfer needs If-Match and a
// resolution, and a transferred simulation is locked.
func TestTransfer_PreviewResolveAndLock(t *testing.T) {
	f := newFake()
	h := router(f)
	seedProject(t, f, "P-SIM")
	working := planOf(t, createPlan(t, h, "P-SIM", `{"kind":"WORKING"}`, "w"))
	if rec := putWBSAs(t, h, "P-SIM", working.ID, `"1"`, sliceWBS); rec.Code != http.StatusOK {
		t.Fatalf("working WBS: %d %s", rec.Code, rec.Body.String())
	}
	sim := planOf(t, createPlan(t, h, "P-SIM", `{"kind":"SIMULATION","copyFromVersionId":"`+working.ID+`"}`, "s"))

	moved := strings.Replace(sliceWBS, `"name":"Build","durationDays":10`, `"name":"Build","durationDays":10,"constraintType":"SNET","constraintDate":"2026-11-09"`, 1)
	if rec := putWBSAs(t, h, "P-SIM", working.ID, `"2"`, moved); rec.Code != http.StatusOK {
		t.Fatalf("move 1.2 in WORKING: %d %s", rec.Code, rec.Body.String())
	}
	simBody := strings.Replace(sliceWBS, `"name":"Build","durationDays":10`, `"name":"Build","durationDays":14`, 1)
	simBody = strings.Replace(simBody, `"name":"Handover","durationDays":1`, `"name":"Handover","durationDays":3`, 1)
	if rec := putWBSAs(t, h, "P-SIM", sim.ID, `"1"`, simBody); rec.Code != http.StatusOK {
		t.Fatalf("simulation WBS: %d %s", rec.Code, rec.Body.String())
	}

	path := "/v1/projects/P-SIM/plans/" + sim.ID + "/transfer"
	rec := as(t, h, []string{DutyInquire}, http.MethodGet, path, "", nil)
	pre := transferOf(t, rec)
	if rec.Code != http.StatusOK || rec.Header().Get("ETag") != `"2"` || len(pre.Applied) != 1 || pre.Applied[0].WBSCode != "1.4" ||
		len(pre.Conflicts) != 1 || pre.Conflicts[0].WBSCode != "1.2" || pre.Conflicts[0].Working == nil || pre.Conflicts[0].Simulation.DurationDays != 14 {
		t.Fatalf("preview: %d %+v", rec.Code, pre)
	}

	if rec := as(t, h, []string{DutyInquire}, http.MethodPost, path, `{}`, map[string]string{"If-Match": `"2"`}); rec.Code != http.StatusForbidden {
		t.Fatalf("transfer without ProjPlan: %d", rec.Code)
	}
	if rec := as(t, h, []string{DutyPlan}, http.MethodPost, path, `{}`, nil); rec.Code != http.StatusPreconditionRequired {
		t.Fatalf("transfer without If-Match: %d", rec.Code)
	}
	rec = as(t, h, []string{DutyPlan}, http.MethodPost, path, `{"resolutions":{"1.2":"MERGE"}}`, map[string]string{"If-Match": `"2"`})
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "resolutions.1.2") {
		t.Fatalf("bad resolution: %d %s", rec.Code, rec.Body.String())
	}
	rec = as(t, h, []string{DutyPlan}, http.MethodPost, path, `{}`, map[string]string{"If-Match": `"2"`})
	if rec.Code != http.StatusUnprocessableEntity || errorCode(t, rec) != domain.CodeSimConflict {
		t.Fatalf("unresolved: %d %s", rec.Code, rec.Body.String())
	}
	if rec := as(t, h, []string{DutyPlan}, http.MethodPost, path, `{"resolutions":{"1.2":"TAKE_SIMULATION"}}`, map[string]string{"If-Match": `"1"`}); rec.Code != http.StatusPreconditionFailed {
		t.Fatalf("stale If-Match: %d", rec.Code)
	}
	if rec := as(t, h, []string{DutyPlan}, http.MethodPost, path, `{"resolutions":{"1.2":"TAKE_SIMULATION"},"workingVersion":2}`, map[string]string{"If-Match": `"2"`}); rec.Code != http.StatusPreconditionFailed {
		t.Fatalf("stale workingVersion: %d", rec.Code)
	}

	rec = as(t, h, []string{DutyPlan}, http.MethodPost, path, `{"resolutions":{"1.2":"TAKE_SIMULATION"},"workingVersion":3}`, map[string]string{"If-Match": `"2"`})
	out := transferOf(t, rec)
	if rec.Code != http.StatusOK || rec.Header().Get("ETag") != `"4"` || !out.Simulation.Locked || out.Simulation.TransferredAt == nil ||
		len(out.Applied) != 2 || out.Conflicts[0].Resolution != domain.ResolveTakeSimulation {
		t.Fatalf("transfer: %d %+v", rec.Code, out)
	}
	wbs := as(t, h, []string{DutyInquire}, http.MethodGet, "/v1/projects/P-SIM/plans/"+working.ID+"/wbs", "", nil)
	if !strings.Contains(wbs.Body.String(), `"durationDays":14`) || !strings.Contains(wbs.Body.String(), `"durationDays":3`) {
		t.Fatalf("WORKING after transfer: %s", wbs.Body.String())
	}
	rec = as(t, h, []string{DutyPlan}, http.MethodPost, path, `{}`, map[string]string{"If-Match": `"3"`})
	if rec.Code != http.StatusUnprocessableEntity || errorCode(t, rec) != domain.CodePlanLocked {
		t.Fatalf("second transfer: %d %s", rec.Code, rec.Body.String())
	}
	rec = as(t, h, []string{DutyInquire}, http.MethodGet, "/v1/projects/P-SIM/plans/"+working.ID+"/transfer", "", nil)
	if rec.Code != http.StatusUnprocessableEntity || errorCode(t, rec) != domain.CodePlanKind {
		t.Fatalf("transfer of WORKING: %d %s", rec.Code, rec.Body.String())
	}
	if rec := as(t, h, []string{DutyInquire}, http.MethodGet, "/v1/projects/P-SIM/plans/not-a-uuid/transfer", "", nil); rec.Code != http.StatusNotFound {
		t.Fatalf("malformed plan id: %d", rec.Code)
	}
}

func forecastOf(t *testing.T, rec *httptest.ResponseRecorder) domain.ForecastModel {
	t.Helper()
	var m domain.ForecastModel
	if err := json.Unmarshal(rec.Body.Bytes(), &m); err != nil || m.ID == "" {
		t.Fatalf("decode forecast: %v (%d %s)", err, rec.Code, rec.Body.String())
	}
	return m
}

const forecastBody = `{"name":"Q4","lines":[
	{"period":"2026-10","txnType":"HOUR","quantity":"10","costAmount":"500","salesAmount":"900"},
	{"period":"2026-11","txnType":"HOUR","quantity":"20","costAmount":"1000","salesAmount":"1800"}]}`

func TestForecasts_CreateListReplaceAndCompare(t *testing.T) {
	f := newFake()
	h := router(f)
	billingProject(t, f, "P-FC", domain.TypeTimeMaterial)
	planner := userCtx("planner-1", DutyPlan, DutyInquire)

	if rec := do(t, h, inquirer, http.MethodPost, "/v1/projects/P-FC/forecasts", forecastBody, key("f1")); rec.Code != http.StatusForbidden {
		t.Fatalf("create without ProjPlan: %d", rec.Code)
	}
	if rec := do(t, h, planner, http.MethodPost, "/v1/projects/P-FC/forecasts", forecastBody, nil); rec.Code != http.StatusBadRequest {
		t.Fatalf("create without key: %d", rec.Code)
	}
	rec := do(t, h, planner, http.MethodPost, "/v1/projects/P-FC/forecasts",
		`{"name":"","lines":[{"period":"2026-1","txnType":"HOUR","wbsCode":"9","quantity":"1","costAmount":"1","salesAmount":"1"}]}`, key("bad"))
	for _, field := range []string{"name", "lines[0].period", "lines[0].wbsCode"} {
		if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), `"`+field+`"`) {
			t.Fatalf("field %s: %d %s", field, rec.Code, rec.Body.String())
		}
	}
	rec = do(t, h, planner, http.MethodPost, "/v1/projects/P-FC/forecasts", forecastBody, key("f1"))
	m := forecastOf(t, rec)
	if rec.Code != http.StatusCreated || rec.Header().Get("ETag") != `"1"` || m.TotalCost != "1500.00" || len(m.Lines) != 2 || m.Lines[1].LineNo != 2 {
		t.Fatalf("create: %d %+v", rec.Code, m)
	}
	if rec := do(t, h, planner, http.MethodPost, "/v1/projects/P-FC/forecasts", forecastBody, key("f1")); rec.Code != http.StatusOK || rec.Header().Get("Idempotent-Replayed") != "true" {
		t.Fatalf("replay: %d", rec.Code)
	}
	rec = do(t, h, planner, http.MethodPost, "/v1/projects/P-FC/forecasts", forecastBody, key("f2"))
	if rec.Code != http.StatusUnprocessableEntity || errorCode(t, rec) != domain.CodeForecastDuplicate {
		t.Fatalf("duplicate name: %d %s", rec.Code, rec.Body.String())
	}

	// 8 recorded hours in October against 10 forecast.
	if rec := postTxn(t, h, hourBody("P-FC", "CHARGEABLE", "2026-10-05", "8"), "t1"); rec.Code != http.StatusCreated {
		t.Fatalf("transaction: %d %s", rec.Code, rec.Body.String())
	}
	rec = do(t, h, inquirer, http.MethodGet, "/v1/projects/P-FC/forecasts/"+m.ID, "", nil)
	var view forecastView
	if err := json.Unmarshal(rec.Body.Bytes(), &view); err != nil || rec.Code != http.StatusOK || len(view.Comparison) != 2 {
		t.Fatalf("compare: %d %s", rec.Code, rec.Body.String())
	}
	if oct := view.Comparison[0]; oct.Period != "2026-10" || oct.ActualQuantity != "8.000" || oct.CostVariance != "-100.00" {
		t.Fatalf("October: %+v", oct)
	}

	path := "/v1/projects/P-FC/forecasts/" + m.ID
	if rec := do(t, h, planner, http.MethodPut, path, forecastBody, nil); rec.Code != http.StatusPreconditionRequired {
		t.Fatalf("replace without If-Match: %d", rec.Code)
	}
	if rec := do(t, h, planner, http.MethodPut, path, forecastBody, map[string]string{"If-Match": `"7"`}); rec.Code != http.StatusPreconditionFailed {
		t.Fatalf("stale replace: %d", rec.Code)
	}
	rec = do(t, h, planner, http.MethodPut, path, `{"name":"Q4 v2","lines":[]}`, map[string]string{"If-Match": `"1"`})
	if upd := forecastOf(t, rec); rec.Code != http.StatusOK || upd.Version != 2 || upd.Name != "Q4 v2" || upd.LineCount != 0 || upd.Lines == nil {
		t.Fatalf("replace: %d %s", rec.Code, rec.Body.String())
	}
	rec = do(t, h, inquirer, http.MethodGet, "/v1/projects/P-FC/forecasts", "", nil)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"name":"Q4 v2"`) || strings.Contains(rec.Body.String(), `"lines"`) {
		t.Fatalf("list: %d %s", rec.Code, rec.Body.String())
	}
	if rec := do(t, h, inquirer, http.MethodGet, "/v1/projects/P-FC/forecasts/not-a-uuid", "", nil); rec.Code != http.StatusNotFound {
		t.Fatalf("malformed model id: %d", rec.Code)
	}
	if rec := do(t, h, inquirer, http.MethodGet, "/v1/projects/P-FC/forecasts/"+ids.New(), "", nil); rec.Code != http.StatusNotFound {
		t.Fatalf("unknown model: %d", rec.Code)
	}
	if rec := do(t, h, inquirer, http.MethodGet, "/v1/projects/NOPE/forecasts", "", nil); rec.Code != http.StatusNotFound {
		t.Fatalf("unknown project: %d", rec.Code)
	}
}

func TestTransactionList_CursorPages(t *testing.T) {
	f := newFake()
	h := router(f)
	billingProject(t, f, "P-PAGE", domain.TypeTimeMaterial)
	for i, date := range []string{"2026-10-01", "2026-10-02", "2026-10-03"} {
		if rec := postTxn(t, h, hourBody("P-PAGE", "CHARGEABLE", date, "1"), "p"+string(rune('a'+i))); rec.Code != http.StatusCreated {
			t.Fatalf("transaction: %d %s", rec.Code, rec.Body.String())
		}
	}
	var page transactionList
	rec := do(t, h, inquirer, http.MethodGet, "/v1/projects/P-PAGE/transactions?limit=2", "", nil)
	if err := json.Unmarshal(rec.Body.Bytes(), &page); err != nil || len(page.Items) != 2 || page.NextCursor == "" || page.Items[0].TxnDate != "2026-10-03" {
		t.Fatalf("first page: %d %s", rec.Code, rec.Body.String())
	}
	rec = do(t, h, inquirer, http.MethodGet, "/v1/projects/P-PAGE/transactions?limit=2&cursor="+page.NextCursor, "", nil)
	page = transactionList{}
	if err := json.Unmarshal(rec.Body.Bytes(), &page); err != nil || len(page.Items) != 1 || page.NextCursor != "" || page.Items[0].TxnDate != "2026-10-01" {
		t.Fatalf("second page: %d %s", rec.Code, rec.Body.String())
	}
	for _, q := range []string{"limit=0", "limit=101", "cursor=!!", "cursor=" + encodeCursor("not-a-uuid")} {
		if rec := do(t, h, inquirer, http.MethodGet, "/v1/projects/P-PAGE/transactions?"+q, "", nil); rec.Code != http.StatusBadRequest {
			t.Errorf("%s: %d", q, rec.Code)
		}
		if rec := do(t, h, inquirer, http.MethodGet, "/v1/projects/invoice-proposals?projectId=P-PAGE&"+q, "", nil); rec.Code != http.StatusBadRequest {
			t.Errorf("proposals %s: %d", q, rec.Code)
		}
	}
}
