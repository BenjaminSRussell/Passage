package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/costing/internal/domain"
	"github.com/BenjaminSRussell/passage/services/costing/internal/store"
)

// slice6 fakes the adjustment and reconciliation stores. Planning against the
// stored value is the store's (tested against Postgres in
// store/adjustment_test.go); the fake records the request the API built and
// keeps the Idempotency-Key rules of the tables.
type slice6 struct {
	previewed   []store.AdjustmentRequest
	posted      map[string]store.Adjustment // idempotency key -> adjustment
	postHashes  map[string]string
	postErr     error
	adjFilter   store.AdjustmentFilter
	runs        map[string]store.ReconciliationRun
	runHashes   map[string]string
	runListedLE string
}

func (f *slice6) PreviewAdjustment(_ context.Context, in store.AdjustmentRequest) (store.Adjustment, error) {
	f.previewed = append(f.previewed, in)
	return store.Adjustment{LegalEntityID: in.LegalEntityID, Kind: in.Kind, Mode: in.Mode, ProductID: in.ProductID,
		SiteID: in.SiteID, Currency: "EUR", FinancialDate: in.FinancialDate, Qty: "50", OldValue: "600.00", NewValue: "550.00",
		Delta: "-50.00", OldUnitCost: "12", NewUnitCost: "11"}, nil
}

func (f *slice6) CreateAdjustment(_ context.Context, in store.AdjustmentRequest, key, hash string) (store.Adjustment, bool, error) {
	if f.postErr != nil {
		return store.Adjustment{}, false, f.postErr
	}
	if f.posted == nil {
		f.posted, f.postHashes = map[string]store.Adjustment{}, map[string]string{}
	}
	if a, ok := f.posted[key]; ok {
		if f.postHashes[key] != hash {
			return store.Adjustment{}, false, httpx.NewProblem(http.StatusUnprocessableEntity, store.CodeIdempotencyReuse, "reused")
		}
		return a, false, nil
	}
	now := time.Now().UTC()
	a := store.Adjustment{ID: ids.New(), LegalEntityID: in.LegalEntityID, Kind: in.Kind, Mode: in.Mode, ProductID: in.ProductID,
		SiteID: in.SiteID, ValuationLedgerID: in.ValuationLedgerID, Reason: in.Reason, Currency: "EUR", FinancialDate: in.FinancialDate,
		Qty: "50", OldValue: "600.00", NewValue: "550.00", Delta: "-50.00", CostTransactionID: ids.New(), CreatedAt: &now}
	f.posted[key], f.postHashes[key] = a, hash
	return a, true, nil
}

func (f *slice6) GetAdjustment(_ context.Context, id string) (store.Adjustment, error) {
	for _, a := range f.posted {
		if a.ID == id {
			return a, nil
		}
	}
	return store.Adjustment{}, httpx.NewProblem(http.StatusNotFound, store.CodeAdjustmentNotFound, "not found")
}

func (f *slice6) ListAdjustments(_ context.Context, flt store.AdjustmentFilter, _ string, _ int) (store.AdjustmentPage, error) {
	f.adjFilter = flt
	page := store.AdjustmentPage{Items: []store.Adjustment{}}
	for _, a := range f.posted {
		page.Items = append(page.Items, a)
	}
	return page, nil
}

func (f *slice6) CreateReconciliationRun(_ context.Context, in store.ReconciliationRun, key, hash string) (store.ReconciliationRun, bool, error) {
	if f.runs == nil {
		f.runs, f.runHashes = map[string]store.ReconciliationRun{}, map[string]string{}
	}
	if run, ok := f.runs[key]; ok {
		if f.runHashes[key] != hash {
			return store.ReconciliationRun{}, false, httpx.NewProblem(http.StatusUnprocessableEntity, store.CodeIdempotencyReuse, "reused")
		}
		return run, false, nil
	}
	in.ID, in.RowCount = ids.New(), len(in.Rows)
	f.runs[key], f.runHashes[key] = in, hash
	return in, true, nil
}

