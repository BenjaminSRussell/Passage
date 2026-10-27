package api

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/platform/testkit"
)

const allDuties = "CostingView,CostingCalculate,CostingActivate,CostingSystem"

// workedItems is the FR-39.4 example as the API receives it.
const workedItems = `[
 {"id":"RM-301","kind":"Purchased","price":"2.50"},
 {"id":"RM-302","kind":"Purchased","price":"1.00"},
 {"id":"RM-300","kind":"Purchased","price":"4.00"},
 {"id":"SA-200","kind":"Manufactured","costingLot":"1",
  "bom":[{"childId":"RM-301","qty":"3","scrapPct":"0"},{"childId":"RM-302","qty":"1","scrapPct":"10"}],
  "operations":[{"runHours":"0.25","setupHours":"0","rate":"60"}],
  "sheet":[{"id":"OH-MAT","type":"Surcharge","component":"IND-MAT","basis":["DM"],"percentage":"10"},
           {"id":"OH-LAB","type":"Surcharge","component":"IND-LAB","basis":["DL"],"percentage":"50"}]},
 {"id":"FG-100","kind":"Manufactured","costingLot":"10",
  "bom":[{"childId":"SA-200","qty":"2","scrapPct":"0"},{"childId":"RM-300","qty":"1","scrapPct":"0"}],
  "operations":[{"runHours":"0.5","setupHours":"1","rate":"40"}],
  "sheet":[{"id":"OH-MAT","type":"Surcharge","component":"IND-MAT","basis":["DM"],"percentage":"10"},
           {"id":"OH-LAB","type":"Surcharge","component":"IND-LAB","basis":["DL"],"percentage":"50"}]}
]`

func newRouter(t *testing.T, pinger Pinger) http.Handler {
	t.Helper()
	r := httpx.NewRouter()
	New(pinger, Config{}).Register(r)
	return r
}

func do(r http.Handler, method, path, body, duties string, withTenant bool) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	if withTenant {
		req.Header.Set(tenant.HeaderTenant, testkit.TenantID(1))
		req.Header.Set(tenant.HeaderDuties, duties)
	}
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	return rec
}

type failingPinger struct{}

func (failingPinger) Ping(context.Context) error { return errors.New("down") }

func TestAuth_TenantAndDutiesAreRequired(t *testing.T) {
	r := newRouter(t, nil)
	if rec := do(r, http.MethodPost, "/v1/costing/rolls", `{}`, "", false); rec.Code != http.StatusUnauthorized {
		t.Errorf("no tenant: %d", rec.Code)
	}
	if rec := do(r, http.MethodPost, "/v1/costing/activations:preview", `{}`, "CostingCalculate", true); rec.Code != http.StatusForbidden {
		t.Errorf("activation without CostingActivate: %d", rec.Code)
	}
	if rec := do(r, http.MethodPost, "/v1/costing/rolls", `{}`, "", true); rec.Code != http.StatusForbidden {
		t.Errorf("roll without CostingCalculate: %d", rec.Code)
	}
	if rec := do(r, http.MethodPost, "/v1/costing/variances/compute", `{}`, "CostingCalculate,CostingActivate", true); rec.Code != http.StatusForbidden {
		t.Errorf("variance compute without CostingSystem: %d", rec.Code)
	}
}

func TestRollOne_WorkedExampleOverHTTP(t *testing.T) {
	r := newRouter(t, nil)
	body := `{"itemId":"FG-100","items":` + workedItems + `}`
	rec := do(r, http.MethodPost, "/v1/costing/calculations", body, allDuties, true)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	for _, want := range []string{`"unitCost":"104.32"`, `"DM":"21.2"`, `"DL":"54"`, `"IND-MAT":"2.12"`, `"IND-LAB":"27"`} {
		if !strings.Contains(rec.Body.String(), want) {
			t.Errorf("body missing %s: %s", want, rec.Body.String())
		}
	}
}

func TestRollOne_ValidationNamesFields(t *testing.T) {
	r := newRouter(t, nil)
	rec := do(r, http.MethodPost, "/v1/costing/calculations", `{"items":`+workedItems+`,"itemId":"NOPE"}`, allDuties, true)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), `"itemId"`) {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	rec = do(r, http.MethodPost, "/v1/costing/rolls", `{"items":[{"id":"X","kind":"Bogus"}]}`, allDuties, true)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), `items[0].kind`) {
		t.Errorf("kind validation: %d %s", rec.Code, rec.Body.String())
	}
	rec = do(r, http.MethodPost, "/v1/costing/rolls", `{"items":[],"options":{"maxLevels":99}}`, allDuties, true)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("empty items: %d", rec.Code)
	}
}

