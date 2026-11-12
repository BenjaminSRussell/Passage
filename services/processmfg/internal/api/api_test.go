package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/services/processmfg/internal/domain"
	"github.com/BenjaminSRussell/passage/services/processmfg/internal/store"
)

const (
	tenantID    = "00000000-0000-0000-0000-000000000001"
	entityID    = "11111111-0000-4000-8000-000000000001"
	otherEntity = "11111111-0000-4000-8000-000000000002"
	formulaID   = "22222222-0000-4000-8000-000000000001"
	orderID     = "33333333-0000-4000-8000-000000000001"
	itemA       = "44444444-0000-4000-8000-00000000000a"
	itemB       = "44444444-0000-4000-8000-00000000000b"
	itemC       = "44444444-0000-4000-8000-00000000000c"
	itemOut     = "44444444-0000-4000-8000-0000000000ff"
)

// fakeStore records the calls the handlers make and returns canned results.
// The store's own behaviour is covered by the database tests in internal/store.
type fakeStore struct {
	target        *store.RecipeTarget
	claims        int
	gotTarget     store.RecipeTarget
	link          store.ControlRecipeLink
	linkSet       int
	formula       store.Formula
	order         store.BatchOrder
	err           error
	replay        bool
	gotFormula    store.Formula
	gotOrder      store.NewBatchOrder
	gotIdem       store.Idem
	gotFilter     store.OrderFilter
	gotLimit      int
	gotVersion    int
	gotSize       decimal.Decimal
	gotCmd        string
	gotReport     store.ReportInput
	gotEnd        store.EndInput
	batch         store.Batch
	gotDef        store.AttributeDefinition
	gotNew        store.NewBatch
	gotValues     map[string]domain.Value
	gotTo         string
	gotReason     string
	gotItem       string
	gotBatches    store.BatchFilter
	gotOnHand     []store.OnHand
	cands         []store.PickCandidate
	potency       string
	registered    []string
	gotIDs        []string
	reqs          []store.CustomerRequirement
	gotReq        store.CustomerRequirement
	gotRule       store.InheritanceRule
	gotDerive     store.DeriveInput
	gotRuleFilt   store.RuleFilter
	gotDerivFilt  store.DerivationFilter
	gotReqFilt    store.RequirementFilter
	gotReqWho     store.ReqScope
	gotReqDate    string
	gotReqEntity  string
	ebr           ebrCalls
	gotRework     store.NewRework
	gotReworkFilt store.ReworkFilter
	gotRelease    bool
}

func (f *fakeStore) CreateFormula(_ context.Context, in store.Formula, idem store.Idem) (store.Formula, bool, error) {
	f.gotFormula, f.gotIdem = in, idem
	if f.err != nil {
		return store.Formula{}, false, f.err
	}
	in.ID = formulaID
	return in, !f.replay, nil
}

func (f *fakeStore) ListFormulas(_ context.Context, limit int) ([]store.Formula, error) {
	f.gotLimit = limit
	return []store.Formula{f.formula}, f.err
}

func (f *fakeStore) GetFormula(_ context.Context, _ string) (store.Formula, error) {
	return f.formula, f.err
}

func (f *fakeStore) CreateBatchOrder(_ context.Context, in store.NewBatchOrder, idem store.Idem) (store.BatchOrder, bool, error) {
	f.gotOrder, f.gotIdem = in, idem
	return f.order, !f.replay, f.err
}

func (f *fakeStore) ListBatchOrders(_ context.Context, filter store.OrderFilter) ([]store.BatchOrder, error) {
	f.gotFilter = filter
	return []store.BatchOrder{f.order}, f.err
}

func (f *fakeStore) GetBatchOrder(_ context.Context, _ string) (store.BatchOrder, error) {
	return f.order, f.err
}

func (f *fakeStore) RescaleBatchOrder(_ context.Context, _ string, size decimal.Decimal, version int, idem store.Idem) (store.BatchOrder, bool, error) {
	f.gotSize, f.gotVersion, f.gotIdem = size, version, idem
	return f.order, !f.replay, f.err
}

func (f *fakeStore) AdvanceBatchOrder(_ context.Context, _ string, cmd string, version int, idem store.Idem) (store.BatchOrder, bool, error) {
	f.gotCmd, f.gotVersion, f.gotIdem = cmd, version, idem
	return f.order, !f.replay, f.err
}

