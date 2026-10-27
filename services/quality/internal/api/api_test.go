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
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/quality/internal/domain"
	"github.com/BenjaminSRussell/passage/services/quality/internal/store"
)

const (
	testTenant = "00000000-0000-0000-0000-000000000001"
	allDuties  = "QM_SETUP_MAINTAIN,QM_ORDER_MAINTAIN,QM_RESULTS_RECORD,QM_USAGE_DECISION,QM_VIEW"
)

// fakeStore is an in-memory store.Store for handler tests.
type fakeStore struct {
	groups      map[string]domain.TestGroup
	instruments map[string]domain.Instrument
	assocs      []domain.Association
	orders      map[string]domain.Order
	results     map[string][]domain.Result
	decisions   map[string]domain.Decision
	batches     map[string]bool
	recordCalls int
	lists       []domain.ComplianceList
	comps       map[string]domain.Composition
	evals       map[string][]domain.ComplianceResult
	studies     map[string]domain.Study
	obs         map[string][]domain.Observation
	ncrs        map[string]domain.Nonconformance
	ncrKeys     map[string]string
	capaKeys    map[string]string
	ncrFilter   store.NCRFilter
	certs       []domain.CertRecord
	certKeys    map[string]domain.CertRecord
	certNCRs    []domain.Nonconformance
	samples     []domain.Sample
	sampleKeys  map[string]domain.Sample
}

func newFake() *fakeStore {
	return &fakeStore{groups: map[string]domain.TestGroup{}, instruments: map[string]domain.Instrument{},
		orders: map[string]domain.Order{}, results: map[string][]domain.Result{}, decisions: map[string]domain.Decision{}, batches: map[string]bool{},
		comps: map[string]domain.Composition{}, evals: map[string][]domain.ComplianceResult{}, studies: map[string]domain.Study{}, obs: map[string][]domain.Observation{},
		ncrs: map[string]domain.Nonconformance{}, ncrKeys: map[string]string{}, capaKeys: map[string]string{}}
}

func (f *fakeStore) Ping(context.Context) error { return nil }

func (f *fakeStore) CreateTestGroup(_ context.Context, in domain.TestGroup, _ string) (domain.TestGroup, bool, error) {
	in.ID, in.Version = ids.New(), 1
	f.groups[in.ID] = in
	return in, true, nil
}

func (f *fakeStore) GetTestGroup(_ context.Context, id string) (domain.TestGroup, error) {
	g, ok := f.groups[id]
	if !ok {
		return g, httpx.NewProblem(http.StatusNotFound, domain.CodeTestGroupMissing, "test group not found")
	}
	return g, nil
}

func (f *fakeStore) CalibrateInstrument(_ context.Context, id string, calibrate func(domain.Instrument) (domain.Instrument, error)) (domain.Instrument, error) {
	cur, ok := f.instruments[id]
	if !ok {
		return cur, httpx.NewProblem(http.StatusNotFound, domain.CodeInstrumentMissing, "instrument not found")
	}
	next, err := calibrate(cur)
	if err != nil {
		return cur, err
	}
	f.instruments[id] = next
	return next, nil
}

func (f *fakeStore) ListTestGroups(context.Context, int) ([]domain.TestGroup, error) {
	out := []domain.TestGroup{}
	for _, g := range f.groups {
		out = append(out, g)
	}
	return out, nil
}

func (f *fakeStore) ListInstruments(context.Context, int) ([]domain.Instrument, error) {
	out := []domain.Instrument{}
	for _, i := range f.instruments {
		out = append(out, i)
	}
	return out, nil
}

func (f *fakeStore) ListOrders(_ context.Context, filter store.OrderFilter) ([]domain.Order, error) {
	out := []domain.Order{}
	for _, o := range f.orders {
		if filter.Status == "" || o.Status == filter.Status {
			out = append(out, o)
		}
	}
	return out, nil
}

func (f *fakeStore) HasResultBatch(_ context.Context, orderID, key string) (bool, error) {
	return f.batches[orderID+"|"+key], nil
}

