package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/planning/internal/domain"
	"github.com/BenjaminSRussell/passage/services/planning/internal/store"
)

func (f *fakeStore) currentVersion() int {
	if f.orderVersion == 0 {
		return 1
	}
	return f.orderVersion
}

// editable mirrors the store's checks: deleted or firmed orders are 409, a
// stale version is 412.
func (f *fakeStore) editable(expected int) error {
	if f.orderStatus == "deleted" || f.orderStatus == "firmed" {
		return httpx.NewProblem(http.StatusConflict, store.CodeOrderNotEditable, "not editable")
	}
	if expected != f.currentVersion() {
		return httpx.NewProblem(http.StatusPreconditionFailed, store.CodeVersionConflict, "stale")
	}
	return nil
}

func (f *fakeStore) UpdatePlannedOrder(ctx context.Context, id string, expected int, patch store.OrderPatch) (store.PlannedOrder, error) {
	if err := f.editable(expected); err != nil {
		return store.PlannedOrder{}, err
	}
	f.lastPatch = patch
	f.orderVersion = f.currentVersion() + 1
	if patch.Qty != nil {
		f.orderQty = patch.Qty.String()
	}
	if patch.Approve {
		f.orderStatus = "approved"
	}
	return f.GetPlannedOrder(ctx, id)
}

func (f *fakeStore) DeletePlannedOrder(_ context.Context, _ string, expected int) error {
	if err := f.editable(expected); err != nil {
		return err
	}
	f.orderStatus = "deleted"
	f.orderVersion = f.currentVersion() + 1
	return nil
}

func (f *fakeStore) UpsertForecastLines(ctx context.Context, code string, expected int, spec *store.ModelSpec, _ []store.ForecastCellInput) (store.ForecastModel, error) {
	if f.models == nil {
		f.models = map[string]store.ForecastModel{}
	}
	m, ok := f.models[code]
	if !ok {
		m = store.ForecastModel{Code: code, ReductionPrinciple: domain.ReductionTransactions, ReductionPercent: "0", Status: domain.ConsensusDraft}
	}
	if m.Version != expected {
		return store.ForecastModel{}, httpx.NewProblem(http.StatusPreconditionFailed, store.CodeVersionConflict, "stale")
	}
	if !domain.ConsensusWritable(statusOf(m)) {
		return store.ForecastModel{}, httpx.NewProblem(http.StatusConflict, store.CodeForecastLocked, "locked")
	}
	f.recordEditor(ctx, code)
	m.Version++
	if spec != nil {
		m.ReductionPrinciple = spec.ReductionPrinciple
		m.ReductionPercent = spec.ReductionPercent.String()
	}
	f.models[code] = m
	return m, nil
}

func (f *fakeStore) ListForecastLines(_ context.Context, code, _, _ string, _ int) (store.ForecastModel, []store.ForecastLine, error) {
	m, ok := f.models[code]
	if !ok {
		return store.ForecastModel{}, nil, httpx.NewProblem(http.StatusNotFound, store.CodeForecastNotFound, "forecast model not found")
	}
	return m, []store.ForecastLine{}, nil
}

func (f *fakeStore) ForecastDemand(_ context.Context, code, _ string, from time.Time) ([]store.ForecastDemandLine, error) {
	if _, ok := f.models[code]; !ok {
		return nil, httpx.NewProblem(http.StatusNotFound, store.CodeForecastNotFound, "forecast model not found")
	}
	f.demandAt = from.Format(dateLayout)
	return f.demand, nil
}

const (
	orderPath     = "/v1/planning/planned-orders/" + fgID
	dutyOrders    = "planning.order.maintain"
	dutyForecasts = "planning.forecast.maintain"
)

// doIfMatch sends one request with the given duties and an optional If-Match.
func doIfMatch(t *testing.T, st store.Store, method, path, body, ifMatch, duties string) *httptest.ResponseRecorder {
	t.Helper()
	router := httpx.NewRouter()
	newRouter(st).Register(router)
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Passage-Tenant", testkit.TenantID(1))
	req.Header.Set("X-Passage-Duties", duties)
	if ifMatch != "" {
		req.Header.Set("If-Match", ifMatch)
	}
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	return rec
}

