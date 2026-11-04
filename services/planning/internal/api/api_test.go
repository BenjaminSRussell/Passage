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

const (
	siteID = "00000000-0000-0000-0000-0000000000a1"
	fgID   = "00000000-0000-0000-0000-0000000000b1"
)

// fakeStore keeps runs in memory and honours the Idempotency-Key contract. Its
// maintenance and forecast methods are in maintain_test.go.
type fakeStore struct {
	runs       map[string]store.Version
	inputs     map[string]string
	last       store.RunRequest
	runErr     error
	pingErr    error
	lastWindow int
	orderID    string
	asOf       string // as-of date of the plan the projection reads; empty is 2026-11-02

	// One planned order for the maintenance tests; version 0 reads as 1.
	orderVersion int
	orderQty     string
	orderStatus  string
	lastPatch    store.OrderPatch
	// Forecast models by code and the forecast demand a run reads.
	models    map[string]store.ForecastModel
	generated []store.ForecastCellInput // cells of the last statistical generation
	keptCells int                       // cells that generation reports as kept
	demand    []store.ForecastDemandLine
	demandAt  string
	// Firming batches by Idempotency-Key and the planned orders firmed so far
	// (firm_test.go).
	firms    map[string]fakeFirm
	firmed   map[string]bool
	lastFirm store.FirmRequest
	// firmedSupply is what FirmedSupply returns for plan MAIN (carry_test.go).
	firmedSupply   []store.FirmedSupply
	firmedErr      error
	lastFirmedFrom time.Time
	// quotas is what RunQuotas returns (run_quota_test.go).
	runQuotas    []domain.RunQuota
	runQuotasErr error
	// allocKeys and allocated serve the allocation tests (allocation_test.go).
	allocKeys []store.AllocationKey
	allocated store.AllocateRequest

	// lastLimit is the page size the last list read received.
	lastLimit int
	// editors are the users who wrote cells of each forecast model
	// (consensus_test.go).
	editors map[string][]string
	// Coverage groups in creation order and the input hash stored per
	// Idempotency-Key (coverage_test.go).
	groups    []store.CoverageGroup
	groupKeys map[string]string
	// Item coverages in creation order and the input hash stored per
	// Idempotency-Key (itemcoverage_test.go).
	covs    []store.ItemCoverage
	covKeys map[string]string
	// Buffer profiles, stored buffers and quota arrangements with the input
	// hash stored per Idempotency-Key and the last list queries
	// (buffer_quota_test.go).
	profiles    []store.BufferProfile
	profileKeys map[string]string
	buffers     []store.DdmrpBuffer
	bufferQuery store.BufferQuery
	quotas      []store.QuotaArrangement
	quotaKeys   map[string]string
	quotaQuery  store.QuotaQuery
	// Stored site-scope coverage by item that runs read (run_coverage_test.go)
	// and the site of the last read.
	runCovs map[string]store.RunCoverage
	// runWhCovs is the stored warehouse-scope coverage by engine id (warehouse_test.go); lastWarehouse is the net requirements filter.
	runWhCovs map[string]store.RunCoverage
	// quotaItems are the item ids every RunQuotas call was sent (the store casts them to uuid).
	quotaItems    []string
	lastWarehouse string
	runCovSite    string
	runCovEntity  string
	// Scenarios and simulation runs (scenario_test.go).
	scenarios   []store.Scenario
	scenarioKey map[string]string
	// runBuffers is what RunBuffers returns (run_buffer_test.go).
	runBuffers []domain.RunBuffer
	// Planning strategy setup, the strategies runs read by item and the input
	// hash stored per Idempotency-Key (strategy_test.go).
	strategies    []store.Strategy
	strategyKeys  map[string]string
	runStrategies map[string]domain.StrategySpec
	lastSim       store.SimulationRequest
	simulations   int
}