func (f *fakeStore) GetDecision(_ context.Context, orderID string) (domain.Decision, bool, error) {
	for _, d := range f.decisions {
		if d.OrderID == orderID {
			return d, true, nil
		}
	}
	return domain.Decision{}, false, nil
}

func (f *fakeStore) CreateInstrument(_ context.Context, in domain.Instrument, _ string) (domain.Instrument, bool, error) {
	in.ID = ids.New()
	f.instruments[in.ID] = in
	return in, true, nil
}

func (f *fakeStore) GetInstruments(_ context.Context, list []string) (map[string]domain.Instrument, error) {
	out := map[string]domain.Instrument{}
	for _, id := range list {
		if i, ok := f.instruments[id]; ok {
			out[id] = i
		}
	}
	return out, nil
}

func (f *fakeStore) CreateAssociation(_ context.Context, in domain.Association, _ string) (domain.Association, bool, error) {
	in.ID = ids.New()
	f.assocs = append(f.assocs, in)
	return in, true, nil
}

func (f *fakeStore) ListAssociations(context.Context) ([]domain.Association, error) {
	return append([]domain.Association{}, f.assocs...), nil
}

func (f *fakeStore) CreateOrder(_ context.Context, in domain.Order, _ string) (domain.Order, bool, error) {
	in.Status, in.Version = domain.StatusCreated, 1
	f.orders[in.ID] = in
	return in, true, nil
}

func (f *fakeStore) GetOrder(_ context.Context, id string) (domain.Order, error) {
	o, ok := f.orders[id]
	if !ok {
		return o, httpx.NewProblem(http.StatusNotFound, domain.CodeOrderMissing, "quality order not found")
	}
	return o, nil
}

func (f *fakeStore) ListResults(_ context.Context, id string) ([]domain.Result, error) {
	return append([]domain.Result{}, f.results[id]...), nil
}

func (f *fakeStore) RecordResults(_ context.Context, id string, expected int, status string, batch []domain.Result, key string) (domain.Order, bool, error) {
	f.recordCalls++
	f.batches[id+"|"+key] = true
	o := f.orders[id]
	if o.Version != expected {
		return o, false, httpx.NewProblem(http.StatusPreconditionFailed, store.CodeVersionConflict, "stale")
	}
	o.Status, o.Version = status, o.Version+1
	f.orders[id] = o
	f.results[id] = append(f.results[id], batch...)
	return o, true, nil
}

func (f *fakeStore) FindDecisionByKey(_ context.Context, key string) (domain.Decision, bool, error) {
	d, ok := f.decisions[key]
	return d, ok, nil
}

func (f *fakeStore) PostUsageDecision(_ context.Context, id string, expected int, d domain.Decision, key string) (domain.Order, domain.Decision, error) {
	o := f.orders[id]
	if o.Version != expected || o.Status != domain.StatusPending {
		return o, d, httpx.NewProblem(http.StatusConflict, domain.CodeStatus, "not pending")
	}
	if prior, ok := f.decisions[key]; ok && prior.OrderID != id {
		return o, d, httpx.NewProblem(http.StatusConflict, domain.CodeClosed, "key used elsewhere")
	}
	o.Status, o.Version = domain.StatusClosed, o.Version+1
	f.orders[id] = o
	d.ID, d.OrderID = ids.New(), id
	if key != "" {
		f.decisions[key] = d
	}
	return o, d, nil
}

type reqSpec struct {
	method, path, body, duties, key, ifMatch, user string
}