func TestMassRoll_ReportsCycleAsErrorAndContinues(t *testing.T) {
	r := newRouter(t, nil)
	items := `[{"id":"A","kind":"Manufactured","bom":[{"childId":"B","qty":"1","scrapPct":"0"}]},
	 {"id":"B","kind":"Manufactured","bom":[{"childId":"A","qty":"1","scrapPct":"0"}]},
	 {"id":"OK","kind":"Purchased","price":"1"}]`
	rec := do(r, http.MethodPost, "/v1/costing/rolls", `{"items":`+items+`}`, allDuties, true)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	for _, want := range []string{`"itemCount":3`, `"errorCount":2`, `CST-0701`, `A -\u003e B -\u003e A`} {
		if !strings.Contains(body, want) && !strings.Contains(body, strings.ReplaceAll(want, `\u003e`, ">")) {
			t.Errorf("body missing %s: %s", want, body)
		}
	}
}

func TestVariance_AC395OverHTTP(t *testing.T) {
	r := newRouter(t, nil)
	body := `{"plannedQty":"100","rafQty":"90","lines":[
	 {"component":"DM","planned":true,"plannedQty":"300","stdUnitCost":"2","actualQty":"290","actualUnitCost":"2"},
	 {"component":"DL","planned":true,"fixed":true,"estPerLot":"40","stdUnitCost":"0.4","actualQty":"1","actualUnitCost":"40"},
	 {"component":"DM","planned":false,"actualQty":"5","actualUnitCost":"1.5"}]}`
	rec := do(r, http.MethodPost, "/v1/costing/variances/compute", body, allDuties, true)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	for _, want := range []string{`"total":"51.5"`, `"quantity":"40"`, `"substitution":"7.5"`} {
		if !strings.Contains(rec.Body.String(), want) {
			t.Errorf("body missing %s: %s", want, rec.Body.String())
		}
	}
}