func (f *slice6) GetReconciliationRun(_ context.Context, id string) (store.ReconciliationRun, error) {
	for _, run := range f.runs {
		if run.ID == id {
			return run, nil
		}
	}
	return store.ReconciliationRun{}, httpx.NewProblem(http.StatusNotFound, store.CodeReconciliationNotFound, "not found")
}

func (f *slice6) ListReconciliationRuns(_ context.Context, le, _ string, _ int) (store.ReconciliationPage, error) {
	f.runListedLE = le
	return store.ReconciliationPage{Items: []store.ReconciliationRun{}}, nil
}

const adjustDuties = allDuties + "," + dutyCostAdjust + "," + dutyReconcile

func onHandBody(extra string) string {
	return `{"legalEntityId":"` + repLE + `","kind":"ON_HAND","mode":"UNIT_COST","productId":"` + repP1 + `","siteId":"` + repS1 +
		`","financialDate":"2026-03-31","unitCost":"11.00","reason":"NRV write-down"` + extra + `}`
}

func TestAdjustments_PostRequiresKeyDutyAndScope(t *testing.T) {
	f := &fakeStore{versions: map[string]store.CostingVersion{}}
	a := New(f)
	h := httpx.NewRouter()
	a.Register(h)
	key := map[string]string{idempotencyKey: "adj-1"}
	if rec := call(h, http.MethodPost, "/v1/costing/adjustments", onHandBody(""), allDuties, key); rec.Code != http.StatusForbidden {
		t.Errorf("without %s: status %d, want 403", dutyCostAdjust, rec.Code)
	}
	rec := call(h, http.MethodPost, "/v1/costing/adjustments", onHandBody(""), adjustDuties, nil)
	if m, _ := decodeMap(rec); rec.Code != http.StatusBadRequest || m["errors"].(map[string]any)[idempotencyKey] == nil {
		t.Errorf("without a key: status %d body %v, want 400 naming %s", rec.Code, m, idempotencyKey)
	}
	scoped := map[string]string{idempotencyKey: "adj-1", "X-Passage-Legal-Entity": ids.New()}
	if rec := call(h, http.MethodPost, "/v1/costing/adjustments", onHandBody(""), adjustDuties, scoped); rec.Code != http.StatusForbidden {
		t.Errorf("another legal entity: status %d, want 403", rec.Code)
	}
	if len(f.posted) != 0 {
		t.Fatalf("refused posts reached the store: %v", f.posted)
	}

	rec = call(h, http.MethodPost, "/v1/costing/adjustments", onHandBody(""), adjustDuties, key)
	m, _ := decodeMap(rec)
	if rec.Code != http.StatusCreated || m["id"] == nil || m["delta"] != "-50.00" {
		t.Fatalf("post status %d body %v", rec.Code, m)
	}
	got := f.posted["adj-1"]
	if got.Reason != "NRV write-down" || got.ValuationLedgerID != "" {
		t.Errorf("store request = %+v, want the reason and the primary valuation", got)
	}
	// The same key and body replays with 200; the same key with another body is refused.
	if rec := call(h, http.MethodPost, "/v1/costing/adjustments", onHandBody(""), adjustDuties, key); rec.Code != http.StatusOK {
		t.Errorf("replay status %d, want 200", rec.Code)
	}
	if rec := call(h, http.MethodPost, "/v1/costing/adjustments", strings.Replace(onHandBody(""), "11.00", "10.00", 1), adjustDuties, key); rec.Code != http.StatusUnprocessableEntity {
		t.Errorf("reused key status %d, want 422", rec.Code)
	}
	if rec := call(h, http.MethodGet, "/v1/costing/adjustments/"+got.ID, "", allDuties, nil); rec.Code != http.StatusOK {
		t.Errorf("get status %d, want 200", rec.Code)
	}
	if rec := call(h, http.MethodGet, "/v1/costing/adjustments/not-an-id", "", allDuties, nil); rec.Code != http.StatusNotFound {
		t.Errorf("get malformed id status %d, want 404", rec.Code)
	}
	metrics := httptest.NewRecorder()
	a.Metrics().ServeHTTP(metrics, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if !strings.Contains(metrics.Body.String(), "\ncosting_adjustments_posted_total 1\n") {
		t.Errorf("metrics do not count exactly the one post (replays are not counted):\n%s", metrics.Body.String())
	}
}

func TestAdjustments_Validation(t *testing.T) {
	h, _ := server()
	tomorrow := time.Now().UTC().AddDate(0, 0, 2).Format(time.DateOnly)
	receipt := func(extra string) string {
		return `{"legalEntityId":"` + repLE + `","kind":"RECEIPT","mode":"AMOUNT","receiptTransactionId":"` + ids.New() +
			`","financialDate":"2026-03-31","amount":"12.50","reason":"freight"` + extra + `}`
	}
	tests := []struct{ name, body, field string }{
		{"unknown kind", strings.Replace(onHandBody(""), "ON_HAND", "STOCK", 1), "kind"},
		{"unknown mode", strings.Replace(onHandBody(""), "UNIT_COST", "PERCENT", 1), "mode"},
		{"on-hand needs a product", strings.Replace(onHandBody(""), repP1, "p1", 1), "productId"},
		{"unit cost must be positive", strings.Replace(onHandBody(""), "11.00", "0", 1), "unitCost"},
		{"unit cost finer than 6 places", strings.Replace(onHandBody(""), "11.00", "1.0000001", 1), "unitCost"},
		{"unit cost mode takes no amount", onHandBody(`,"amount":"1"`), "amount"},
		{"amount mode needs an amount", strings.Replace(onHandBody(""), `"mode":"UNIT_COST"`, `"mode":"AMOUNT"`, 1), "amount"},
		{"amount finer than numeric(19,4)", strings.Replace(receipt(""), "12.50", "0.00001", 1), "amount"},
		{"reason is required to post", strings.Replace(onHandBody(""), "NRV write-down", "  ", 1), "reason"},
		{"no future financial date", strings.Replace(onHandBody(""), "2026-03-31", tomorrow, 1), "financialDate"},
		{"warehouse is a UUID", onHandBody(`,"warehouseId":"w1"`), "warehouseId"},
		{"on-hand names no receipt", onHandBody(`,"receiptTransactionId":"` + ids.New() + `"`), "receiptTransactionId"},
		{"a receipt takes its product from the receipt", receipt(`,"productId":"` + repP1 + `"`), "productId"},
		{"a receipt is adjusted by amount", strings.Replace(strings.Replace(receipt(""), `"mode":"AMOUNT"`, `"mode":"UNIT_COST"`, 1), `"amount":"12.50"`, `"unitCost":"1"`, 1), "mode"},
		{"a receipt needs its id", strings.Replace(receipt(""), `"receiptTransactionId":"`, `"receiptTransactionId":"x`, 1), "receiptTransactionId"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rec := call(h, http.MethodPost, "/v1/costing/adjustments", tc.body, adjustDuties, map[string]string{idempotencyKey: "k"})
			m, _ := decodeMap(rec)
			errs, _ := m["errors"].(map[string]any)
			if _, ok := errs[tc.field]; rec.Code != http.StatusBadRequest || !ok {
				t.Fatalf("status %d errors %v, want 400 naming %s", rec.Code, errs, tc.field)
			}
		})
	}
	if rec := call(h, http.MethodPost, "/v1/costing/adjustments", receipt(""), adjustDuties, map[string]string{idempotencyKey: "r"}); rec.Code != http.StatusCreated {
		t.Errorf("valid receipt adjustment status %d, want 201", rec.Code)
	}
}