func call(h http.Handler, s reqSpec) *httptest.ResponseRecorder {
	req := httptest.NewRequest(s.method, s.path, strings.NewReader(s.body))
	req.Header.Set("X-Passage-Tenant", testTenant)
	req.Header.Set("X-Passage-Duties", s.duties)
	req.Header.Set("Content-Type", "application/json")
	if s.key != "" {
		req.Header.Set("Idempotency-Key", s.key)
	}
	if s.ifMatch != "" {
		req.Header.Set("If-Match", s.ifMatch)
	}
	if s.user != "" {
		req.Header.Set("X-Passage-User", s.user)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func newHandler(s store.Store) http.Handler {
	r := httpx.NewRouter()
	New(s).Register(r)
	return r
}

func TestSizeSample_AQLFixtureOverHTTP(t *testing.T) {
	h := newHandler(newFake())
	rec := call(h, reqSpec{method: http.MethodPost, path: "/v1/quality/sampling-plans:size", body: `{"lotQty":"51","method":"AQL","aql":"2.5"}`, duties: "QM_VIEW"})
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"sampleQty":"20"`) || !strings.Contains(rec.Body.String(), `"codeLetter":"E"`) {
		t.Fatalf("status %d body %s", rec.Code, rec.Body.String())
	}
}

func TestDuties_AreEnforced(t *testing.T) {
	h := newHandler(newFake())
	rec := call(h, reqSpec{method: http.MethodPost, path: "/v1/quality/test-groups", body: `{}`, duties: "QM_VIEW", key: "k"})
	if rec.Code != http.StatusForbidden {
		t.Fatalf("missing QM_SETUP_MAINTAIN: status %d", rec.Code)
	}
	rec = call(h, reqSpec{method: http.MethodPost, path: "/v1/quality/sampling-plans:size", body: `{}`})
	if rec.Code != http.StatusForbidden {
		t.Fatalf("no duties on sizing: status %d", rec.Code)
	}
}

func TestIdempotencyKeyIsRequired(t *testing.T) {
	h := newHandler(newFake())
	rec := call(h, reqSpec{method: http.MethodPost, path: "/v1/quality/test-groups", body: `{"code":"GRP-1","tests":[]}`, duties: allDuties})
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "Idempotency-Key") {
		t.Fatalf("status %d body %s", rec.Code, rec.Body.String())
	}
}

// groupBody builds a one-test attribute group and returns its id after creation.
func createGroup(t *testing.T, h http.Handler, required bool) (groupID, testID, outcomeID string) {
	t.Helper()
	testID, outcomeID = ids.New(), ids.New()
	body, _ := json.Marshal(map[string]any{"code": "GRP-1", "tests": []map[string]any{{
		"testId": testID, "name": "appearance", "kind": "ATTRIBUTE", "required": required, "passingOutcomeIds": []string{outcomeID},
	}}})
	rec := call(h, reqSpec{method: http.MethodPost, path: "/v1/quality/test-groups", body: string(body), duties: allDuties, key: ids.New()})
	if rec.Code != http.StatusCreated {
		t.Fatalf("create group: %d %s", rec.Code, rec.Body.String())
	}
	var g domain.TestGroup
	if err := json.Unmarshal(rec.Body.Bytes(), &g); err != nil {
		t.Fatal(err)
	}
	return g.ID, testID, outcomeID
}

func TestManualOrder_SizesAndRequiresAssociationForOthers(t *testing.T) {
	f := newFake()
	h := newHandler(f)
	groupID, _, _ := createGroup(t, h, true)
	body := `{"trigger":"MANUAL","sourceRef":{"type":"purchase_line","id":"` + ids.New() + `"},"itemId":"` + ids.New() +
		`","siteId":"` + ids.New() + `","warehouseId":"` + ids.New() + `","lotQty":"51","uom":"EA","testGroupId":"` + groupID +
		`","samplingPlan":{"method":"AQL","aql":"2.5"}}`
	rec := call(h, reqSpec{method: http.MethodPost, path: "/v1/quality/quality-orders", body: body, duties: allDuties, key: ids.New()})
	if rec.Code != http.StatusCreated || !strings.Contains(rec.Body.String(), `"sampleQty":"20"`) || rec.Header().Get("ETag") != `"1"` {
		t.Fatalf("manual order: %d %s", rec.Code, rec.Body.String())
	}
	noAssoc := `{"trigger":"PURCHASE_RECEIPT","sourceRef":{"type":"receipt_line","id":"` + ids.New() + `"},"itemId":"` + ids.New() +
		`","siteId":"` + ids.New() + `","warehouseId":"` + ids.New() + `","lotQty":"10","uom":"EA"}`
	rec = call(h, reqSpec{method: http.MethodPost, path: "/v1/quality/quality-orders", body: noAssoc, duties: allDuties, key: ids.New()})
	if rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), "QM-0100") {
		t.Fatalf("no association: %d %s", rec.Code, rec.Body.String())
	}
}

// seedPending puts an order in PENDING_DECISION, optionally with its results recorded.
func seedPending(t *testing.T, f *fakeStore, h http.Handler, lot string, withResults bool) string {
	t.Helper()
	groupID, testID, outcomeID := createGroup(t, h, true)
	orderID := ids.New()
	f.orders[orderID] = domain.Order{ID: orderID, LotQty: decimal.RequireFromString(lot), SampleQty: decimal.NewFromInt(1),
		TestGroupID: groupID, Status: domain.StatusPending, Version: 3}
	if withResults {
		f.results[orderID] = []domain.Result{{TestID: testID, SampleNo: 1, OutcomeID: outcomeID, Passed: true}}
	}
	return orderID
}

func TestUsageDecision_IfMatchSplitAndReplay(t *testing.T) {
	f := newFake()
	h := newHandler(f)
	orderID := seedPending(t, f, h, "10", true)
	path := "/v1/quality/quality-orders/" + orderID + ":usage-decision"
	accept10 := `{"lines":[{"decision":"ACCEPT","quantity":"10"}]}`

	if rec := call(h, reqSpec{method: http.MethodPost, path: path, body: accept10, duties: allDuties, key: "d1"}); rec.Code != http.StatusPreconditionRequired {
		t.Fatalf("no If-Match: %d", rec.Code)
	}
	if rec := call(h, reqSpec{method: http.MethodPost, path: path, body: accept10, duties: allDuties, key: "d1", ifMatch: `"2"`}); rec.Code != http.StatusPreconditionFailed {
		t.Fatalf("stale If-Match: %d", rec.Code)
	}
	short := `{"lines":[{"decision":"ACCEPT","quantity":"9"}]}`
	if rec := call(h, reqSpec{method: http.MethodPost, path: path, body: short, duties: allDuties, key: "d1", ifMatch: `"3"`}); rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), "QM-0104") {
		t.Fatalf("split mismatch: %d %s", rec.Code, rec.Body.String())
	}
	rec := call(h, reqSpec{method: http.MethodPost, path: path, body: accept10, duties: allDuties, key: "d1", ifMatch: `"3"`})
	if rec.Code != http.StatusCreated || f.orders[orderID].Status != domain.StatusClosed {
		t.Fatalf("decision: %d %s", rec.Code, rec.Body.String())
	}
	replay := call(h, reqSpec{method: http.MethodPost, path: path, body: accept10, duties: allDuties, key: "d1", ifMatch: `"3"`})
	if replay.Code != http.StatusOK {
		t.Fatalf("replay should answer 200: %d %s", replay.Code, replay.Body.String())
	}
}

