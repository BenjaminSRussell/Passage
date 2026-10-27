package api

import (
	"net/http"
	"testing"
)

const (
	pathSupply    = "/v1/planning/supply-schedule:project"
	pathShelfLife = "/v1/planning/shelf-life:allocate"
)

func TestSupplySchedule_WeeklyProjection(t *testing.T) {
	body := `{"horizonDays":14,"bucket":"week","openingOnHand":"10","safetyStock":"5","unitCost":"2.50","currency":"EUR",
	 "demand":[{"day":3,"qty":"4"},{"day":10,"qty":"6"}],"supply":[{"day":5,"qty":"20"}],"planned":[{"day":12,"qty":"8"}]}`
	rec := do(t, &fakeStore{}, http.MethodPost, pathSupply, body, "planning.read", true, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d body %s", rec.Code, rec.Body.String())
	}
	out := decodeBody(t, rec.Body.String())
	periods := out["periods"].([]any)
	if len(periods) != 2 {
		t.Fatalf("periods = %d, want 2", len(periods))
	}
	w1 := periods[0].(map[string]any)
	assertDecimal(t, "week1.closingOnHand", w1["closingOnHand"], "26")
	if w1["value"] != "65.00" || out["currency"] != "EUR" {
		t.Fatalf("week1 value %v %v, want 65.00 EUR", w1["value"], out["currency"])
	}
	assertDecimal(t, "week1.daysOfSupply", w1["daysOfSupply"], "130")
	w2 := periods[1].(map[string]any)
	if w2["daysOfSupply"] != nil {
		t.Fatalf("week2 daysOfSupply = %v, want null", w2["daysOfSupply"])
	}
	totals := out["totals"].(map[string]any)
	assertDecimal(t, "totals.closingOnHand", totals["closingOnHand"], "28")
}

func TestSupplySchedule_Validation(t *testing.T) {
	tests := map[string]struct{ body, field string }{
		"bad bucket":       {`{"horizonDays":7,"bucket":"quarter","currency":"EUR"}`, "bucket"},
		"zero horizon":     {`{"horizonDays":0,"bucket":"day","currency":"EUR"}`, "horizonDays"},
		"negative opening": {`{"horizonDays":7,"bucket":"day","currency":"EUR","openingOnHand":"-1"}`, "openingOnHand"},
		"negative cost":    {`{"horizonDays":7,"bucket":"day","currency":"EUR","unitCost":"-1"}`, "unitCost"},
		"no currency":      {`{"horizonDays":7,"bucket":"day"}`, "currency"},
		"bad currency":     {`{"horizonDays":7,"bucket":"day","currency":"eur"}`, "currency"},
		"negative flow":    {`{"horizonDays":7,"bucket":"day","currency":"EUR","demand":[{"day":1,"qty":"-2"}]}`, "demand[0].qty"},
		"flow day too far": {`{"horizonDays":7,"bucket":"day","currency":"EUR","supply":[{"day":9999,"qty":"2"}]}`, "supply[0].day"},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			assertFieldError(t, do(t, &fakeStore{}, http.MethodPost, pathSupply, tc.body, "planning.read", true, ""), tc.field)
		})
	}
}

func TestSupplySchedule_RequiresTenantAndDuty(t *testing.T) {
	body := `{"horizonDays":7,"bucket":"day","currency":"EUR"}`
	if rec := do(t, &fakeStore{}, http.MethodPost, pathSupply, body, "planning.read", false, ""); rec.Code != http.StatusUnauthorized {
		t.Fatalf("no tenant: status %d", rec.Code)
	}
	if rec := do(t, &fakeStore{}, http.MethodPost, pathSupply, body, "planning.run", true, ""); rec.Code != http.StatusForbidden {
		t.Fatalf("wrong duty: status %d", rec.Code)
	}
}

// AC-22.15: a batch expiring in 20 days cannot serve a demand 5 days out that
// needs 30 days of remaining shelf life, so the 30 is short and the batch is
// reported as expiring unpegged within the horizon.
func TestShelfLife_AC2215(t *testing.T) {
	body := `{"demandDay":5,"demandQty":"30","requiredShelfLifeDays":30,"horizonDay":25,
	 "batches":[{"id":"B1","qty":"100","expiryDay":20}]}`
	rec := do(t, &fakeStore{}, http.MethodPost, pathShelfLife, body, "planning.read", true, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d body %s", rec.Code, rec.Body.String())
	}
	out := decodeBody(t, rec.Body.String())
	if allocs := out["allocations"].([]any); len(allocs) != 0 {
		t.Fatalf("allocations = %v, want none", allocs)
	}
	assertDecimal(t, "shortfallQty", out["shortfallQty"], "30")
	expiring := out["expiringUnpegged"].([]any)
	if len(expiring) != 1 {
		t.Fatalf("expiringUnpegged = %v, want one batch", expiring)
	}
	first := expiring[0].(map[string]any)
	if first["batchId"] != "B1" {
		t.Fatalf("expiring batch = %v, want B1", first["batchId"])
	}
	assertDecimal(t, "expiring qty", first["qty"], "100")
	if first["expiryDay"] != float64(20) {
		t.Fatalf("expiryDay = %#v, want the integer 20", first["expiryDay"])
	}
}

func TestShelfLife_FEFOAllocationWithoutHorizon(t *testing.T) {
	body := `{"demandDay":0,"demandQty":"50","requiredShelfLifeDays":0,
	 "batches":[{"id":"late","qty":"40","expiryDay":300},{"id":"early","qty":"30","expiryDay":100}]}`
	rec := do(t, &fakeStore{}, http.MethodPost, pathShelfLife, body, "planning.read", true, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d body %s", rec.Code, rec.Body.String())
	}
	out := decodeBody(t, rec.Body.String())
	allocs := out["allocations"].([]any)
	if len(allocs) != 2 || allocs[0].(map[string]any)["batchId"] != "early" {
		t.Fatalf("allocations = %v, want early first", allocs)
	}
	assertDecimal(t, "shortfallQty", out["shortfallQty"], "0")
	if expiring := out["expiringUnpegged"].([]any); len(expiring) != 0 {
		t.Fatalf("no horizon means no expiring list, got %v", expiring)
	}
}

func TestShelfLife_Validation(t *testing.T) {
	tests := map[string]struct{ body, field string }{
		"negative demand":     {`{"demandDay":1,"demandQty":"-1","requiredShelfLifeDays":0,"batches":[]}`, "demandQty"},
		"past demand day":     {`{"demandDay":-1,"demandQty":"1","requiredShelfLifeDays":0,"batches":[]}`, "demandDay"},
		"duplicate batch":     {`{"demandDay":1,"demandQty":"1","requiredShelfLifeDays":0,"batches":[{"id":"B","qty":"1","expiryDay":5},{"id":"B","qty":"1","expiryDay":6}]}`, "batches[1].id"},
		"negative batch":      {`{"demandDay":1,"demandQty":"1","requiredShelfLifeDays":0,"batches":[{"id":"B","qty":"-1","expiryDay":5}]}`, "batches[0].qty"},
		"shelf life too long": {`{"demandDay":1,"demandQty":"1","requiredShelfLifeDays":5000,"batches":[]}`, "requiredShelfLifeDays"},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			assertFieldError(t, do(t, &fakeStore{}, http.MethodPost, pathShelfLife, tc.body, "planning.read", true, ""), tc.field)
		})
	}
}