func TestAdjustments_PreviewNeedsNoReasonAndResolvesTheLedger(t *testing.T) {
	h, f := server()
	f.ledgers = []store.ValuationLedger{
		{ID: ifrsID, LegalEntityID: repLE, Code: "IFRS", Currency: "EUR", Primary: true},
		{ID: gaapID, LegalEntityID: repLE, Code: "USGAAP", Currency: "USD"},
	}
	body := strings.Replace(onHandBody(`,"valuationLedgerId":"`+ifrsID+`"`), `"reason":"NRV write-down"`, `"reason":""`, 1)
	rec := call(h, http.MethodPost, "/v1/costing/adjustments:preview", body, allDuties, nil)
	if m, _ := decodeMap(rec); rec.Code != http.StatusOK || m["newValue"] != "550.00" || m["id"] != nil {
		t.Fatalf("preview status %d body %v", rec.Code, m)
	}
	if got := f.previewed[0].ValuationLedgerID; got != "" {
		t.Errorf("the primary ledger's id is stored as %q, want the primary valuation", got)
	}
	call(h, http.MethodPost, "/v1/costing/adjustments:preview", onHandBody(`,"valuationLedgerId":"`+gaapID+`"`), allDuties, nil)
	if got := f.previewed[1].ValuationLedgerID; got != gaapID {
		t.Errorf("group ledger stored as %q, want %s", got, gaapID)
	}
	if rec := call(h, http.MethodPost, "/v1/costing/adjustments:preview", onHandBody(`,"valuationLedgerId":"`+ids.New()+`"`), allDuties, nil); rec.Code != http.StatusNotFound {
		t.Errorf("unknown ledger status %d, want 404", rec.Code)
	}
}