func TestUsageDecision_NeedsRequiredResults(t *testing.T) {
	f := newFake()
	h := newHandler(f)
	orderID := seedPending(t, f, h, "10", false)
	path := "/v1/quality/quality-orders/" + orderID + ":usage-decision"
	rec := call(h, reqSpec{method: http.MethodPost, path: path, body: `{"lines":[{"decision":"ACCEPT","quantity":"10"}]}`, duties: allDuties, key: "d2", ifMatch: `"3"`})
	if rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), "QM-0105") {
		t.Fatalf("missing results: %d %s", rec.Code, rec.Body.String())
	}
}

func TestResults_OverdueInstrumentIsRefused(t *testing.T) {
	f := newFake()
	h := newHandler(f)
	groupID, testID, outcomeID := createGroup(t, h, true)
	orderID := ids.New()
	f.orders[orderID] = domain.Order{ID: orderID, LotQty: decimal.NewFromInt(5), SampleQty: decimal.NewFromInt(1), TestGroupID: groupID, Status: domain.StatusCreated, Version: 1}
	instID := ids.New()
	calibrated := time.Now().UTC().AddDate(0, 0, -60)
	f.instruments[instID] = domain.Instrument{ID: instID, Code: "PH-01", DueAt: calibrated.AddDate(0, 0, 30)}

	body := `{"results":[{"testId":"` + testID + `","sampleNo":1,"outcomeId":"` + outcomeID + `","instrumentId":"` + instID + `"}]}`
	rec := call(h, reqSpec{method: http.MethodPost, path: "/v1/quality/quality-orders/" + orderID + "/results", body: body, duties: allDuties, key: "r1"})
	if rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), "INSTRUMENT_OVERDUE") {
		t.Fatalf("overdue instrument: %d %s", rec.Code, rec.Body.String())
	}
	if f.recordCalls != 0 {
		t.Fatal("no result may be written when the instrument is overdue")
	}
}