func (f *fakeStore) RunPlan(_ context.Context, req store.RunRequest) (store.Version, bool, error) {
	f.last = req
	if f.runErr != nil {
		return store.Version{}, false, f.runErr
	}
	if f.runs == nil {
		f.runs, f.inputs = map[string]store.Version{}, map[string]string{}
	}
	if v, ok := f.runs[req.IdempotencyKey]; ok {
		if f.inputs[req.IdempotencyKey] != req.InputHash {
			return store.Version{}, false, httpx.NewProblem(http.StatusUnprocessableEntity, store.CodeIdempotencyReuse, "reuse")
		}
		return v, true, nil
	}
	f.inputs[req.IdempotencyKey] = req.InputHash
	v := store.Version{ID: testkit.TenantID(9), PlanCode: req.PlanCode, Number: len(f.runs) + 1, Status: "current",
		AsOf: req.AsOf.Format("2006-01-02"), ItemsPlanned: req.ItemsPlanned, PlannedOrders: len(req.Result.Orders),
		Messages: len(req.Result.Messages), PlanHash: req.Result.Hash}
	f.runs[req.IdempotencyKey] = v
	return v, false, nil
}

func (f *fakeStore) GetVersion(_ context.Context, code string, number int) (store.Version, error) {
	for _, v := range f.runs {
		if v.PlanCode == code && v.Number == number {
			return v, nil
		}
	}
	return store.Version{}, httpx.NewProblem(http.StatusNotFound, store.CodeVersionNotFound, "plan version not found")
}

func (f *fakeStore) ListVersions(_ context.Context, code string, _ int) ([]store.Version, error) {
	out := []store.Version{}
	for _, v := range f.runs {
		if v.PlanCode == code {
			out = append(out, v)
		}
	}
	return out, nil
}

func (f *fakeStore) ListPlannedOrders(context.Context, string, store.OrderFilter, int) ([]store.PlannedOrder, error) {
	return []store.PlannedOrder{}, nil
}

func (f *fakeStore) GetPlannedOrder(_ context.Context, id string) (store.PlannedOrder, error) {
	if f.orderID != "" && id != f.orderID {
		return store.PlannedOrder{}, httpx.NewProblem(http.StatusNotFound, store.CodeOrderNotFound, "planned order not found")
	}
	qty, status := f.orderQty, f.orderStatus
	if qty == "" {
		qty = "500"
	}
	if status == "" {
		status = "unprocessed"
	}
	return store.PlannedOrder{ID: id, VersionID: testkit.TenantID(9), Number: "PLN-000005", Qty: qty, Status: status, Version: f.currentVersion()}, nil
}

// PegsOf models a purchase PLN-000005 that covers 300 of the bom-line demand of
// production order PLN-000002 and 200 of sales line SO-2; PLN-000002 covers SO-1.
func (f *fakeStore) PegsOf(_ context.Context, _, supplyRef string) ([]domain.PegLine, error) {
	switch supplyRef {
	case "PLN-000005":
		return []domain.PegLine{
			{DemandRef: "PLN-000002/RM", DemandRefType: domain.RefBOM, Qty: decimal.RequireFromString("300")},
			{DemandRef: "SO-2", DemandRefType: domain.RefSales, Qty: decimal.RequireFromString("200")},
		}, nil
	case "PLN-000002":
		return []domain.PegLine{{DemandRef: "SO-1", DemandRefType: domain.RefSales, Qty: decimal.RequireFromString("100")}}, nil
	}
	return nil, nil
}

func (f *fakeStore) ListMessages(context.Context, string, string, string, string, int) ([]store.Message, error) {
	return []store.Message{}, nil
}

func (f *fakeStore) GetMessage(context.Context, string) (store.Message, error) {
	return store.Message{}, nil
}

func (f *fakeStore) DecideMessage(context.Context, string, int, store.MessageDecision) (store.Message, bool, error) {
	return store.Message{}, false, nil
}

func (f *fakeStore) Ping(context.Context) error { return f.pingErr }

func newRouter(st store.Store) *API {
	return New(st)
}

func do(t *testing.T, st store.Store, method, path, body, duties string, withTenant bool, key string) *httptest.ResponseRecorder {
	t.Helper()
	a := newRouter(st)
	router := httpx.NewRouter()
	a.Register(router)
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	if withTenant {
		req.Header.Set("X-Passage-Tenant", testkit.TenantID(1))
	}
	if duties != "" {
		req.Header.Set("X-Passage-Duties", duties)
	}
	if key != "" {
		req.Header.Set("Idempotency-Key", key)
	}
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	return rec
}

