package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/platform/testkit"
)

const cleanConversion = `"conversionDate":"2026-11-01","closeThrough":"2026-10-31","currency":"USD",
 "blockers":["NegativeOnHand","InTransit","StartedOrders"],
 "positions":[{"itemId":"IT-1","siteId":"S-1","onHand":"500","standardUnit":"10.00","actualValue":"4925.00"}]`

// doAs is do with a user id on the principal.
func doAs(r http.Handler, path, body, duties, user string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(tenant.HeaderTenant, testkit.TenantID(1))
	req.Header.Set(tenant.HeaderDuties, duties)
	if user != "" {
		req.Header.Set(tenant.HeaderUser, user)
	}
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	return rec
}

func TestVersionTransition_OverHTTP(t *testing.T) {
	r := newRouter(t, nil)
	open := `{"version":{"id":"v1","type":"Standard","status":"Open","fallback":"Active"},"action":"block"}`
	rec := do(r, http.MethodPost, "/v1/costing/versions:transition", open, "CostingMaintain", true)
	if rec.Code != http.StatusOK {
		t.Fatalf("block status %d: %s", rec.Code, rec.Body.String())
	}
	expectAll(t, rec.Body.String(), `"status":"Blocked"`)

	closeOpen := `{"version":{"id":"v1","type":"Standard","status":"Open","fallback":"Active"},"action":"close"}`
	rec = do(r, http.MethodPost, "/v1/costing/versions:transition", closeOpen, "CostingMaintain", true)
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("close an open version: %d", rec.Code)
	}
	expectAll(t, rec.Body.String(), "CST-0715")

	specified := `{"version":{"id":"v1","type":"Standard","status":"Open","fallback":"SpecifiedVersion"},"action":"block"}`
	rec = do(r, http.MethodPost, "/v1/costing/versions:transition", specified, "CostingMaintain", true)
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("specified fallback without a version: %d", rec.Code)
	}
	expectAll(t, rec.Body.String(), "CST-0716")

	// Segregation of duties: the maintain route never activates.
	activate := `{"version":{"id":"v1","type":"Standard","status":"Open","fallback":"Active"},"action":"activate"}`
	if rec = do(r, http.MethodPost, "/v1/costing/versions:transition", activate, "CostingMaintain", true); rec.Code != http.StatusBadRequest {
		t.Errorf("activate through the maintain route: %d", rec.Code)
	}
	if rec = do(r, http.MethodPost, "/v1/costing/versions:transition", open, "CostingActivate", true); rec.Code != http.StatusForbidden {
		t.Errorf("transition without CostingMaintain: %d", rec.Code)
	}
	bad := `{"version":{"id":"v1","type":"Standard","status":"Open","fallback":"Active"},"action":"explode"}`
	if rec = do(r, http.MethodPost, "/v1/costing/versions:transition", bad, "CostingMaintain", true); rec.Code != http.StatusBadRequest {
		t.Errorf("unknown action: %d", rec.Code)
	}
	noID := `{"version":{"type":"Standard","status":"Open","fallback":"Active"},"action":"block"}`
	rec = do(r, http.MethodPost, "/v1/costing/versions:transition", noID, "CostingMaintain", true)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), `"version.id"`) {
		t.Errorf("missing id: %d %s", rec.Code, rec.Body.String())
	}
}

func TestVersionActivate_OverHTTP(t *testing.T) {
	r := newRouter(t, nil)
	blocked := `{"version":{"id":"v1","type":"Standard","status":"Blocked","fallback":"Active"}}`
	rec := do(r, http.MethodPost, "/v1/costing/versions:activate", blocked, "CostingActivate", true)
	if rec.Code != http.StatusOK {
		t.Fatalf("activate status %d: %s", rec.Code, rec.Body.String())
	}
	expectAll(t, rec.Body.String(), `"status":"Active"`)

	sim := `{"version":{"id":"s1","type":"Simulation","status":"Open","fallback":"None"}}`
	rec = do(r, http.MethodPost, "/v1/costing/versions:activate", sim, "CostingActivate", true)
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("simulation activate status %d", rec.Code)
	}
	expectAll(t, rec.Body.String(), "CST-0711")

	closed := `{"version":{"id":"v1","type":"Standard","status":"Closed","fallback":"Active"}}`
	rec = do(r, http.MethodPost, "/v1/costing/versions:activate", closed, "CostingActivate", true)
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("closed activate status %d", rec.Code)
	}
	expectAll(t, rec.Body.String(), "CST-0715")

	if rec = do(r, http.MethodPost, "/v1/costing/versions:activate", blocked, "CostingMaintain", true); rec.Code != http.StatusForbidden {
		t.Errorf("activate without CostingActivate: %d", rec.Code)
	}
}