func TestResults_CompleteBatchMovesOrderToPending(t *testing.T) {
	f := newFake()
	h := newHandler(f)
	groupID, testID, outcomeID := createGroup(t, h, true)
	orderID := ids.New()
	f.orders[orderID] = domain.Order{ID: orderID, LotQty: decimal.NewFromInt(5), SampleQty: decimal.NewFromInt(1), TestGroupID: groupID, Status: domain.StatusCreated, Version: 1}
	body := `{"results":[{"testId":"` + testID + `","sampleNo":1,"outcomeId":"` + outcomeID + `"}]}`
	rec := call(h, reqSpec{method: http.MethodPost, path: "/v1/quality/quality-orders/" + orderID + "/results", body: body, duties: allDuties, key: "r2"})
	if rec.Code != http.StatusCreated || f.orders[orderID].Status != domain.StatusPending {
		t.Fatalf("complete batch: %d %s status %s", rec.Code, rec.Body.String(), f.orders[orderID].Status)
	}
}

func TestInstrumentCreate_RejectsBadInterval(t *testing.T) {
	h := newHandler(newFake())
	body := `{"code":"PH-01","name":"pH meter","calibrationIntervalDays":0,"lastCalibratedAt":"2026-01-01T00:00:00Z"}`
	rec := call(h, reqSpec{method: http.MethodPost, path: "/v1/quality/instruments", body: body, duties: allDuties, key: "i1"})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("interval 0: %d %s", rec.Code, rec.Body.String())
	}
}

// orderWithGroup seeds an order in status with a one-test required attribute group.
func orderWithGroup(t *testing.T, f *fakeStore, h http.Handler, status string, version int) (orderID, testID, outcomeID string) {
	t.Helper()
	groupID, testID, outcomeID := createGroup(t, h, true)
	orderID = ids.New()
	f.orders[orderID] = domain.Order{ID: orderID, LotQty: decimal.NewFromInt(5), SampleQty: decimal.NewFromInt(1), TestGroupID: groupID, Status: status, Version: version}
	return orderID, testID, outcomeID
}

func TestResults_ReplayOfCompletingBatchAnswers200(t *testing.T) {
	f := newFake()
	h := newHandler(f)
	orderID, testID, outcomeID := orderWithGroup(t, f, h, domain.StatusCreated, 1)
	body := `{"results":[{"testId":"` + testID + `","sampleNo":1,"outcomeId":"` + outcomeID + `"}]}`
	path := "/v1/quality/quality-orders/" + orderID + "/results"
	if rec := call(h, reqSpec{method: http.MethodPost, path: path, body: body, duties: allDuties, key: "batch-1"}); rec.Code != http.StatusCreated {
		t.Fatalf("first batch: %d %s", rec.Code, rec.Body.String())
	}
	// The order is now PENDING_DECISION; a retry of the same batch must not be judged as new results.
	rec := call(h, reqSpec{method: http.MethodPost, path: path, body: body, duties: allDuties, key: "batch-1"})
	if rec.Code != http.StatusOK || f.recordCalls != 1 {
		t.Fatalf("replay: %d %s, record calls %d", rec.Code, rec.Body.String(), f.recordCalls)
	}
}