func TestAdjustments_DomainAndStaleRefusals(t *testing.T) {
	h, f := server()
	f.postErr = &domain.Error{Code: domain.CodeAdjustUnitCost, Detail: "no stock"}
	rec := call(h, http.MethodPost, "/v1/costing/adjustments", onHandBody(""), adjustDuties, map[string]string{idempotencyKey: "a"})
	if m, _ := decodeMap(rec); rec.Code != http.StatusUnprocessableEntity || m["code"] != domain.CodeAdjustUnitCost {
		t.Errorf("domain refusal status %d body %v, want 422 %s", rec.Code, m, domain.CodeAdjustUnitCost)
	}
	f.postErr = httpx.NewProblem(http.StatusConflict, store.CodeAdjustmentStale, "changed")
	rec = call(h, http.MethodPost, "/v1/costing/adjustments", onHandBody(`,"expectedValue":"600.00"`), adjustDuties, map[string]string{idempotencyKey: "b"})
	if m, _ := decodeMap(rec); rec.Code != http.StatusConflict || m["code"] != store.CodeAdjustmentStale {
		t.Errorf("stale preview status %d body %v, want 409 %s", rec.Code, m, store.CodeAdjustmentStale)
	}
}

func TestAdjustments_ListFilters(t *testing.T) {
	h, f := server()
	rec := call(h, http.MethodGet, "/v1/costing/adjustments?legalEntityId="+strings.ToUpper(repLE)+"&kind=RECEIPT&productId="+repP1, "", allDuties, nil)
	if rec.Code != http.StatusOK || f.adjFilter.LegalEntityID != repLE || f.adjFilter.Kind != "RECEIPT" || f.adjFilter.ProductID != repP1 {
		t.Fatalf("list status %d filter %+v", rec.Code, f.adjFilter)
	}
	if rec := call(h, http.MethodGet, "/v1/costing/adjustments?kind=STOCK", "", allDuties, nil); rec.Code != http.StatusBadRequest {
		t.Errorf("unknown kind status %d, want 400", rec.Code)
	}
	scoped := map[string]string{"X-Passage-Legal-Entity": ids.New()}
	if rec := call(h, http.MethodGet, "/v1/costing/adjustments?legalEntityId="+repLE, "", allDuties, scoped); rec.Code != http.StatusForbidden {
		t.Errorf("another legal entity status %d, want 403", rec.Code)
	}
}