// runBody is the BR-22.2 worked example reduced to one item, in the API's wire shape.
const runBody = `{"asOf":"2026-11-02","siteId":"` + siteID + `","items":[
 {"itemId":"` + fgID + `","coverage":{"code":"requirement","marginDays":0},"onHand":"20","safetyStock":"10",
  "orderType":"production","leadTimeDays":7,"lot":{},
  "demand":[{"ref":"SO-1","refType":"sales-line","date":"2026-11-30","qty":"120"}],
  "receipts":[],"components":[]}]}`

func TestRun_RequiresTenantAndDuty(t *testing.T) {
	st := &fakeStore{}
	if rec := do(t, st, http.MethodPost, "/v1/planning/plans/MAIN:run", runBody, "planning.run", false, "k1"); rec.Code != http.StatusUnauthorized {
		t.Fatalf("no tenant: status %d", rec.Code)
	}
	if rec := do(t, st, http.MethodPost, "/v1/planning/plans/MAIN:run", runBody, "planning.read", true, "k1"); rec.Code != http.StatusForbidden {
		t.Fatalf("missing run duty: status %d", rec.Code)
	}
}

func TestRun_NeedsIdempotencyKey(t *testing.T) {
	rec := do(t, &fakeStore{}, http.MethodPost, "/v1/planning/plans/MAIN:run", runBody, "planning.run", true, "")
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "Idempotency-Key") {
		t.Fatalf("missing key: status %d body %s", rec.Code, rec.Body.String())
	}
}

func TestRun_CreatesThenReplays(t *testing.T) {
	st := &fakeStore{}
	first := do(t, st, http.MethodPost, "/v1/planning/plans/MAIN:run", runBody, "planning.run", true, "k1")
	if first.Code != http.StatusCreated {
		t.Fatalf("first run status %d body %s", first.Code, first.Body.String())
	}
	var v store.Version
	if err := json.Unmarshal(first.Body.Bytes(), &v); err != nil || v.PlannedOrders == 0 {
		t.Fatalf("version = %+v, %v", v, err)
	}
	replay := do(t, st, http.MethodPost, "/v1/planning/plans/MAIN:run", runBody, "planning.run", true, "k1")
	if replay.Code != http.StatusOK || replay.Header().Get("Idempotent-Replayed") != "true" {
		t.Fatalf("replay status %d header %q", replay.Code, replay.Header().Get("Idempotent-Replayed"))
	}
}

func TestRun_RejectsMalformedSnapshot(t *testing.T) {
	bad := strings.Replace(runBody, `"2026-11-02"`, `"02/11/2026"`, 1)
	rec := do(t, &fakeStore{}, http.MethodPost, "/v1/planning/plans/MAIN:run", bad, "planning.run", true, "k2")
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "asOf") {
		t.Fatalf("bad asOf: status %d body %s", rec.Code, rec.Body.String())
	}
}

type pegResponse struct {
	OrderID string  `json:"orderId"`
	Number  string  `json:"number"`
	Qty     string  `json:"qty"`
	Tree    pegNode `json:"tree"`
}

func TestPegging_ExplainsOrderQuantityThroughBOMLevels(t *testing.T) {
	rec := do(t, &fakeStore{}, http.MethodGet, "/v1/planning/planned-orders/"+fgID+"/pegging?depth=5", "", "planning.read", true, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("pegging status %d body %s", rec.Code, rec.Body.String())
	}
	var got pegResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.OrderID != fgID || got.Number != "PLN-000005" || got.Tree.Qty != "500" || len(got.Tree.Children) != 2 {
		t.Fatalf("response = %+v", got)
	}
	bom := got.Tree.Children[0]
	if bom.Ref != "PLN-000002" || bom.Qty != "300" || len(bom.Children) != 1 || bom.Children[0].Ref != "SO-1" {
		t.Fatalf("bom-line demand must expand through its driving order to SO-1: %+v", bom)
	}
	// Leaves carry an empty array, not null, so the web client can recurse.
	if !strings.Contains(rec.Body.String(), `"ref":"SO-2","refType":"sales-line","qty":"200","children":[]`) {
		t.Fatalf("leaf children must be []: %s", rec.Body.String())
	}
}

