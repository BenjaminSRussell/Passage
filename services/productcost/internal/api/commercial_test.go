package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/BenjaminSRussell/passage/platform/httpx"
)

const mixedItems = `[
 {"id":"BUY","kind":"Purchased","price":"10"},
 {"id":"RM","kind":"Purchased","price":"12.5"},
 {"id":"PROD","kind":"Manufactured","costingLot":"1","bom":[{"childId":"RM","qty":"1","scrapPct":"0"}]}
]`

func expectAll(t *testing.T, body string, wants ...string) {
	t.Helper()
	for _, w := range wants {
		if !strings.Contains(body, w) {
			t.Errorf("body missing %s: %s", w, body)
		}
	}
}

func TestMixedCost_WeightedOverHTTP(t *testing.T) {
	r := newRouter(t, nil)
	body := `{"itemId":"FG","items":` + mixedItems + `,"alternatives":[
	 {"source":"VENDOR-A","percent":"60","itemId":"BUY"},
	 {"source":"IN-HOUSE","percent":"40","itemId":"PROD"}]}`
	rec := do(r, http.MethodPost, "/v1/product-costing/mixed-costs", body, allDuties, true)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	expectAll(t, rec.Body.String(), `"unitCost":"11"`, `"DM":"11"`, `"status":"Pending"`)

	bad := `{"itemId":"FG","items":` + mixedItems + `,"alternatives":[
	 {"source":"VENDOR-A","percent":"60","itemId":"BUY"},
	 {"source":"IN-HOUSE","percent":"39.99","itemId":"PROD"}]}`
	rec = do(r, http.MethodPost, "/v1/product-costing/mixed-costs", bad, allDuties, true)
	if rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), "CST-0741") {
		t.Errorf("ratio total: %d %s", rec.Code, rec.Body.String())
	}
	unknown := `{"itemId":"FG","items":` + mixedItems + `,"alternatives":[{"source":"X","percent":"100","itemId":"NOPE"}]}`
	if rec = do(r, http.MethodPost, "/v1/product-costing/mixed-costs", unknown, allDuties, true); rec.Code != http.StatusBadRequest {
		t.Errorf("unknown alternative item: %d", rec.Code)
	}
}

func TestPostings_COGSAndPriceDifferenceOverHTTP(t *testing.T) {
	r := newRouter(t, nil)
	cogs := `{"quantity":"10","unitCost":{"DM":"21.20","DL":"54.00","IND-MAT":"2.12","IND-LAB":"27.00"}}`
	rec := do(r, http.MethodPost, "/v1/product-costing/postings:cogs", cogs, allDuties, true)
	if rec.Code != http.StatusOK {
		t.Fatalf("cogs status %d: %s", rec.Code, rec.Body.String())
	}
	expectAll(t, rec.Body.String(), `"total":"1043.2"`, `"account":"COGS-DM"`, `"account":"COGS-IND-LAB"`, `"account":"Inventory"`)

	ppv := `{"quantity":"10","receiptUnitCost":"5.20","standard":{"DM":"5.00"}}`
	rec = do(r, http.MethodPost, "/v1/product-costing/postings:price-difference", ppv, allDuties, true)
	if rec.Code != http.StatusOK {
		t.Fatalf("ppv status %d: %s", rec.Code, rec.Body.String())
	}
	expectAll(t, rec.Body.String(), `"total":"2"`, `"account":"PPV-DM"`, `"account":"PriceDifferenceClearing"`)

	if rec = do(r, http.MethodPost, "/v1/product-costing/postings:cogs", `{"quantity":"0","unitCost":{}}`, allDuties, true); rec.Code != http.StatusBadRequest {
		t.Errorf("invalid cogs: %d", rec.Code)
	}
}