func (f *fakeStore) ReportBatchOrder(_ context.Context, _ string, in store.ReportInput, version int, idem store.Idem) (store.BatchOrder, bool, error) {
	f.gotReport, f.gotVersion, f.gotIdem = in, version, idem
	return f.order, !f.replay, f.err
}

func (f *fakeStore) EndBatchOrder(_ context.Context, _ string, in store.EndInput, version int, idem store.Idem) (store.BatchOrder, bool, error) {
	f.gotEnd, f.gotVersion, f.gotIdem = in, version, idem
	return f.order, !f.replay, f.err
}

func (f *fakeStore) DefineAttribute(_ context.Context, in store.AttributeDefinition, idem store.Idem) (store.AttributeDefinition, bool, error) {
	f.gotDef, f.gotIdem = in, idem
	return in, !f.replay, f.err
}

func (f *fakeStore) ListAttributeDefinitions(_ context.Context, itemID string, limit int) ([]store.AttributeDefinition, error) {
	f.gotItem, f.gotLimit = itemID, limit
	return []store.AttributeDefinition{f.gotDef}, f.err
}

func (f *fakeStore) RegisterBatch(_ context.Context, in store.NewBatch, idem store.Idem) (store.Batch, bool, error) {
	f.gotNew, f.gotIdem = in, idem
	return f.batch, !f.replay, f.err
}

func (f *fakeStore) ListBatches(_ context.Context, filter store.BatchFilter) ([]store.Batch, error) {
	f.gotBatches = filter
	return []store.Batch{f.batch}, f.err
}

func (f *fakeStore) GetBatch(_ context.Context, _ string) (store.Batch, error) { return f.batch, f.err }

func (f *fakeStore) RecordAttributes(_ context.Context, _ string, values map[string]domain.Value, version int, idem store.Idem) (store.AttributesResult, bool, error) {
	f.gotValues, f.gotVersion, f.gotIdem = values, version, idem
	return store.AttributesResult{Batch: f.batch, Checks: []store.AttributeCheck{}}, !f.replay, f.err
}

func (f *fakeStore) ChangeDisposition(_ context.Context, _, to, reason string, version int, idem store.Idem) (store.Batch, bool, error) {
	f.gotTo, f.gotReason, f.gotVersion, f.gotIdem = to, reason, version, idem
	return f.batch, !f.replay, f.err
}

func (f *fakeStore) MergeBatches(context.Context, store.MergeInput, store.Idem) (store.MergeResult, bool, error) {
	return store.MergeResult{}, false, f.err
}

func (f *fakeStore) GetGenealogy(context.Context, string) (store.Genealogy, error) {
	return store.Genealogy{}, f.err
}

func (f *fakeStore) Ping(context.Context) error { return f.err }

func dec(s string) decimal.Decimal { return decimal.RequireFromString(s) }

// ac241Formula is the AC-24.1 formula: 1,000 kg with a 50 kg non-scalable line.
func ac241Formula() store.Formula {
	return store.Formula{ID: formulaID, Code: "DOUGH-1", Name: "Dough", ItemID: itemOut, Size: dec("1000"), Unit: "kg", Version: 1, Lines: []store.FormulaLine{
		{LineNumber: 1, ItemID: itemA, Type: domain.LineIngredient, Qty: dec("600"), Scalable: true},
		{LineNumber: 2, ItemID: itemB, Type: domain.LineIngredient, Qty: dec("350"), Scalable: true},
		{LineNumber: 3, ItemID: itemC, Type: domain.LineIngredient, Qty: dec("50"), Scalable: false},
	}}
}

func newRouter(s store.Store) http.Handler {
	r := httpx.NewRouter()
	New(s).Register(r)
	return r
}

type call struct {
	method, path, body, duties string
	headers                    map[string]string
	noTenant                   bool
}