func TestResults_PendingOrderTakesOnlyRetests(t *testing.T) {
	f := newFake()
	h := newHandler(f)
	orderID, testID, outcomeID := orderWithGroup(t, f, h, domain.StatusPending, 2)
	f.results[orderID] = []domain.Result{{TestID: testID, SampleNo: 1, OutcomeID: outcomeID, Passed: false}}
	path := "/v1/quality/quality-orders/" + orderID + "/results"
	first := `{"results":[{"testId":"` + testID + `","sampleNo":1,"outcomeId":"` + outcomeID + `"}]}`
	if rec := call(h, reqSpec{method: http.MethodPost, path: path, body: first, duties: allDuties, key: "p1"}); rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "QM-0115") {
		t.Fatalf("non-retest on pending: %d %s", rec.Code, rec.Body.String())
	}
	skip := `{"results":[{"testId":"` + testID + `","sampleNo":1,"retestNo":2,"outcomeId":"` + outcomeID + `"}]}`
	if rec := call(h, reqSpec{method: http.MethodPost, path: path, body: skip, duties: allDuties, key: "p2"}); rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "retestNo") {
		t.Fatalf("retest 2 without retest 1: %d %s", rec.Code, rec.Body.String())
	}
	retest := `{"results":[{"testId":"` + testID + `","sampleNo":1,"retestNo":1,"outcomeId":"` + outcomeID + `"}]}`
	rec := call(h, reqSpec{method: http.MethodPost, path: path, body: retest, duties: allDuties, key: "p3"})
	if rec.Code != http.StatusCreated || f.orders[orderID].Status != domain.StatusPending {
		t.Fatalf("retest on pending: %d %s", rec.Code, rec.Body.String())
	}
	var view struct {
		Evaluation domain.Evaluation `json:"evaluation"`
		TestGroup  domain.TestGroup  `json:"testGroup"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &view); err != nil {
		t.Fatal(err)
	}
	if !view.Evaluation.Passed || view.Evaluation.DefectiveSamples != 0 || len(view.TestGroup.Tests) != 1 {
		t.Fatalf("passing retest replaces the failed result: %+v", view)
	}
}

func TestResults_ClosedOrderIsQM0109(t *testing.T) {
	f := newFake()
	h := newHandler(f)
	orderID, testID, outcomeID := orderWithGroup(t, f, h, domain.StatusClosed, 4)
	body := `{"results":[{"testId":"` + testID + `","sampleNo":1,"retestNo":1,"outcomeId":"` + outcomeID + `"}]}`
	rec := call(h, reqSpec{method: http.MethodPost, path: "/v1/quality/quality-orders/" + orderID + "/results", body: body, duties: allDuties, key: "c1"})
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "QM-0109") {
		t.Fatalf("results on closed order: %d %s", rec.Code, rec.Body.String())
	}
	rec = call(h, reqSpec{method: http.MethodPost, path: "/v1/quality/quality-orders/" + orderID + ":usage-decision", body: `{"lines":[{"decision":"ACCEPT","quantity":"5"}]}`, duties: allDuties, key: "c2", ifMatch: `"4"`})
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "QM-0109") {
		t.Fatalf("decision on closed order: %d %s", rec.Code, rec.Body.String())
	}
}

func TestUsageDecision_KeyFromAnotherOrderIsRefused(t *testing.T) {
	f := newFake()
	h := newHandler(f)
	first := seedPending(t, f, h, "10", true)
	second := seedPending(t, f, h, "10", true)
	body := `{"lines":[{"decision":"ACCEPT","quantity":"10"}]}`
	if rec := call(h, reqSpec{method: http.MethodPost, path: "/v1/quality/quality-orders/" + first + ":usage-decision", body: body, duties: allDuties, key: "same", ifMatch: `"3"`}); rec.Code != http.StatusCreated {
		t.Fatalf("first decision: %d %s", rec.Code, rec.Body.String())
	}
	rec := call(h, reqSpec{method: http.MethodPost, path: "/v1/quality/quality-orders/" + second + ":usage-decision", body: body, duties: allDuties, key: "same", ifMatch: `"3"`})
	if rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), "idempotency_key_reuse") || f.orders[second].Status != domain.StatusPending {
		t.Fatalf("reused key: %d %s", rec.Code, rec.Body.String())
	}
}

func TestAssociation_DefaultsAndPlanValidation(t *testing.T) {
	f := newFake()
	h := newHandler(f)
	groupID, _, _ := createGroup(t, h, true)
	body := `{"trigger":"PURCHASE_RECEIPT","itemScope":"ALL","partyScope":"ALL","siteScope":"ALL","sequence":1,"testGroupId":"` + groupID + `","samplingPlan":{"method":"AQL","aql":"2.5"}}`
	rec := call(h, reqSpec{method: http.MethodPost, path: "/v1/quality/associations", body: body, duties: allDuties, key: "a1"})
	if rec.Code != http.StatusCreated || f.assocs[0].Plan.Severity != domain.SeverityNormal || f.assocs[0].Plan.InspectionLevel != "II" {
		t.Fatalf("defaults: %d %s %+v", rec.Code, rec.Body.String(), f.assocs)
	}
	bad := strings.Replace(body, `"aql":"2.5"`, `"aql":"2.5","severity":"TIGHTENED"`, 1)
	rec = call(h, reqSpec{method: http.MethodPost, path: "/v1/quality/associations", body: bad, duties: allDuties, key: "a2"})
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "samplingPlan.severity") {
		t.Fatalf("tightened AQL plan: %d %s", rec.Code, rec.Body.String())
	}
	rec = call(h, reqSpec{method: http.MethodGet, path: "/v1/quality/associations", duties: "QM_VIEW"})
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"items":[`) {
		t.Fatalf("list associations: %d %s", rec.Code, rec.Body.String())
	}
}