func TestSalesOrderEstimate_MarginOverHTTP(t *testing.T) {
	r := newRouter(t, nil)
	body := `{"itemId":"CFG","quantity":"100","netAmount":"2400","items":[
	 {"id":"X","kind":"Purchased","price":"18.60"},
	 {"id":"CFG","kind":"Manufactured","costingLot":"1","bom":[{"childId":"X","qty":"1","scrapPct":"0"}]}]}`
	rec := do(r, http.MethodPost, "/v1/product-costing/sales-order-estimates", body, allDuties, true)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	expectAll(t, rec.Body.String(), `"calculationType":"SalesOrder"`, `"cost":"1860"`, `"margin":"540"`, `"marginPct":"22.5"`)

	zero := `{"itemId":"CFG","quantity":"100","netAmount":"0","items":[{"id":"CFG","kind":"Purchased","price":"1"}]}`
	if rec = do(r, http.MethodPost, "/v1/product-costing/sales-order-estimates", zero, allDuties, true); rec.Code != http.StatusBadRequest {
		t.Errorf("zero net: %d", rec.Code)
	}
}

func TestCompare_BaselineAndCandidateOverHTTP(t *testing.T) {
	r := newRouter(t, nil)
	body := `{"baseline":{"items":[{"id":"A","kind":"Purchased","price":"10"}]},
	 "candidate":{"items":[{"id":"A","kind":"Purchased","price":"11"},{"id":"C","kind":"Purchased","price":"3"}]}}`
	rec := do(r, http.MethodPost, "/v1/product-costing/comparisons", body, allDuties, true)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	expectAll(t, rec.Body.String(), `"totalDelta":"1"`, `"status":"Compared"`, `"status":"CandidateOnly"`, `"deltaPct":"10"`)

	bad := `{"baseline":{"items":[]},"candidate":{"items":[{"id":"A","kind":"Bogus"}]}}`
	rec = do(r, http.MethodPost, "/v1/product-costing/comparisons", bad, allDuties, true)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "baseline.items") || !strings.Contains(rec.Body.String(), "candidate.items[0].kind") {
		t.Errorf("side validation: %d %s", rec.Code, rec.Body.String())
	}
}

func TestReconcile_OverHTTPAndMetric(t *testing.T) {
	a := New(nil, Config{})
	r := httpx.NewRouter()
	a.Register(r)
	body := `{"onHand":[{"siteId":"S1","itemId":"A","qty":"10","stdUnit":"10.50"},
	 {"siteId":"S2","itemId":"A","qty":"4","stdUnit":"2.50"}],
	 "glBalances":[{"siteId":"S1","amount":"100"},{"siteId":"S2","amount":"10"}]}`
	rec := do(r, http.MethodPost, "/v1/product-costing/reconciliations", body, allDuties, true)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	expectAll(t, rec.Body.String(), `"differences":1`, `"difference":"5"`, `"status":"Reconciled"`)

	if rec = do(r, http.MethodPost, "/v1/product-costing/reconciliations", `{"onHand":[{"siteId":"S1","itemId":"A","stdUnit":"-1"}]}`, allDuties, true); rec.Code != http.StatusBadRequest {
		t.Errorf("negative standard: %d", rec.Code)
	}
	m := httptest.NewRecorder()
	a.Metrics().ServeHTTP(m, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if !strings.Contains(m.Body.String(), "productcost_reconciliation_differences_total 1") {
		t.Errorf("metrics = %s", m.Body.String())
	}
}

// TestCommercialRoutes_EnforceTheirDuty: the calculations need CostingCalculate
// and the inquiries (comparison, reconciliation) need CostingView (PRD 7.39.17).
func TestCommercialRoutes_EnforceTheirDuty(t *testing.T) {
	r := newRouter(t, nil)
	for p, duty := range map[string]string{
		"/v1/product-costing/mixed-costs":               "CostingCalculate",
		"/v1/product-costing/postings:cogs":             "CostingCalculate",
		"/v1/product-costing/postings:price-difference": "CostingCalculate",
		"/v1/product-costing/sales-order-estimates":     "CostingCalculate",
		"/v1/product-costing/comparisons":               "CostingView",
		"/v1/product-costing/reconciliations":           "CostingView",
	} {
		other := "CostingView"
		if duty == "CostingView" {
			other = "CostingCalculate"
		}
		if rec := do(r, http.MethodPost, p, `{}`, other, true); rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), duty) {
			t.Errorf("%s with only %s: %d %s", p, other, rec.Code, rec.Body.String())
		}
		if rec := do(r, http.MethodPost, p, `{}`, duty, true); rec.Code != http.StatusBadRequest {
			t.Errorf("%s with %s and an empty body: %d, want 400", p, duty, rec.Code)
		}
	}
}