// AC-20.3 style: the stored run carries the tie-out the report computed, a
// differing group counts once, and a replayed key does not run again.
func TestReconciliations_StoreTheTieOut(t *testing.T) {
	h, f := server()
	reportFixture(f)
	body := `{"legalEntityId":"` + repLE + `","asOf":"2026-03-31","groupBy":["site"],"glBalances":[` +
		`{"dimensions":{"site":"` + repS1 + `"},"balance":"1700.00"},{"dimensions":{"site":"` + repS2 + `"},"balance":"45.00"}]}`
	key := map[string]string{idempotencyKey: "rec-2026-03-31"}
	if rec := call(h, http.MethodPost, "/v1/costing/reconciliations", body, allDuties, key); rec.Code != http.StatusForbidden {
		t.Errorf("without %s: status %d, want 403", dutyReconcile, rec.Code)
	}
	if rec := call(h, http.MethodPost, "/v1/costing/reconciliations", body, adjustDuties, nil); rec.Code != http.StatusBadRequest {
		t.Errorf("without a key: status %d, want 400", rec.Code)
	}
	rec := call(h, http.MethodPost, "/v1/costing/reconciliations", body, adjustDuties, key)
	m, _ := decodeMap(rec)
	if rec.Code != http.StatusCreated || m["ties"] != false || m["differences"] != float64(1) || m["totalDiff"] != "5.00" {
		t.Fatalf("run status %d body %v", rec.Code, m)
	}
	run := f.runs["rec-2026-03-31"]
	if len(run.Rows) != 2 || run.Rows[1].Diff != "5.00" || run.Rows[0].Diff != "0.00" || run.Currency != "EUR" {
		t.Errorf("stored rows = %+v (currency %q)", run.Rows, run.Currency)
	}
	if rec := call(h, http.MethodPost, "/v1/costing/reconciliations", body, adjustDuties, key); rec.Code != http.StatusOK {
		t.Errorf("replay status %d, want 200", rec.Code)
	}
	changed := strings.Replace(body, "45.00", "50.00", 1)
	if rec := call(h, http.MethodPost, "/v1/costing/reconciliations", changed, adjustDuties, key); rec.Code != http.StatusUnprocessableEntity {
		t.Errorf("reused key status %d, want 422", rec.Code)
	}
	if rec := call(h, http.MethodGet, "/v1/costing/reconciliations/"+run.ID, "", allDuties, nil); rec.Code != http.StatusOK {
		t.Errorf("get status %d, want 200", rec.Code)
	}
	if rec := call(h, http.MethodGet, "/v1/costing/reconciliations/"+ids.New(), "", allDuties, nil); rec.Code != http.StatusNotFound {
		t.Errorf("get unknown status %d, want 404", rec.Code)
	}
	if rec := call(h, http.MethodGet, "/v1/costing/reconciliations?legalEntityId="+repLE, "", allDuties, nil); rec.Code != http.StatusOK || f.runListedLE != repLE {
		t.Errorf("list status %d legal entity %q", rec.Code, f.runListedLE)
	}
	// A balance naming the wrong dimensions is the tie-out's refusal, not a stored run.
	bad := strings.Replace(body, `"dimensions":{"site"`, `"dimensions":{"product"`, 1)
	rec = call(h, http.MethodPost, "/v1/costing/reconciliations", bad, adjustDuties, map[string]string{idempotencyKey: "rec-bad"})
	if m, _ := decodeMap(rec); rec.Code != http.StatusUnprocessableEntity || m["code"] != domain.CodeTieOutDimension {
		t.Errorf("bad dimensions status %d body %v", rec.Code, m)
	}
	if len(f.runs) != 1 {
		t.Errorf("store holds %d runs, want 1", len(f.runs))
	}
}