func TestManualOrder_RejectsUnsizablePlan(t *testing.T) {
	f := newFake()
	h := newHandler(f)
	groupID, _, _ := createGroup(t, h, true)
	body := `{"trigger":"MANUAL","sourceRef":{"type":"manual","id":"` + ids.New() + `"},"itemId":"` + ids.New() +
		`","siteId":"` + ids.New() + `","warehouseId":"` + ids.New() + `","lotQty":"51","uom":"EA","testGroupId":"` + groupID +
		`","samplingPlan":{"method":"AQL","aql":"6.5"}}`
	rec := call(h, reqSpec{method: http.MethodPost, path: "/v1/quality/quality-orders", body: body, duties: allDuties, key: ids.New()})
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "samplingPlan.aql") || len(f.orders) != 0 {
		t.Fatalf("unloaded AQL: %d %s", rec.Code, rec.Body.String())
	}
}

func TestLists_FilterValidationAndDuty(t *testing.T) {
	f := newFake()
	h := newHandler(f)
	orderWithGroup(t, f, h, domain.StatusPending, 2)
	orderWithGroup(t, f, h, domain.StatusClosed, 3)
	rec := call(h, reqSpec{method: http.MethodGet, path: "/v1/quality/quality-orders?status=PENDING_DECISION", duties: "QM_VIEW"})
	var body struct {
		Items []domain.Order `json:"items"`
	}
	if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &body) != nil || len(body.Items) != 1 || body.Items[0].Status != domain.StatusPending {
		t.Fatalf("status filter: %d %s", rec.Code, rec.Body.String())
	}
	for _, q := range []string{"status=DONE", "siteId=x", "limit=0", "limit=501"} {
		if rec := call(h, reqSpec{method: http.MethodGet, path: "/v1/quality/quality-orders?" + q, duties: "QM_VIEW"}); rec.Code != http.StatusBadRequest {
			t.Errorf("%s: %d", q, rec.Code)
		}
	}
	if rec := call(h, reqSpec{method: http.MethodGet, path: "/v1/quality/instruments", duties: "QM_RESULTS_RECORD"}); rec.Code != http.StatusForbidden {
		t.Errorf("instruments without QM_VIEW: %d", rec.Code)
	}
}