func TestPegging_RejectsDepthOutOfRangeAndUnknownOrder(t *testing.T) {
	if rec := do(t, &fakeStore{}, http.MethodGet, "/v1/planning/planned-orders/"+fgID+"/pegging?depth=21", "", "planning.read", true, ""); rec.Code != http.StatusBadRequest {
		t.Fatalf("depth 21: status %d", rec.Code)
	}
	if rec := do(t, &fakeStore{}, http.MethodGet, "/v1/planning/planned-orders/not-a-uuid/pegging", "", "planning.read", true, ""); rec.Code != http.StatusNotFound {
		t.Fatalf("bad id: status %d", rec.Code)
	}
}

func TestGetPlannedOrder(t *testing.T) {
	st := &fakeStore{orderID: fgID}
	rec := do(t, st, http.MethodGet, "/v1/planning/planned-orders/"+fgID, "", "planning.read", true, "")
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"number":"PLN-000005"`) {
		t.Fatalf("get: status %d body %s", rec.Code, rec.Body.String())
	}
	if rec := do(t, st, http.MethodGet, "/v1/planning/planned-orders/"+siteID, "", "planning.read", true, ""); rec.Code != http.StatusNotFound || !strings.Contains(rec.Body.String(), store.CodeOrderNotFound) {
		t.Fatalf("unknown: status %d body %s", rec.Code, rec.Body.String())
	}
	if rec := do(t, st, http.MethodGet, "/v1/planning/planned-orders/"+fgID, "", "planning.run", true, ""); rec.Code != http.StatusForbidden {
		t.Fatalf("reads need planning.read: status %d", rec.Code)
	}
}

func TestListVersions(t *testing.T) {
	st := &fakeStore{}
	do(t, st, http.MethodPost, "/v1/planning/plans/MAIN:run", runBody, "planning.run", true, "k1")
	rec := do(t, st, http.MethodGet, "/v1/planning/plans/MAIN/versions?limit=10", "", "planning.read", true, "")
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"number":1`) {
		t.Fatalf("list: status %d body %s", rec.Code, rec.Body.String())
	}
	if rec := do(t, st, http.MethodGet, "/v1/planning/plans/main/versions", "", "planning.read", true, ""); rec.Code != http.StatusBadRequest {
		t.Fatalf("lower-case plan code: status %d", rec.Code)
	}
}

func TestRun_RejectsInvalidLines(t *testing.T) {
	cases := map[string]struct{ from, to, field string }{
		"negative receipt":         {`"receipts":[]`, `"receipts":[{"ref":"PO-1","refType":"purchase-line","date":"2026-11-10","qty":"-5"}]`, "items[0].receipts"},
		"receipt bad date":         {`"receipts":[]`, `"receipts":[{"ref":"PO-1","refType":"purchase-line","date":"10/11/2026","qty":"5"}]`, "items[0].receipts.date"},
		"unknown ref type":         {`"refType":"sales-line"`, `"refType":"wish"`, "items[0].demand"},
		"bad component":            {`"components":[]`, `"components":[{"itemId":"x","qty":"1"}]`, "items[0].components"},
		"negative on hand":         {`"onHand":"20"`, `"onHand":"-1"`, "items[0].quantities"},
		"negative days":            {`"marginDays":0`, `"marginDays":0,"positiveDays":-1`, "items[0].coverage"},
		"negative margin":          {`"marginDays":0`, `"marginDays":-1`, "items[0].coverage.marginDays"},
		"issue margin over a year": {`"marginDays":0`, `"marginDays":0,"issueMarginDays":366`, "items[0].coverage.issueMarginDays"},
		"negative reorder margin":  {`"marginDays":0`, `"marginDays":0,"reorderMarginDays":-1`, "items[0].coverage.reorderMarginDays"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			body := strings.Replace(runBody, c.from, c.to, 1)
			rec := do(t, &fakeStore{}, http.MethodPost, "/v1/planning/plans/MAIN:run", body, "planning.run", true, "k")
			if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), `"`+c.field+`"`) {
				t.Fatalf("status %d body %s; want 400 naming %s", rec.Code, rec.Body.String(), c.field)
			}
		})
	}
}