func TestActivationPreview_AC394OverHTTP(t *testing.T) {
	r := newRouter(t, nil)
	body := `{"lines":[{"itemId":"X","siteId":"S1","qty":"1000","oldStd":"10","newStd":"10.5"}]}`
	rec := do(r, http.MethodPost, "/v1/costing/activations:preview", body, allDuties, true)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"total":"500"`) || !strings.Contains(rec.Body.String(), `"account":"Inventory"`) {
		t.Errorf("preview = %s", rec.Body.String())
	}
	rec = do(r, http.MethodPost, "/v1/costing/activations:preview", `{"lines":[{"oldStd":"-1"}]}`, allDuties, true)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), `"lines[0].oldStd"`) || !strings.Contains(rec.Body.String(), `"lines[0].siteId"`) {
		t.Errorf("invalid lines: %d %s", rec.Code, rec.Body.String())
	}
}

func TestReadyz_ReportsDatabaseFailure(t *testing.T) {
	if rec := do(newRouter(t, failingPinger{}), http.MethodGet, "/readyz", "", "", false); rec.Code != http.StatusServiceUnavailable {
		t.Errorf("readyz with failing pool: %d", rec.Code)
	}
	if rec := do(newRouter(t, nil), http.MethodGet, "/readyz", "", "", false); rec.Code != http.StatusOK {
		t.Errorf("readyz without pool: %d", rec.Code)
	}
}

func TestMetrics_CountComputations(t *testing.T) {
	a := New(nil, Config{})
	r := httpx.NewRouter()
	a.Register(r)
	do(r, http.MethodPost, "/v1/costing/calculations", `{"itemId":"FG-100","items":`+workedItems+`}`, allDuties, true)
	rec := httptest.NewRecorder()
	a.Metrics().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if !strings.Contains(rec.Body.String(), "productcost_calculations_total 1") {
		t.Errorf("metrics = %s", rec.Body.String())
	}
}

func TestRoll_RejectsInvalidStructure(t *testing.T) {
	r := newRouter(t, nil)
	cases := []struct {
		name, items, field string
	}{
		{"negative price", `[{"id":"X","kind":"Purchased","price":"-1"}]`, "items[0].price"},
		{"basis node is not evaluated in slice 1", `[{"id":"X","kind":"Manufactured","sheet":[{"id":"B","type":"Basis","component":"DM"}]}]`, "items[0].sheet[0].type"},
		{"surcharge without basis", `[{"id":"X","kind":"Manufactured","sheet":[{"id":"S","type":"Surcharge","component":"IND-MAT","percentage":"10"}]}]`, "items[0].sheet[0].basis"},
		{"setup without a lot size", `[{"id":"X","kind":"Manufactured","operations":[{"setupHours":"1","runHours":"0.5","rate":"40"}]}]`, "items[0].costingLot"},
		{"negative run hours", `[{"id":"X","kind":"Manufactured","costingLot":"1","operations":[{"runHours":"-0.5","rate":"40"}]}]`, "items[0].operations[0].runHours"},
		{"purchased item with a bom", `[{"id":"X","kind":"Purchased","price":"1","bom":[{"childId":"Y","qty":"1"}]}]`, "items[0].kind"},
		{"zero bom quantity", `[{"id":"X","kind":"Manufactured","bom":[{"childId":"Y","qty":"0"}]}]`, "items[0].bom[0].qty"},
		{"duplicate id", `[{"id":"X","kind":"Purchased","price":"1"},{"id":"X","kind":"Purchased","price":"2"}]`, "items[1].id"},
	}
	for _, tc := range cases {
		rec := do(r, http.MethodPost, "/v1/costing/rolls", `{"items":`+tc.items+`}`, allDuties, true)
		if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), `"`+tc.field+`"`) {
			t.Errorf("%s: status %d, want 400 naming %s: %s", tc.name, rec.Code, tc.field, rec.Body.String())
		}
	}
}

func TestRollOne_FailedItemIsAResultNotAnError(t *testing.T) {
	r := newRouter(t, nil)
	items := `[{"id":"P","kind":"Manufactured","bom":[{"childId":"C","qty":"1"}]},{"id":"C","kind":"Purchased"}]`
	rec := do(r, http.MethodPost, "/v1/costing/calculations", `{"itemId":"P","items":`+items+`}`, allDuties, true)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"status":"Error"`) || !strings.Contains(rec.Body.String(), "CST-0707") {
		t.Errorf("want an Error result blocked by the unpriced component: %s", rec.Body.String())
	}
}

func TestVariance_ValidationNamesFields(t *testing.T) {
	r := newRouter(t, nil)
	body := `{"currency":"usd","plannedQty":"0","rafQty":"-1","lines":[{"fixed":true,"actualQty":"1","actualUnitCost":"1"}]}`
	rec := do(r, http.MethodPost, "/v1/costing/variances/compute", body, allDuties, true)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	for _, field := range []string{"currency", "plannedQty", "rafQty", "lines[0].component", "lines[0].fixed"} {
		if !strings.Contains(rec.Body.String(), `"`+field+`"`) {
			t.Errorf("missing field %s: %s", field, rec.Body.String())
		}
	}
	if rec := do(r, http.MethodPost, "/v1/costing/variances/compute", `{"plannedQty":"1","rafQty":"1","lines":[]}`, allDuties, true); rec.Code != http.StatusBadRequest {
		t.Errorf("empty lines: %d", rec.Code)
	}
}

func TestActivationPreview_RoundsInTheRequestCurrency(t *testing.T) {
	r := newRouter(t, nil)
	body := `{"currency":"JPY","lines":[{"itemId":"X","siteId":"S1","qty":"7","oldStd":"100","newStd":"100.3333"}]}`
	rec := do(r, http.MethodPost, "/v1/costing/activations:preview", body, allDuties, true)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"total":"2"`) || !strings.Contains(rec.Body.String(), `"currency":"JPY"`) {
		t.Errorf("JPY preview: %d %s", rec.Code, rec.Body.String())
	}
	rec = do(r, http.MethodPost, "/v1/costing/activations:preview", `{"currency":"US","lines":[{"itemId":"X","siteId":"S","qty":"1","oldStd":"1","newStd":"2"}]}`, allDuties, true)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), `"currency"`) {
		t.Errorf("bad currency: %d %s", rec.Code, rec.Body.String())
	}
}