func TestPostings_RoundToTheCurrencyMinorUnit(t *testing.T) {
	r := newRouter(t, nil)
	// 3 × (DM 10.4 + DL 5.3) = 47.1, which JPY posts as 47: DM 31 and DL 16, whole yen.
	body := `{"currency":"JPY","quantity":"3","unitCost":{"DM":"10.4","DL":"5.3"}}`
	rec := do(r, http.MethodPost, "/v1/product-costing/postings:cogs", body, allDuties, true)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	expectAll(t, rec.Body.String(), `"currency":"JPY"`, `"total":"47"`, `"account":"COGS-DL","debit":"16"`, `"account":"COGS-DM","debit":"31"`)
	if rec = do(r, http.MethodPost, "/v1/product-costing/postings:price-difference", `{"currency":"usd","quantity":"1","receiptUnitCost":"1","standard":{"DM":"1"}}`, allDuties, true); rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), `"currency"`) {
		t.Errorf("lower-case currency: %d %s", rec.Code, rec.Body.String())
	}
}

func TestMixedCost_ValidatesAlternatives(t *testing.T) {
	r := newRouter(t, nil)
	body := `{"itemId":"BUY","items":` + mixedItems + `,"alternatives":[
	 {"source":"A","percent":"50","itemId":"BUY"},
	 {"source":"A","percent":"150","itemId":"PROD"}]}`
	rec := do(r, http.MethodPost, "/v1/product-costing/mixed-costs", body, allDuties, true)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	expectAll(t, rec.Body.String(), `"alternatives[0].itemId":"must not be the mixed item itself"`, `"alternatives[1].source":"must be unique"`, `"alternatives[1].percent"`)
}

func TestRoll_ValidatesLineFlagsAndOperationScrap(t *testing.T) {
	r := newRouter(t, nil)
	body := `{"itemId":"P","items":[{"id":"X","kind":"Purchased","price":"1"},
	 {"id":"P","kind":"Manufactured","costingLot":"1","bom":[{"childId":"X","qty":"1","phantom":true},{"childId":"X","qty":"1","phantom":true,"makeToOrder":true}],
	  "operations":[{"runHours":"1","rate":"1","scrapPct":"100"}]}]}`
	rec := do(r, http.MethodPost, "/v1/product-costing/calculations", body, allDuties, true)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	expectAll(t, rec.Body.String(), `"items[1].bom[0].phantom"`, `"items[1].bom[1].makeToOrder"`, `"items[1].operations[0].scrapPct"`)
}

func TestSalesOrderEstimate_SplitSumsToCost(t *testing.T) {
	r := newRouter(t, nil)
	// 3 × (DM 0.335 + DL 0.335) = 2.01; each component alone rounds to 1.01,
	// so the split carries the residual on DM: DM 1.00 + DL 1.01 = 2.01.
	body := `{"itemId":"P","quantity":"3","netAmount":"3","items":[
	 {"id":"X","kind":"Purchased","price":"0.335"},
	 {"id":"P","kind":"Manufactured","costingLot":"1","bom":[{"childId":"X","qty":"1"}],"operations":[{"runHours":"1","rate":"0.335"}]}]}`
	rec := do(r, http.MethodPost, "/v1/product-costing/sales-order-estimates", body, allDuties, true)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	expectAll(t, rec.Body.String(), `"cost":"2.01"`, `"DM":"1"`, `"DL":"1.01"`, `"margin":"0.99"`)
}

func TestReconcile_RequiresInput(t *testing.T) {
	r := newRouter(t, nil)
	if rec := do(r, http.MethodPost, "/v1/product-costing/reconciliations", `{}`, allDuties, true); rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), `"onHand"`) {
		t.Errorf("empty reconciliation: %d %s", rec.Code, rec.Body.String())
	}
}