func (c call) do(h http.Handler) *httptest.ResponseRecorder {
	req := httptest.NewRequest(c.method, c.path, strings.NewReader(c.body))
	req.Header.Set("Content-Type", "application/json")
	if !c.noTenant {
		req.Header.Set("X-Passage-Tenant", tenantID)
		req.Header.Set("X-Passage-Legal-Entity", entityID)
		req.Header.Set("X-Passage-User", "planner-1")
	}
	req.Header.Set("X-Passage-Duties", c.duties)
	for k, v := range c.headers {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func key(k string) map[string]string { return map[string]string{"Idempotency-Key": k} }

func problemOf(t *testing.T, rec *httptest.ResponseRecorder) httpx.Problem {
	t.Helper()
	var p httpx.Problem
	if err := json.Unmarshal(rec.Body.Bytes(), &p); err != nil {
		t.Fatalf("not a problem body: %s", rec.Body)
	}
	return p
}

const formulaBody = `{"code":"DOUGH-1","name":" Dough ","itemId":"` + itemOut + `","size":"1000","unit":"kg","lines":[
 {"lineNumber":1,"itemId":"` + itemA + `","type":"ingredient","qty":"600"},
 {"lineNumber":2,"itemId":"` + itemB + `","type":"ingredient","qty":"350"},
 {"lineNumber":3,"itemId":"` + itemC + `","type":"ingredient","qty":"50","scalable":false}]}`

func TestCreateFormula_StoresTrimmedFormulaWithDefaultScalable(t *testing.T) {
	fs := &fakeStore{}
	rec := call{method: "POST", path: "/v1/production/formulas", body: formulaBody, duties: DutyFormulaWrite, headers: key("f-1")}.do(newRouter(fs))
	if rec.Code != http.StatusCreated {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	got := fs.gotFormula
	if got.Name != "Dough" || len(got.Lines) != 3 || !got.Lines[0].Scalable || got.Lines[2].Scalable {
		t.Fatalf("stored %+v", got)
	}
	if fs.gotIdem.Key != "f-1" || len(fs.gotIdem.Fingerprint) != 64 {
		t.Fatalf("idempotency %+v", fs.gotIdem)
	}
}

func TestCreateFormula_Rejects(t *testing.T) {
	tests := []struct {
		name   string
		body   string
		hdr    map[string]string
		duties string
		status int
		code   string
		field  string
	}{
		{"no idempotency key", formulaBody, nil, DutyFormulaWrite, 400, "COM-0001", ""},
		{"read duty cannot write", formulaBody, key("k"), DutyRead, 403, "AUTH-0003", ""},
		{"lower-case code", strings.Replace(formulaBody, "DOUGH-1", "dough", 1), key("k"), DutyFormulaWrite, 400, "COM-0001", "code"},
		{"item is not a uuid", strings.Replace(formulaBody, itemA, "FLOUR", 1), key("k"), DutyFormulaWrite, 400, "COM-0001", "lines[0].itemId"},
		{"size has 7 decimals", strings.Replace(formulaBody, `"size":"1000"`, `"size":"0.0000001"`, 1), key("k"), DutyFormulaWrite, 400, "COM-0001", "size"},
		{"unknown field", strings.Replace(formulaBody, `"unit":"kg"`, `"unit":"kg","colour":"red"`, 1), key("k"), DutyFormulaWrite, 400, "COM-0001", ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			fs := &fakeStore{}
			rec := call{method: "POST", path: "/v1/production/formulas", body: tc.body, duties: tc.duties, headers: tc.hdr}.do(newRouter(fs))
			p := problemOf(t, rec)
			if rec.Code != tc.status || p.Code != tc.code {
				t.Fatalf("got %d %s, want %d %s", rec.Code, rec.Body, tc.status, tc.code)
			}
			if tc.field != "" && p.Errors[tc.field] == "" {
				t.Fatalf("no field error for %s: %v", tc.field, p.Errors)
			}
			if fs.gotFormula.Code != "" {
				t.Fatal("store called for a refused request")
			}
		})
	}
}

// AC-24.9: a percent-controlled formula summing to 99.5% is refused with PMF-0001.
func TestCreateFormula_DomainRuleIsUnprocessable(t *testing.T) {
	fs := &fakeStore{err: &domain.Error{Code: domain.CodePercentSum, Detail: "percent-controlled lines sum to 99.5000%"}}
	rec := call{method: "POST", path: "/v1/production/formulas", body: formulaBody, duties: DutyFormulaWrite, headers: key("k")}.do(newRouter(fs))
	if rec.Code != http.StatusUnprocessableEntity || problemOf(t, rec).Code != "PMF-0001" {
		t.Fatalf("got %d %s", rec.Code, rec.Body)
	}
}

func TestCreateFormula_ReplayAnswers200WithHeader(t *testing.T) {
	fs := &fakeStore{replay: true}
	rec := call{method: "POST", path: "/v1/production/formulas", body: formulaBody, duties: DutyFormulaWrite, headers: key("f-1")}.do(newRouter(fs))
	if rec.Code != http.StatusOK || rec.Header().Get("Idempotent-Replayed") != "true" {
		t.Fatalf("got %d replayed=%q", rec.Code, rec.Header().Get("Idempotent-Replayed"))
	}
}

// AC-24.1 through the preview endpoint: scalable lines ×2.5, the fixed line stays 50.
func TestPreviewScale_AC241(t *testing.T) {
	fs := &fakeStore{formula: ac241Formula()}
	rec := call{method: "POST", path: "/v1/production/formulas/" + formulaID + ":scale", body: `{"batchSize":"2500"}`, duties: DutyRead}.do(newRouter(fs))
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	var out scaleResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	want := []string{"1500", "875", "50"}
	for i, w := range want {
		if !out.Lines[i].Qty.Equal(dec(w)) {
			t.Fatalf("line %d = %s, want %s", i+1, out.Lines[i].Qty, w)
		}
	}
	if !out.ScaleFactor.Equal(dec("2.5")) {
		t.Fatalf("factor %s", out.ScaleFactor)
	}
}

func TestPreviewScale_RejectsBadSizeAndMissingFormula(t *testing.T) {
	fs := &fakeStore{formula: ac241Formula()}
	rec := call{method: "POST", path: "/v1/production/formulas/" + formulaID + ":scale", body: `{"batchSize":"0"}`, duties: DutyRead}.do(newRouter(fs))
	if rec.Code != http.StatusBadRequest || problemOf(t, rec).Errors["batchSize"] == "" {
		t.Fatalf("zero batch: %d %s", rec.Code, rec.Body)
	}
	fs.err = httpx.NewProblem(http.StatusNotFound, store.CodeFormulaNotFound, "formula not found")
	rec = call{method: "POST", path: "/v1/production/formulas/" + formulaID + ":scale", body: `{"batchSize":"10"}`, duties: DutyRead}.do(newRouter(fs))
	if rec.Code != http.StatusNotFound || problemOf(t, rec).Code != store.CodeFormulaNotFound {
		t.Fatalf("missing formula: %d %s", rec.Code, rec.Body)
	}
	rec = call{method: "POST", path: "/v1/production/formulas/not-a-uuid:scale", body: `{"batchSize":"10"}`, duties: DutyRead}.do(newRouter(fs))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("bad id: %d", rec.Code)
	}
}

func TestCreateBatchOrder_DefaultsLegalEntityToCaller(t *testing.T) {
	fs := &fakeStore{order: store.BatchOrder{ID: orderID, OrderNumber: "BO-000001"}}
	body := `{"formulaId":"` + formulaID + `","batchSize":"2500"}`
	rec := call{method: "POST", path: "/v1/production/batch-orders", body: body, duties: DutyBatchOrderWrite, headers: key("o-1")}.do(newRouter(fs))
	if rec.Code != http.StatusCreated || !strings.Contains(rec.Body.String(), "BO-000001") {
		t.Fatalf("got %d %s", rec.Code, rec.Body)
	}
	if fs.gotOrder.LegalEntityID != entityID || !fs.gotOrder.BatchSize.Equal(dec("2500")) || fs.gotOrder.FormulaID != formulaID {
		t.Fatalf("store got %+v", fs.gotOrder)
	}
}

func TestCreateBatchOrder_RefusesOtherLegalEntityAndBadInput(t *testing.T) {
	fs := &fakeStore{}
	body := `{"formulaId":"` + formulaID + `","batchSize":"2500","legalEntityId":"` + otherEntity + `"}`
	rec := call{method: "POST", path: "/v1/production/batch-orders", body: body, duties: DutyBatchOrderWrite, headers: key("o-1")}.do(newRouter(fs))
	if rec.Code != http.StatusForbidden || problemOf(t, rec).Code != "AUTH-0003" {
		t.Fatalf("other entity: %d %s", rec.Code, rec.Body)
	}
	rec = call{method: "POST", path: "/v1/production/batch-orders", body: `{"formulaId":"x","batchSize":"-1"}`, duties: DutyBatchOrderWrite, headers: key("o-2")}.do(newRouter(fs))
	p := problemOf(t, rec)
	if rec.Code != http.StatusBadRequest || p.Errors["formulaId"] == "" || p.Errors["batchSize"] == "" {
		t.Fatalf("bad input: %d %s", rec.Code, rec.Body)
	}
	if fs.gotOrder.FormulaID != "" {
		t.Fatal("store called for a refused request")
	}
}

func TestListBatchOrders_FiltersAndLimits(t *testing.T) {
	fs := &fakeStore{}
	rec := call{method: "GET", path: "/v1/production/batch-orders?status=planned&formulaId=" + formulaID + "&limit=20", duties: DutyRead}.do(newRouter(fs))
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"items":[`) {
		t.Fatalf("got %d %s", rec.Code, rec.Body)
	}
	if fs.gotFilter != (store.OrderFilter{Status: "planned", FormulaID: formulaID, Limit: 20}) {
		t.Fatalf("filter %+v", fs.gotFilter)
	}
	for _, q := range []string{"status=open", "limit=0", "limit=201", "formulaId=x"} {
		rec := call{method: "GET", path: "/v1/production/batch-orders?" + q, duties: DutyRead}.do(newRouter(fs))
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("%s: got %d", q, rec.Code)
		}
	}
}

func TestRescale_PassesVersionAndMapsConflicts(t *testing.T) {
	fs := &fakeStore{order: store.BatchOrder{ID: orderID, Version: 2}}
	path := "/v1/production/batch-orders/" + orderID + ":rescale"
	rec := call{method: "POST", path: path, body: `{"batchSize":"3000","version":1}`, duties: DutyBatchOrderWrite, headers: key("r-1")}.do(newRouter(fs))
	if rec.Code != http.StatusOK || fs.gotVersion != 1 || !fs.gotSize.Equal(dec("3000")) {
		t.Fatalf("got %d version=%d size=%s", rec.Code, fs.gotVersion, fs.gotSize)
	}
	fs.err = httpx.NewProblem(http.StatusConflict, store.CodeVersionConflict, "stale")
	rec = call{method: "POST", path: path, body: `{"batchSize":"3000","version":1}`, duties: DutyBatchOrderWrite, headers: key("r-2")}.do(newRouter(fs))
	if rec.Code != http.StatusConflict || problemOf(t, rec).Code != store.CodeVersionConflict {
		t.Fatalf("stale: %d %s", rec.Code, rec.Body)
	}
	rec = call{method: "POST", path: path, body: `{"batchSize":"3000"}`, duties: DutyBatchOrderWrite, headers: key("r-3")}.do(newRouter(fs))
	if rec.Code != http.StatusBadRequest || problemOf(t, rec).Errors["version"] == "" {
		t.Fatalf("no version: %d %s", rec.Code, rec.Body)
	}
}

func TestReadyReflectsDatabase(t *testing.T) {
	fs := &fakeStore{}
	if rec := (call{method: "GET", path: "/readyz", noTenant: true}).do(newRouter(fs)); rec.Code != http.StatusOK {
		t.Fatalf("ready: %d", rec.Code)
	}
	fs.err = errors.New("down")
	if rec := (call{method: "GET", path: "/readyz", noTenant: true}).do(newRouter(fs)); rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("down: %d", rec.Code)
	}
}

func TestUnexpectedStoreErrorIsHidden(t *testing.T) {
	fs := &fakeStore{err: errors.New("pq: connection reset with secret detail")}
	rec := call{method: "GET", path: "/v1/production/formulas", duties: DutyRead}.do(newRouter(fs))
	if rec.Code != http.StatusInternalServerError || strings.Contains(rec.Body.String(), "secret") {
		t.Fatalf("got %d %s", rec.Code, rec.Body)
	}
}

func TestMissingTenantIsUnauthorized(t *testing.T) {
	rec := call{method: "GET", path: "/v1/production/formulas", duties: DutyRead, noTenant: true}.do(newRouter(&fakeStore{}))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("got %d", rec.Code)
	}
}

func TestPotencyConsume(t *testing.T) {
	h := newRouter(&fakeStore{})
	ok := call{method: "POST", path: "/v1/production/potency:consume", duties: DutyRead,
		body: `{"planned":"100","standardPotency":"50","batchPotency":"40","compensatingPlanned":"100"}`}.do(h)
	if ok.Code != http.StatusOK || !strings.Contains(ok.Body.String(), `"qty":"125"`) || !strings.Contains(ok.Body.String(), `"compensatingQty":"75"`) {
		t.Fatalf("got %d %s", ok.Code, ok.Body)
	}
	neg := call{method: "POST", path: "/v1/production/potency:consume", duties: DutyRead,
		body: `{"planned":"100","standardPotency":"50","batchPotency":"20","compensatingPlanned":"100"}`}.do(h)
	if neg.Code != http.StatusUnprocessableEntity || problemOf(t, neg).Code != "PMF-0002" {
		t.Fatalf("got %d %s", neg.Code, neg.Body)
	}
}

func TestCatchWeightValidate(t *testing.T) {
	h := newRouter(&fakeStore{})
	spec := `"nominal":"10","min":"9.8","max":"11","cwQty":10`
	short := call{method: "POST", path: "/v1/production/catch-weight:validate", duties: DutyRead, body: "{" + spec + `,"weight":"95"}`}.do(h)
	if short.Code != http.StatusUnprocessableEntity || problemOf(t, short).Code != "PMF-0004" {
		t.Fatalf("got %d %s", short.Code, short.Body)
	}
	ok := call{method: "POST", path: "/v1/production/catch-weight:validate", duties: DutyRead, body: "{" + spec + `,"weight":"102"}`}.do(h)
	if ok.Code != http.StatusOK || !strings.Contains(ok.Body.String(), `"valid":true`) {
		t.Fatalf("got %d %s", ok.Code, ok.Body)
	}
}

// AC-24.3 / BR-24.3 through HTTP, under the "Post production journals" duty.
func TestCoProductAllocate(t *testing.T) {
	h := newRouter(&fakeStore{})
	body := `{"batchCost":"10000","currency":"USD","method":"tca",
	 "main":{"itemId":"A","standardCost":"40","rafQty":"200"},
	 "coProducts":[{"itemId":"B","standardCost":"20","rafQty":"100"}],
	 "byProducts":[{"itemId":"C","costType":"per-quantity","qty":"50","rate":"1.5"}]}`
	rec := call{method: "POST", path: "/v1/production/co-products:allocate", body: body, duties: DutyCoProductAllocate}.do(h)
	var out allocateResponse
	if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &out) != nil {
		t.Fatalf("got %d %s", rec.Code, rec.Body)
	}
	if !out.Main.Amount.Equal(dec("7940")) || !out.CoProducts[0].Amount.Equal(dec("1985")) || !out.Burdens[0].Amount.Equal(dec("75")) ||
		!out.Total.Equal(dec("10000")) || out.Currency != "USD" {
		t.Fatalf("allocation %+v", out)
	}
	if denied := (call{method: "POST", path: "/v1/production/co-products:allocate", body: body, duties: DutyRead}).do(h); denied.Code != http.StatusForbidden {
		t.Fatalf("read duty allocated: %d", denied.Code)
	}
	noCurrency := strings.Replace(body, `"currency":"USD",`, "", 1)
	if rec := (call{method: "POST", path: "/v1/production/co-products:allocate", body: noCurrency, duties: DutyCoProductAllocate}).do(h); rec.Code != http.StatusBadRequest {
		t.Fatalf("no currency: %d", rec.Code)
	}
}

func TestCreateBatchOrder_RefusesACallerWithNoLegalEntityScope(t *testing.T) {
	fs := &fakeStore{}
	body := `{"formulaId":"` + formulaID + `","batchSize":"2500","legalEntityId":"` + entityID + `"}`
	headers := key("o-1")
	headers["X-Passage-Legal-Entity"] = ""
	rec := call{method: "POST", path: "/v1/production/batch-orders", body: body, duties: DutyBatchOrderWrite, headers: headers}.do(newRouter(fs))
	if rec.Code != http.StatusForbidden || problemOf(t, rec).Code != "AUTH-0003" || fs.gotOrder.FormulaID != "" {
		t.Fatalf("no scope: %d %s", rec.Code, rec.Body)
	}
}

func (f *fakeStore) PickCandidates(_ context.Context, itemID string, onHand []store.OnHand) ([]store.PickCandidate, string, error) {
	f.gotItem = itemID
	f.gotOnHand = onHand
	want := map[string]bool{}
	for _, oh := range onHand {
		want[oh.BatchID] = true
	}
	var out []store.PickCandidate
	for _, c := range f.cands {
		if want[c.Batch.ID] {
			out = append(out, c)
		}
	}
	return out, f.potency, f.err
}

func (f *fakeStore) RegisteredBatches(_ context.Context, batchIDs []string) ([]string, error) {
	f.gotIDs = batchIDs
	return f.registered, f.err
}