func problemOf(t *testing.T, rec *httptest.ResponseRecorder) httpx.Problem {
	t.Helper()
	var p httpx.Problem
	if err := json.Unmarshal(rec.Body.Bytes(), &p); err != nil {
		t.Fatalf("problem body: %v: %s", err, rec.Body.String())
	}
	return p
}

func TestGetPlannedOrder_SendsETag(t *testing.T) {
	rec := doIfMatch(t, &fakeStore{orderVersion: 4}, http.MethodGet, orderPath, "", "", "planning.read")
	if rec.Code != http.StatusOK || rec.Header().Get("ETag") != `"4"` {
		t.Fatalf("status %d etag %q", rec.Code, rec.Header().Get("ETag"))
	}
	var o store.PlannedOrder
	if err := json.Unmarshal(rec.Body.Bytes(), &o); err != nil || o.Version != 4 {
		t.Fatalf("body version %d (%v)", o.Version, err)
	}
}

func TestPatch_Preconditions(t *testing.T) {
	cases := []struct {
		name, ifMatch, duties string
		status                int
		code                  string
	}{
		{"missing If-Match is 428", "", dutyOrders, http.StatusPreconditionRequired, codePrecondition},
		{"malformed If-Match is 400", `"v1"`, dutyOrders, http.StatusBadRequest, codeValidation},
		{"version 0 is not an order version", `"0"`, dutyOrders, http.StatusBadRequest, codeValidation},
		{"stale ETag is 412", `"7"`, dutyOrders, http.StatusPreconditionFailed, store.CodeVersionConflict},
		{"read duty cannot maintain", `"1"`, "planning.read", http.StatusForbidden, "AUTH-0003"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			st := &fakeStore{}
			rec := doIfMatch(t, st, http.MethodPatch, orderPath, `{"qty":"600"}`, tc.ifMatch, tc.duties)
			if rec.Code != tc.status || problemOf(t, rec).Code != tc.code {
				t.Fatalf("status %d body %s; want %d %s", rec.Code, rec.Body.String(), tc.status, tc.code)
			}
			if st.orderQty != "" {
				t.Fatalf("refused patch changed the order: qty %s", st.orderQty)
			}
		})
	}
}

func TestPatch_ChangesQtyAndBumpsETag(t *testing.T) {
	st := &fakeStore{}
	rec := doIfMatch(t, st, http.MethodPatch, orderPath, `{"qty":"600","deliveryDate":"2026-12-01"}`, `W/"1"`, dutyOrders)
	if rec.Code != http.StatusOK {
		t.Fatalf("patch status %d body %s", rec.Code, rec.Body.String())
	}
	if rec.Header().Get("ETag") != `"2"` || !strings.Contains(rec.Body.String(), `"qty":"600"`) {
		t.Fatalf("etag %q body %s", rec.Header().Get("ETag"), rec.Body.String())
	}
	if st.lastPatch.DeliveryDate == nil || *st.lastPatch.DeliveryDate != "2026-12-01" || st.lastPatch.Approve {
		t.Fatalf("patch passed to the store = %+v", st.lastPatch)
	}
}

