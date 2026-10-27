package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/services/productcost/internal/domain"
)

func mustDec(s string) decimal.Decimal { return decimal.RequireFromString(s) }

func newAPIRouter(t *testing.T) (*API, http.Handler) {
	t.Helper()
	a := New(nil, Config{})
	r := httpx.NewRouter()
	a.Register(r)
	return a, r
}

func TestAdHocEstimate_OverHTTP(t *testing.T) {
	a, r := newAPIRouter(t)
	body, _ := json.Marshal(adHocRequest{
		Model: domain.CostingModel{
			Code:            "INJ",
			Characteristics: []domain.Characteristic{{Name: "weight"}},
			Lines: []domain.ModelLine{{Type: domain.LineActivity, Ref: "MACHINE",
				Component: "DL", QuantityFormula: "weight"}},
		},
		ObjectType:      domain.ObjectQuote,
		ObjectID:        "Q-9",
		Characteristics: map[string]decimal.Decimal{"weight": mustDec("0.5")},
		Prices:          map[string]domain.Price{"MACHINE": {Amount: mustDec("40")}},
		Currency:        "USD",
	})
	rec := do(r, http.MethodPost, "/v1/costing/estimates", string(body), allDuties, true)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	var est domain.AdHocEstimate
	if err := json.Unmarshal(rec.Body.Bytes(), &est); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if est.Total.StringFixed(2) != "20.00" || est.Split["DL"].StringFixed(2) != "20.00" || est.Currency != "USD" {
		t.Errorf("estimate = %+v, want DL 20.00 total 20.00 in USD", est)
	}
	metrics := httptest.NewRecorder()
	a.Metrics().ServeHTTP(metrics, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if !strings.Contains(metrics.Body.String(), "productcost_adhoc_estimates_total 1") {
		t.Errorf("metrics: %s", metrics.Body.String())
	}
}

func TestAdHocEstimate_RejectsBadRequests(t *testing.T) {
	r := newRouter(t, nil)
	// Unknown object type is a field error (400); a model the domain refuses is 422.
	if rec := do(r, http.MethodPost, "/v1/costing/estimates",
		`{"objectType":"Invoice","objectId":"x","model":{}}`, allDuties, true); rec.Code != http.StatusBadRequest ||
		!strings.Contains(rec.Body.String(), "objectType") {
		t.Errorf("object type: %d %s", rec.Code, rec.Body.String())
	}
	if rec := do(r, http.MethodPost, "/v1/costing/estimates",
		`{"objectType":"Quote","objectId":"x","model":{"code":"M"}}`, allDuties, true); rec.Code != http.StatusUnprocessableEntity {
		t.Errorf("model without lines: %d %s", rec.Code, rec.Body.String())
	}
	if rec := do(r, http.MethodPost, "/v1/costing/estimates",
		`{"objectType":"Quote","objectId":"x","routingHours":"-1","model":{}}`, allDuties, true); rec.Code != http.StatusBadRequest {
		t.Errorf("negative hours: %d", rec.Code)
	}
	if rec := do(r, http.MethodPost, "/v1/costing/estimates", `{}`, "", true); rec.Code != http.StatusForbidden {
		t.Errorf("estimate without costing.calculate: %d", rec.Code)
	}
}

// salesBody builds a refresh request for FG-100 (the FR-39.4 worked example) at
// a net price of 200.00 with the given configuration hash and prior versions.
func salesBody(t *testing.T, hash string, versions []domain.EstimateVersion) string {
	t.Helper()
	var items []domain.Item
	if err := json.Unmarshal([]byte(workedItems), &items); err != nil {
		t.Fatalf("items: %v", err)
	}
	body, err := json.Marshal(refreshRequest{
		salesRequest: salesRequest{Items: items, ItemID: "FG-100",
			Quantity: mustDec("1"), NetAmount: mustDec("200")},
		LineID: "L-1", ConfigHash: hash, Versions: versions,
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return string(body)
}

func TestSalesEstimateLifecycle_OverHTTP(t *testing.T) {
	r := newRouter(t, nil)

	rec := do(r, http.MethodPost, "/v1/costing/sales-order-estimates:refresh", salesBody(t, "cfg-a", nil), allDuties, true)
	if rec.Code != http.StatusOK {
		t.Fatalf("refresh status %d: %s", rec.Code, rec.Body.String())
	}
	var first domain.RefreshResult
	if err := json.Unmarshal(rec.Body.Bytes(), &first); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !first.Changed || first.Current.Version != 1 {
		t.Fatalf("first refresh = %+v, want version 1", first.Current)
	}

	// Same configuration: nothing changes.
	rec = do(r, http.MethodPost, "/v1/costing/sales-order-estimates:refresh",
		salesBody(t, "cfg-a", first.Versions), allDuties, true)
	var same domain.RefreshResult
	_ = json.Unmarshal(rec.Body.Bytes(), &same)
	if rec.Code != http.StatusOK || same.Changed {
		t.Fatalf("unchanged refresh: %d changed=%v", rec.Code, same.Changed)
	}

	// Configuration change: version 2 supersedes version 1 (BR-39.14).
	rec = do(r, http.MethodPost, "/v1/costing/sales-order-estimates:refresh",
		salesBody(t, "cfg-b", first.Versions), allDuties, true)
	var next domain.RefreshResult
	_ = json.Unmarshal(rec.Body.Bytes(), &next)
	if rec.Code != http.StatusOK || next.Current.Version != 2 || next.Versions[0].Status != domain.EstimateSuperseded {
		t.Fatalf("changed refresh: %d %+v", rec.Code, next.Current)
	}

	// Freeze at confirmation.
	freeze, _ := json.Marshal(freezeRequest{LineID: "L-1", Versions: next.Versions})
	rec = do(r, http.MethodPost, "/v1/costing/sales-order-estimates:freeze", string(freeze), allDuties, true)
	var frozen freezeResponse
	_ = json.Unmarshal(rec.Body.Bytes(), &frozen)
	if rec.Code != http.StatusOK || frozen.Confirmed.Status != domain.EstimateFrozen {
		t.Fatalf("freeze: %d %s", rec.Code, rec.Body.String())
	}
}

func TestSalesEstimateLifecycle_ValidationAndRefusals(t *testing.T) {
	r := newRouter(t, nil)
	if rec := do(r, http.MethodPost, "/v1/costing/sales-order-estimates:refresh",
		salesBody(t, "", nil), allDuties, true); rec.Code != http.StatusBadRequest ||
		!strings.Contains(rec.Body.String(), "configHash") {
		t.Errorf("empty configHash: %d %s", rec.Code, rec.Body.String())
	}
	if rec := do(r, http.MethodPost, "/v1/costing/sales-order-estimates:freeze",
		`{"versions":[]}`, allDuties, true); rec.Code != http.StatusBadRequest {
		t.Errorf("freeze without lineId: %d", rec.Code)
	}
	if rec := do(r, http.MethodPost, "/v1/costing/sales-order-estimates:freeze",
		`{"lineId":"L-1","versions":[]}`, allDuties, true); rec.Code != http.StatusUnprocessableEntity ||
		!strings.Contains(rec.Body.String(), domain.CodeNoEstimate) {
		t.Errorf("freeze without estimate: %d %s", rec.Code, rec.Body.String())
	}
	if rec := do(r, http.MethodPost, "/v1/costing/sales-order-estimates:refresh",
		salesBody(t, "cfg-a", nil), "", true); rec.Code != http.StatusForbidden {
		t.Errorf("refresh without costing.calculate: %d", rec.Code)
	}
}

func TestAdHocEstimate_ValidatesPricesSheetAndCurrency(t *testing.T) {
	r := newRouter(t, nil)
	model := `"model":{"code":"M","lines":[{"type":"Item","ref":"RM","component":"DM","quantityFormula":"2"}]}`
	cases := map[string]string{
		"currency":          `{"currency":"usd","objectType":"Quote","objectId":"Q",` + model + `}`,
		"prices.RM.amount":  `{"objectType":"Quote","objectId":"Q","prices":{"RM":{"amount":"-1"}},` + model + `}`,
		"sheet[0].basisRef": `{"objectType":"Quote","objectId":"Q","sheet":[{"id":"S","type":"Surcharge","component":"OH","basisRef":"NOPE"}],` + model + `}`,
	}
	for field, body := range cases {
		rec := do(r, http.MethodPost, "/v1/costing/estimates", body, allDuties, true)
		if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), `"`+field+`"`) {
			t.Errorf("%s: %d %s", field, rec.Code, rec.Body.String())
		}
	}
	// No price for the line: CST-0702 from the domain.
	rec := do(r, http.MethodPost, "/v1/costing/estimates", `{"objectType":"Quote","objectId":"Q",`+model+`}`, allDuties, true)
	if rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), "CST-0702") {
		t.Errorf("missing price: %d %s", rec.Code, rec.Body.String())
	}
	// A characteristic the model does not have: CST-0764.
	rec = do(r, http.MethodPost, "/v1/costing/estimates", `{"objectType":"Quote","objectId":"Q","characteristics":{"x":"1"},"prices":{"RM":{"amount":"1"}},`+model+`}`, allDuties, true)
	if rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), domain.CodeEstimateInvalid) {
		t.Errorf("unknown characteristic: %d %s", rec.Code, rec.Body.String())
	}
}