func TestRun_MapsEngineErrorsTo422(t *testing.T) {
	cycle := `{"asOf":"2026-11-02","siteId":"` + siteID + `","items":[
 {"itemId":"` + fgID + `","coverage":{"code":"requirement"},"orderType":"production","components":[{"itemId":"` + siteID + `","qty":"1"}]},
 {"itemId":"` + siteID + `","coverage":{"code":"requirement"},"orderType":"production","components":[{"itemId":"` + fgID + `","qty":"1"}]}]}`
	dup := strings.Replace(runBody, `"components":[]}]}`, `"components":[]},{"itemId":"`+fgID+`","coverage":{"code":"requirement"},"orderType":"purchase"}]}`, 1)
	badCoverage := strings.Replace(runBody, `"code":"requirement"`, `"code":"lot-for-lot"`, 1)
	for name, c := range map[string]struct{ body, code string }{
		"cycle":          {cycle, "PLAN-0004"},
		"duplicate item": {dup, codeInput},
		"bad coverage":   {badCoverage, codeInput},
	} {
		t.Run(name, func(t *testing.T) {
			st := &fakeStore{}
			rec := do(t, st, http.MethodPost, "/v1/planning/plans/MAIN:run", c.body, "planning.run", true, "k")
			if rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), c.code) {
				t.Fatalf("status %d body %s; want 422 %s", rec.Code, rec.Body.String(), c.code)
			}
			if st.last.IdempotencyKey != "" {
				t.Fatalf("a refused run must not reach the store")
			}
		})
	}
}

func TestRun_PassesStoreConflictThrough(t *testing.T) {
	st := &fakeStore{runErr: httpx.NewProblem(http.StatusConflict, store.CodeAlreadyRunning, "a run of plan MAIN is already in progress")}
	rec := do(t, st, http.MethodPost, "/v1/planning/plans/MAIN:run", runBody, "planning.run", true, "k1")
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "PLAN-0001") {
		t.Fatalf("status %d body %s; want 409 PLAN-0001", rec.Code, rec.Body.String())
	}
}

func TestRun_SameKeyOnAnotherPlanIsAReuse(t *testing.T) {
	st := &fakeStore{}
	if rec := do(t, st, http.MethodPost, "/v1/planning/plans/MAIN:run", runBody, "planning.run", true, "k1"); rec.Code != http.StatusCreated {
		t.Fatalf("first: %d", rec.Code)
	}
	rec := do(t, st, http.MethodPost, "/v1/planning/plans/OTHER:run", runBody, "planning.run", true, "k1")
	if rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), store.CodeIdempotencyReuse) {
		t.Fatalf("status %d body %s; want 422 reuse", rec.Code, rec.Body.String())
	}
}

func TestRun_SendsTheEnginePlanToTheStore(t *testing.T) {
	st := &fakeStore{}
	if rec := do(t, st, http.MethodPost, "/v1/planning/plans/MAIN:run", runBody, "planning.run", true, "k1"); rec.Code != http.StatusCreated {
		t.Fatalf("status %d", rec.Code)
	}
	// FG: on hand 20 - safety 10 leaves 10 against SO-1 120 on 2026-11-30 (day 28),
	// so one production order of 110 due day 28, started day 21 (lead time 7).
	r := st.last.Result
	if len(r.Orders) != 1 || !r.Orders[0].Qty.Equal(decimal.RequireFromString("110")) || r.Orders[0].DeliveryDate != 28 || r.Orders[0].OrderDate != 21 {
		t.Fatalf("orders = %+v", r.Orders)
	}
	if st.last.PlanCode != "MAIN" || st.last.SiteID != siteID || st.last.ItemsPlanned != 1 || st.last.AsOf.Format("2006-01-02") != "2026-11-02" {
		t.Fatalf("run request = %+v", st.last)
	}
}

func TestMetricsCountRuns(t *testing.T) {
	a := newRouter(&fakeStore{})
	a.runsCompleted.Add(2)
	rec := httptest.NewRecorder()
	a.Metrics().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if !strings.Contains(rec.Body.String(), "planning_plan_runs_completed_total 2") {
		t.Fatalf("metrics = %s", rec.Body.String())
	}
}

func TestReadyz_FailsClosedWhenTheDatabaseIsDown(t *testing.T) {
	if rec := do(t, &fakeStore{}, http.MethodGet, "/readyz", "", "", false, ""); rec.Code != http.StatusOK {
		t.Fatalf("ready: status %d", rec.Code)
	}
	down := &fakeStore{pingErr: context.DeadlineExceeded}
	if rec := do(t, down, http.MethodGet, "/readyz", "", "", false, ""); rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("database down: status %d", rec.Code)
	}
}