func TestPatch_ApprovesOrder(t *testing.T) {
	st := &fakeStore{}
	rec := doIfMatch(t, st, http.MethodPatch, orderPath, `{"status":"approved"}`, `"1"`, dutyOrders)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"status":"approved"`) || !st.lastPatch.Approve {
		t.Fatalf("approve: status %d body %s", rec.Code, rec.Body.String())
	}
}

func TestPatch_RejectsInvalidBodies(t *testing.T) {
	cases := map[string]string{
		"empty patch":        `{}`,
		"firm through patch": `{"status":"firmed"}`,
		"zero qty":           `{"qty":"0"}`,
		"seven decimals":     `{"qty":"1.1234567"}`,
		"bad date":           `{"orderDate":"01/12/2026"}`,
		"vendor not a uuid":  `{"vendorId":"ACME"}`,
		"unknown field":      `{"itemId":"` + fgID + `"}`,
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			st := &fakeStore{}
			rec := doIfMatch(t, st, http.MethodPatch, orderPath, body, `"1"`, dutyOrders)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status %d body %s", rec.Code, rec.Body.String())
			}
			if st.orderVersion != 0 {
				t.Fatal("invalid patch reached the store")
			}
		})
	}
}

func TestPatch_UnknownIDIs404(t *testing.T) {
	rec := doIfMatch(t, &fakeStore{}, http.MethodPatch, "/v1/planning/planned-orders/not-a-uuid", `{"qty":"1"}`, `"1"`, dutyOrders)
	if rec.Code != http.StatusNotFound || problemOf(t, rec).Code != store.CodeOrderNotFound {
		t.Fatalf("status %d body %s", rec.Code, rec.Body.String())
	}
}

func TestDelete_ThenEditConflicts(t *testing.T) {
	st := &fakeStore{}
	if rec := doIfMatch(t, st, http.MethodDelete, orderPath, "", "", dutyOrders); rec.Code != http.StatusPreconditionRequired {
		t.Fatalf("delete without If-Match: status %d", rec.Code)
	}
	if rec := doIfMatch(t, st, http.MethodDelete, orderPath, "", `"1"`, dutyOrders); rec.Code != http.StatusNoContent {
		t.Fatalf("delete status %d body %s", rec.Code, rec.Body.String())
	}
	rec := doIfMatch(t, st, http.MethodPatch, orderPath, `{"qty":"600"}`, `"2"`, dutyOrders)
	if rec.Code != http.StatusConflict || problemOf(t, rec).Code != store.CodeOrderNotEditable {
		t.Fatalf("edit after delete: status %d body %s", rec.Code, rec.Body.String())
	}
}

const forecastPath = "/v1/planning/forecasts/BASE/lines"

const forecastBody = `{"reductionPrinciple":"transactions","lines":[{"itemId":"` + fgID + `","siteId":"` + siteID +
	`","periodStart":"2026-11-02","periodEnd":"2026-11-08","qty":"100","salesQty":"30"}]}`

func TestPutForecast_RequiresIfMatchAndDuty(t *testing.T) {
	if rec := doIfMatch(t, &fakeStore{}, http.MethodPut, forecastPath, forecastBody, "", dutyForecasts); rec.Code != http.StatusPreconditionRequired {
		t.Fatalf("no If-Match: status %d body %s", rec.Code, rec.Body.String())
	}
	if rec := doIfMatch(t, &fakeStore{}, http.MethodPut, forecastPath, forecastBody, `"0"`, dutyOrders); rec.Code != http.StatusForbidden {
		t.Fatalf("planned-order duty wrote a forecast: status %d", rec.Code)
	}
}

func TestPutForecast_CreatesModelAtVersionZero(t *testing.T) {
	st := &fakeStore{}
	rec := doIfMatch(t, st, http.MethodPut, forecastPath, forecastBody, `"0"`, dutyForecasts)
	if rec.Code != http.StatusOK {
		t.Fatalf("put status %d body %s", rec.Code, rec.Body.String())
	}
	if rec.Header().Get("ETag") != `"1"` || !strings.Contains(rec.Body.String(), `"updated":1`) {
		t.Fatalf("etag %q body %s", rec.Header().Get("ETag"), rec.Body.String())
	}
	rec = doIfMatch(t, st, http.MethodPut, forecastPath, forecastBody, `"0"`, dutyForecasts)
	if rec.Code != http.StatusPreconditionFailed {
		t.Fatalf("second write with the stale ETag: status %d", rec.Code)
	}
}

func TestPutForecast_RejectsInvalidCells(t *testing.T) {
	line := func(fields string) string {
		return `{"itemId":"` + fgID + `","siteId":"` + siteID + `",` + fields + `}`
	}
	cases := map[string]string{
		"no lines":                    `{"lines":[]}`,
		"percent without principle":   `{"reductionPercent":"10","lines":[` + line(`"periodStart":"2026-11-02","periodEnd":"2026-11-08","qty":"1"`) + `]}`,
		"percent with transactions":   `{"reductionPrinciple":"transactions","reductionPercent":"10","lines":[` + line(`"periodStart":"2026-11-02","periodEnd":"2026-11-08","qty":"1"`) + `]}`,
		"percent above 100":           `{"reductionPrinciple":"percent","reductionPercent":"101","lines":[` + line(`"periodStart":"2026-11-02","periodEnd":"2026-11-08","qty":"1"`) + `]}`,
		"unknown principle":           `{"reductionPrinciple":"magic","lines":[` + line(`"periodStart":"2026-11-02","periodEnd":"2026-11-08","qty":"1"`) + `]}`,
		"negative qty":                `{"lines":[` + line(`"periodStart":"2026-11-02","periodEnd":"2026-11-08","qty":"-1"`) + `]}`,
		"missing qty":                 `{"lines":[` + line(`"periodStart":"2026-11-02","periodEnd":"2026-11-08"`) + `]}`,
		"percent with 7 decimals":     `{"reductionPrinciple":"percent","reductionPercent":"10.1234567","lines":[` + line(`"periodStart":"2026-11-02","periodEnd":"2026-11-08","qty":"1"`) + `]}`,
		"negative sales":              `{"lines":[` + line(`"periodStart":"2026-11-02","periodEnd":"2026-11-08","qty":"1","salesQty":"-1"`) + `]}`,
		"end before start":            `{"lines":[` + line(`"periodStart":"2026-11-08","periodEnd":"2026-11-02","qty":"1"`) + `]}`,
		"consensus is workflow-owned": `{"lines":[` + line(`"periodStart":"2026-11-02","periodEnd":"2026-11-08","qty":"1","source":"consensus"`) + `]}`,
		"repeated cell": `{"lines":[` + line(`"periodStart":"2026-11-02","periodEnd":"2026-11-08","qty":"1"`) + `,` +
			line(`"periodStart":"2026-11-02","periodEnd":"2026-11-08","qty":"2"`) + `]}`,
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			st := &fakeStore{}
			rec := doIfMatch(t, st, http.MethodPut, forecastPath, body, `"0"`, dutyForecasts)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status %d body %s", rec.Code, rec.Body.String())
			}
			if len(st.models) != 0 {
				t.Fatal("invalid write reached the store")
			}
		})
	}
}

func TestGetForecast_UnknownModelIs404(t *testing.T) {
	rec := doIfMatch(t, &fakeStore{}, http.MethodGet, forecastPath, "", "", "planning.read")
	if rec.Code != http.StatusNotFound || problemOf(t, rec).Code != store.CodeForecastNotFound {
		t.Fatalf("unknown model: status %d body %s", rec.Code, rec.Body.String())
	}
}

func TestGetForecast_ReturnsModelAndETagAfterPut(t *testing.T) {
	st := &fakeStore{}
	if rec := doIfMatch(t, st, http.MethodPut, forecastPath, forecastBody, `"0"`, dutyForecasts); rec.Code != http.StatusOK {
		t.Fatalf("put status %d body %s", rec.Code, rec.Body.String())
	}
	rec := doIfMatch(t, st, http.MethodGet, forecastPath+"?itemId="+fgID, "", "", "planning.read")
	if rec.Code != http.StatusOK || rec.Header().Get("ETag") != `"1"` || !strings.Contains(rec.Body.String(), `"reductionPrinciple":"transactions"`) {
		t.Fatalf("get status %d etag %q body %s", rec.Code, rec.Header().Get("ETag"), rec.Body.String())
	}
	if rec := doIfMatch(t, st, http.MethodGet, forecastPath+"?siteId=x", "", "", "planning.read"); rec.Code != http.StatusBadRequest {
		t.Fatalf("bad site filter: status %d", rec.Code)
	}
}

// AC-22.6 end to end through the run: forecast 100 with sales 30 in the period
// is reduced to 70 by the forecast write, and the run nets the 70 as forecast
// demand next to the 120 of the sales line.
func TestRun_NetsReducedForecastOfTheModel(t *testing.T) {
	st := &fakeStore{models: map[string]store.ForecastModel{"BASE": {Code: "BASE"}}}
	otherItem := "00000000-0000-0000-0000-0000000000c1"
	st.demand = []store.ForecastDemandLine{
		{ItemID: fgID, PeriodStart: time.Date(2026, 12, 7, 0, 0, 0, 0, time.UTC), ReducedQty: decimal.RequireFromString("70")},
		{ItemID: otherItem, PeriodStart: time.Date(2026, 12, 7, 0, 0, 0, 0, time.UTC), ReducedQty: decimal.RequireFromString("5")},
	}
	body := strings.Replace(runBody, `"siteId"`, `"forecastModel":"BASE","siteId"`, 1)
	rec := do(t, st, http.MethodPost, "/v1/planning/plans/MAIN:run", body, "planning.run", true, "run-fc")
	if rec.Code != http.StatusCreated {
		t.Fatalf("status %d body %s", rec.Code, rec.Body.String())
	}
	if st.demandAt != "2026-11-02" {
		t.Fatalf("forecast read from %q; want the plan date", st.demandAt)
	}
	var forecast decimal.Decimal
	for _, p := range st.last.Result.Pegging {
		if p.DemandRefType == domain.RefForecast {
			if p.DemandRef != "FC:BASE:2026-12-07" || p.ItemID != fgID {
				t.Fatalf("forecast peg %+v", p)
			}
			forecast = forecast.Add(p.Qty)
		}
	}
	if !forecast.Equal(decimal.RequireFromString("70")) {
		t.Fatalf("pegged forecast demand = %s; want the reduced 70 (items outside the snapshot ignored)", forecast)
	}
}

func TestRun_UnknownForecastModelIs422(t *testing.T) {
	body := strings.Replace(runBody, `"siteId"`, `"forecastModel":"NOPE","siteId"`, 1)
	rec := do(t, &fakeStore{}, http.MethodPost, "/v1/planning/plans/MAIN:run", body, "planning.run", true, "run-nofc")
	if rec.Code != http.StatusUnprocessableEntity || problemOf(t, rec).Code != store.CodeForecastNotFound {
		t.Fatalf("status %d body %s", rec.Code, rec.Body.String())
	}
}

func TestRun_FenceValidation(t *testing.T) {
	body := strings.Replace(runBody, `"marginDays":0`, `"marginDays":0,"freezeFenceDays":-1,"futuresFenceDays":4000`, 1)
	rec := do(t, &fakeStore{}, http.MethodPost, "/v1/planning/plans/MAIN:run", body, "planning.run", true, "run-fence")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status %d body %s", rec.Code, rec.Body.String())
	}
	p := problemOf(t, rec)
	if p.Errors["items[0].coverage.freezeFenceDays"] == "" || p.Errors["items[0].coverage.futuresFenceDays"] == "" {
		t.Fatalf("field errors %v", p.Errors)
	}
}

// BR-22.4 through the API: a freeze fence wider than the demand date leaves the
// sales line short instead of planning an order.
func TestRun_FreezeFenceReachesTheEngine(t *testing.T) {
	st := &fakeStore{}
	body := strings.Replace(runBody, `"marginDays":0`, `"marginDays":0,"freezeFenceDays":60`, 1)
	rec := do(t, st, http.MethodPost, "/v1/planning/plans/MAIN:run", body, "planning.run", true, "run-freeze")
	if rec.Code != http.StatusCreated {
		t.Fatalf("status %d body %s", rec.Code, rec.Body.String())
	}
	if len(st.last.Result.Orders) != 0 {
		t.Fatalf("orders inside the freeze fence: %+v", st.last.Result.Orders)
	}
	shortSO := false
	for _, m := range st.last.Result.Messages {
		if m.Action == domain.ActionShortage && m.DemandRef == "SO-1" {
			shortSO = true
		}
	}
	if !shortSO {
		t.Fatalf("no shortage for SO-1: %+v", st.last.Result.Messages)
	}
}