func TestConversionValidate_OverHTTP(t *testing.T) {
	r := newRouter(t, nil)
	rec := do(r, http.MethodPost, "/v1/costing/conversions:validate", "{"+cleanConversion+"}", "CostingActivate", true)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	var rep struct {
		Blocked       bool   `json:"blocked"`
		CloseComplete bool   `json:"closeComplete"`
		Revaluation   string `json:"revaluation"`
		Lines         []struct {
			Amount string `json:"amount"`
		} `json:"lines"`
		Journal []struct {
			Account string `json:"account"`
			Debit   string `json:"debit"`
		} `json:"journal"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &rep); err != nil {
		t.Fatal(err)
	}
	// AC-39.10: 500 × 10.00 − 4,925.00 = 75.00, Dr Inventory / Cr Cost change variance.
	if rep.Blocked || !rep.CloseComplete || rep.Revaluation != "75" || len(rep.Lines) != 1 || rep.Lines[0].Amount != "75" {
		t.Errorf("report = %+v", rep)
	}
	if len(rep.Journal) != 2 || rep.Journal[0].Account != "Inventory" || rep.Journal[0].Debit != "75" {
		t.Errorf("journal = %+v", rep.Journal)
	}

	late := `{"conversionDate":"2026-11-01","closeThrough":"2026-10-30","positions":[{"itemId":"A","siteId":"S","onHand":"1","standardUnit":"1","actualValue":"1"}]}`
	rec = do(r, http.MethodPost, "/v1/costing/conversions:validate", late, "CostingActivate", true)
	expectAll(t, rec.Body.String(), `"closeComplete":false`, "CST-0731")

	cases := []struct{ name, body, field string }{
		{"bad date", `{"conversionDate":"soon","positions":[{"itemId":"A","siteId":"S"}]}`, "conversionDate"},
		{"unknown blocker", `{"conversionDate":"2026-11-01","blockers":["Vibes"],"positions":[{"itemId":"A","siteId":"S"}]}`, "blockers[0]"},
		{"no positions", `{"conversionDate":"2026-11-01"}`, "positions"},
		{"duplicate position", `{"conversionDate":"2026-11-01","positions":[{"itemId":"A","siteId":"S"},{"itemId":"A","siteId":"S"}]}`, "positions[1].itemId"},
		{"bad currency", `{"conversionDate":"2026-11-01","currency":"usd","positions":[{"itemId":"A","siteId":"S"}]}`, "currency"},
	}
	for _, tc := range cases {
		rec := do(r, http.MethodPost, "/v1/costing/conversions:validate", tc.body, "CostingActivate", true)
		if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), `"`+tc.field+`"`) {
			t.Errorf("%s: status %d, want 400 naming %s: %s", tc.name, rec.Code, tc.field, rec.Body.String())
		}
	}
	if rec = do(r, http.MethodPost, "/v1/costing/conversions:validate", "{"+cleanConversion+"}", "CostingCalculate", true); rec.Code != http.StatusForbidden {
		t.Errorf("validate without CostingActivate: %d", rec.Code)
	}
}

func TestConversionAdvance_OverHTTP(t *testing.T) {
	r := newRouter(t, nil)
	run := `{"status":"Draft","checklist":[]}`
	// The user is the principal, never a body field.
	spoofed := `{"run":` + run + `,"step":"select","user":"mallory","input":{` + cleanConversion + `}}`
	if rec := doAs(r, "/v1/costing/conversions:advance", spoofed, "CostingActivate", "alice"); rec.Code != http.StatusBadRequest {
		t.Errorf("user in the body: %d, want 400", rec.Code)
	}
	body := `{"run":` + run + `,"step":"select","input":{` + cleanConversion + `}}`
	rec := doAs(r, "/v1/costing/conversions:advance", body, "CostingActivate", "alice")
	if rec.Code != http.StatusOK {
		t.Fatalf("select status %d: %s", rec.Code, rec.Body.String())
	}
	expectAll(t, rec.Body.String(), `"step":"select"`, `"user":"alice"`, `"status":"Draft"`)

	if rec = doAs(r, "/v1/costing/conversions:advance", body, "CostingActivate", ""); rec.Code != http.StatusUnauthorized {
		t.Errorf("no user on the principal: %d", rec.Code)
	}

	early := `{"run":` + run + `,"step":"validate","input":{` + cleanConversion + `}}`
	rec = doAs(r, "/v1/costing/conversions:advance", early, "CostingActivate", "alice")
	if rec.Code != http.StatusUnprocessableEntity {
		t.Errorf("out-of-order step: %d", rec.Code)
	}
	expectAll(t, rec.Body.String(), "CST-0736")

	done := `{"step":"select","status":"Done","user":"alice","at":"2026-10-28T09:00:00Z"},{"step":"roll","status":"Done","user":"alice","at":"2026-10-28T09:00:00Z"}`
	blocked := `{"run":{"status":"Draft","checklist":[` + done + `]},"step":"validate","input":{"conversionDate":"2026-11-01","closeThrough":"2026-10-31","blockers":["NegativeOnHand"],"positions":[{"itemId":"IT-1","siteId":"S-1","onHand":"-2","standardUnit":"10","actualValue":"-20"}]}}`
	rec = doAs(r, "/v1/costing/conversions:advance", blocked, "CostingActivate", "alice")
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("blocked validate status %d: %s", rec.Code, rec.Body.String())
	}
	expectAll(t, rec.Body.String(), "CST-0735")

	forged := `{"run":{"status":"Validated","checklist":[{"step":"validate","status":"Done","user":"alice","at":"2026-10-28T09:00:00Z"}]},"step":"preview","input":{` + cleanConversion + `}}`
	rec = doAs(r, "/v1/costing/conversions:advance", forged, "CostingActivate", "alice")
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), `"run.checklist[0]"`) {
		t.Errorf("forged checklist: %d %s", rec.Code, rec.Body.String())
	}

	unknownStep := `{"run":` + run + `,"step":"party","input":{` + cleanConversion + `}}`
	if rec = doAs(r, "/v1/costing/conversions:advance", unknownStep, "CostingActivate", "alice"); rec.Code != http.StatusBadRequest {
		t.Errorf("unknown step: %d", rec.Code)
	}
}

// TestRoll_GroupViewAndSheetSubtotals covers the slice-3 roll options over
// HTTP: AC-39.14 (the Group view rolls the supplier's Legal-view cost of an
// intercompany component) and Basis / Total subtotals with a surcharge on a
// Total (FR-39.2).
func TestRoll_GroupViewAndSheetSubtotals(t *testing.T) {
	r := newRouter(t, nil)
	items := `[{"id":"RM-IC","kind":"Purchased","price":"120.00","intercompany":true,"legalCost":"100.00"},
	 {"id":"FG","kind":"Manufactured","costingLot":"1","bom":[{"childId":"RM-IC","qty":"1"}],
	  "operations":[{"runHours":"0.5","rate":"40"}],
	  "sheet":[{"id":"B-DM","type":"Basis","basis":["DM"]},{"id":"B-DL","type":"Basis","basis":["DL"]},
	   {"id":"T","type":"Total","refs":["B-DM","B-DL"]},{"id":"OH","type":"Surcharge","component":"IND","basisRef":"T","percentage":"10"}]}]`
	for view, want := range map[string][]string{
		"Legal": {`"unitCost":"154"`, `"DM":"120"`, `"T":"140"`, `"IND":"14"`},
		"Group": {`"unitCost":"132"`, `"DM":"100"`, `"T":"120"`, `"IND":"12"`},
	} {
		body := `{"itemId":"FG","options":{"view":"` + view + `"},"items":` + items + `}`
		rec := do(r, http.MethodPost, "/v1/costing/calculations", body, allDuties, true)
		if rec.Code != http.StatusOK {
			t.Fatalf("%s: status %d: %s", view, rec.Code, rec.Body.String())
		}
		expectAll(t, rec.Body.String(), want...)
	}
	rec := do(r, http.MethodPost, "/v1/costing/calculations", `{"itemId":"FG","options":{"view":"ProfitCenter"},"items":`+items+`}`, allDuties, true)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), `"options"`) {
		t.Errorf("ProfitCenter view: %d %s", rec.Code, rec.Body.String())
	}
}