func TestSalesEstimateLifecycle_AC39_13OverHTTP(t *testing.T) {
	r := newRouter(t, nil)
	body := `{"currency":"USD","itemId":"CFG-LINE","quantity":"1","netAmount":"2400.00","lineId":"SO-1/10","configHash":"cfg-1",
	 "items":[{"id":"RM-FRAME","kind":"Purchased","price":"1500.00"},
	  {"id":"CFG-LINE","kind":"Manufactured","costingLot":"1","bom":[{"childId":"RM-FRAME","qty":"1"}],"operations":[{"runHours":"6","rate":"60"}]}]}`
	rec := do(r, http.MethodPost, "/v1/costing/sales-order-estimates:refresh", body, allDuties, true)
	if rec.Code != http.StatusOK {
		t.Fatalf("refresh: %d %s", rec.Code, rec.Body.String())
	}
	var res domain.RefreshResult
	if err := json.Unmarshal(rec.Body.Bytes(), &res); err != nil {
		t.Fatal(err)
	}
	if res.Current.Estimate.Margin.StringFixed(2) != "540.00" || res.Current.Estimate.MarginPct.StringFixed(2) != "22.50" {
		t.Fatalf("estimate = %+v, want margin 540.00 (22.50%%)", res.Current.Estimate)
	}
	freeze, _ := json.Marshal(freezeRequest{LineID: "SO-1/10", Versions: res.Versions})
	rec = do(r, http.MethodPost, "/v1/costing/sales-order-estimates:freeze", string(freeze), allDuties, true)
	var frozen freezeResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &frozen); err != nil || rec.Code != http.StatusOK {
		t.Fatalf("freeze: %d %s", rec.Code, rec.Body.String())
	}
	if frozen.Confirmed.Status != domain.EstimateFrozen || frozen.Confirmed.Estimate.Cost.StringFixed(2) != "1860.00" {
		t.Errorf("confirmed = %+v, want frozen at 1860.00", frozen.Confirmed)
	}
}