func TestInstrumentList_ShowsCalibrationStatus(t *testing.T) {
	f := newFake()
	h := newHandler(f)
	now := time.Now().UTC()
	f.instruments["a"] = domain.Instrument{ID: "a", Code: "CAL-OK", DueAt: now.AddDate(0, 0, 60)}
	f.instruments["b"] = domain.Instrument{ID: "b", Code: "CAL-SOON", DueAt: now.AddDate(0, 0, 3)}
	f.instruments["c"] = domain.Instrument{ID: "c", Code: "CAL-LATE", DueAt: now.AddDate(0, 0, -1)}
	rec := call(h, reqSpec{method: http.MethodGet, path: "/v1/quality/instruments", duties: "QM_VIEW"})
	var body struct {
		Items []domain.Instrument `json:"items"`
	}
	if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &body) != nil {
		t.Fatalf("list: %d %s", rec.Code, rec.Body.String())
	}
	got := map[string]string{}
	for _, i := range body.Items {
		got[i.Code] = i.CalibrationStatus
	}
	if got["CAL-OK"] != domain.CalibrationOK || got["CAL-SOON"] != domain.CalibrationDueSoon || got["CAL-LATE"] != domain.CalibrationOverdue {
		t.Fatalf("statuses: %v", got)
	}
}

func TestCalibrateInstrument_MovesDueDateAndRefusesFuture(t *testing.T) {
	f := newFake()
	h := newHandler(f)
	id := ids.New()
	last := time.Now().UTC().AddDate(0, 0, -400)
	f.instruments[id] = domain.Instrument{ID: id, Code: "PH-01", CalibrationIntervalDays: 365, LastCalibratedAt: last, DueAt: last.AddDate(0, 0, 365)}
	path := "/v1/quality/instruments/" + id + ":calibrate"
	at := time.Now().UTC().Add(-time.Hour).Truncate(time.Second)
	rec := call(h, reqSpec{method: http.MethodPost, path: path, body: `{"calibratedAt":"` + at.Format(time.RFC3339) + `"}`, duties: "QM_SETUP_MAINTAIN"})
	if rec.Code != http.StatusOK || !f.instruments[id].DueAt.Equal(at.AddDate(0, 0, 365)) || !strings.Contains(rec.Body.String(), `"calibrationStatus":"IN_CALIBRATION"`) {
		t.Fatalf("calibrate: %d %s", rec.Code, rec.Body.String())
	}
	future := time.Now().UTC().Add(time.Hour).Format(time.RFC3339)
	if rec := call(h, reqSpec{method: http.MethodPost, path: path, body: `{"calibratedAt":"` + future + `"}`, duties: "QM_SETUP_MAINTAIN"}); rec.Code != http.StatusBadRequest {
		t.Fatalf("future calibration: %d %s", rec.Code, rec.Body.String())
	}
	if rec := call(h, reqSpec{method: http.MethodPost, path: "/v1/quality/instruments/" + ids.New() + ":calibrate", body: `{"calibratedAt":"` + at.Format(time.RFC3339) + `"}`, duties: "QM_SETUP_MAINTAIN"}); rec.Code != http.StatusNotFound {
		t.Fatalf("unknown instrument: %d", rec.Code)
	}
	if rec := call(h, reqSpec{method: http.MethodPost, path: path, body: `{}`, duties: "QM_VIEW"}); rec.Code != http.StatusForbidden {
		t.Fatalf("calibration needs QM_SETUP_MAINTAIN: %d", rec.Code)
	}
}