func TestSalesEstimateLifecycle_RejectsForgedHistory(t *testing.T) {
	r := newRouter(t, nil)
	foreign := []domain.EstimateVersion{{LineID: "L-9", Version: 1, ConfigHash: "x", Status: domain.StatusPending}}
	if rec := do(r, http.MethodPost, "/v1/costing/sales-order-estimates:refresh", salesBody(t, "cfg-a", foreign), allDuties, true); rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), `"versions"`) {
		t.Errorf("refresh with another line's version: %d %s", rec.Code, rec.Body.String())
	}
	freeze, _ := json.Marshal(freezeRequest{LineID: "L-1", Versions: []domain.EstimateVersion{{LineID: "L-1", Version: 1, ConfigHash: "x", Status: "Approved"}}})
	if rec := do(r, http.MethodPost, "/v1/costing/sales-order-estimates:freeze", string(freeze), allDuties, true); rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), `"versions"`) {
		t.Errorf("freeze with an unknown status: %d %s", rec.Code, rec.Body.String())
	}
}

func TestRoll_ProfitCenterViewOverHTTP(t *testing.T) {
	r := newRouter(t, nil)
	items := `[{"id":"RM-PC","kind":"Purchased","price":"120.00","profitCenterSource":true,"legalCost":"80.00","costPlusPct":"12.5"},
	 {"id":"FG","kind":"Manufactured","costingLot":"1","bom":[{"childId":"RM-PC","qty":"2"}]}]`
	rec := do(r, http.MethodPost, "/v1/costing/calculations", `{"itemId":"FG","options":{"view":"ProfitCenter"},"items":`+items+`}`, allDuties, true)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	expectAll(t, rec.Body.String(), `"unitCost":"180"`)
	rec = do(r, http.MethodPost, "/v1/costing/calculations", `{"itemId":"FG","items":`+items+`}`, allDuties, true)
	expectAll(t, rec.Body.String(), `"unitCost":"240"`)

	bad := `[{"id":"RM","kind":"Purchased","price":"1","profitCenterSource":true,"intercompany":true,"transferPrice":"-1"},
	 {"id":"FG","kind":"Manufactured","profitCenterSource":true,"bom":[{"childId":"RM","qty":"1"}]}]`
	rec = do(r, http.MethodPost, "/v1/costing/calculations", `{"itemId":"FG","items":`+bad+`}`, allDuties, true)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("bad profit-center items: %d %s", rec.Code, rec.Body.String())
	}
	expectAll(t, rec.Body.String(), `"items[0].transferPrice"`, `"items[0].profitCenterSource"`, `"items[1].profitCenterSource"`)
}
