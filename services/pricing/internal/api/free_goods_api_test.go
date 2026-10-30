package api_test

import (
	"fmt"
	"net/http"
	"testing"
)

const (
	freeGoodsBody = `{"legalEntityId":"%s","customerKind":"all","productId":"P1","unit":"EA","kind":"%s","calcRule":"%s",` +
		`"everyQuantity":"10","freeQuantity":"1","minQuantity":"0","validFrom":"2026-01-01","validTo":"2026-12-31"}`
	freeResolveBody = `{"legalEntityId":"%s","customerId":"C1","currency":"USD","pricingDate":"2026-06-01",
	"lines":[{"productId":"P1","unit":"EA","quantity":"%s"}]}`
)

func createFreeGoods(t *testing.T, h http.Handler, kind, calc, key string) map[string]any {
	t.Helper()
	code, out := call(t, h, http.MethodPost, "/v1/pricing/free-goods-rules", campaignDuties, fmt.Sprintf(freeGoodsBody, legalEntity, kind, calc), "Idempotency-Key", key)
	if code != http.StatusCreated {
		t.Fatalf("create free goods rule = %d %v", code, out)
	}
	return out
}

func freeResolve(t *testing.T, h http.Handler, qty string) map[string]any {
	t.Helper()
	code, out := call(t, h, http.MethodPost, "/v1/pricing/resolve", allDuties, fmt.Sprintf(freeResolveBody, legalEntity, qty))
	if code != http.StatusOK {
		t.Fatalf("resolve = %d %v", code, out)
	}
	return out
}

func freeProposals(t *testing.T, h http.Handler, qty string) []any {
	t.Helper()
	return freeResolve(t, h, qty)["freeGoods"].([]any)
}

// TestFreeGoods_ResolveProposesZeroPricedLine covers FR-26.22 and AC-26.13 end to end.
func TestFreeGoods_ResolveProposesZeroPricedLine(t *testing.T) {
	h := router(newFake())
	activeP1Record(t, h, "9.00", "")
	if got := freeProposals(t, h, "25"); len(got) != 0 {
		t.Fatalf("no rule: proposals = %v", got)
	}
	rule := createFreeGoods(t, h, "INCLUSIVE", "WHOLE_UNITS", "fg-1")
	if rule["version"] != float64(1) || rule["withdrawn"] != false || rule["everyQuantity"] != "10" {
		t.Fatalf("created rule = %v", rule)
	}
	out := freeResolve(t, h, "25")
	got := out["freeGoods"].([]any)
	if len(got) != 1 {
		t.Fatalf("proposals = %v; want one", got)
	}
	// The priced line charges the 23 units only (BR-26.13), so the order net is 23 x 9.00.
	line := out["lines"].([]any)[0].(map[string]any)
	if line["quantity"] != "23" || line["lineNet"] != "207" || out["orderNet"] != "207" {
		t.Fatalf("priced line = %v order net %v; want 23 units, 207.00", line, out["orderNet"])
	}
	p := got[0].(map[string]any)
	if p["pricedQuantity"] != "23" || p["freeQuantity"] != "2" || p["orderedQuantity"] != "25" || p["ruleId"] != rule["id"] || p["kind"] != "INCLUSIVE" {
		t.Fatalf("inclusive proposal = %v; want 23 priced, 2 free", p)
	}
	if got := freeProposals(t, h, "9"); len(got) != 0 {
		t.Fatalf("below one multiple: proposals = %v", got)
	}

	code, w := call(t, h, http.MethodPost, "/v1/pricing/free-goods-rules/"+rule["id"].(string)+":withdraw", campaignDuties, `{"version":1}`)
	if code != http.StatusOK || w["withdrawn"] != true || w["version"] != float64(2) {
		t.Fatalf("withdraw = %d %v", code, w)
	}
	if got := freeProposals(t, h, "25"); len(got) != 0 {
		t.Fatalf("withdrawn rule still proposes: %v", got)
	}
	if code, _ := call(t, h, http.MethodPost, "/v1/pricing/free-goods-rules/"+rule["id"].(string)+":withdraw", campaignDuties, `{"version":2}`); code != http.StatusConflict {
		t.Fatalf("second withdraw = %d; want 409", code)
	}
	if code, _ := call(t, h, http.MethodPost, "/v1/pricing/free-goods-rules/"+rule["id"].(string)+":withdraw", campaignDuties, `{"version":1}`); code != http.StatusPreconditionFailed {
		t.Fatalf("stale withdraw = %d; want 412", code)
	}
}

func TestFreeGoods_ExclusiveAndOverlap(t *testing.T) {
	h := router(newFake())
	activeP1Record(t, h, "9.00", "")
	createFreeGoods(t, h, "EXCLUSIVE", "UNIT_REFERENCE", "fg-x")
	out := freeResolve(t, h, "25")
	p := out["freeGoods"].([]any)[0].(map[string]any)
	if p["pricedQuantity"] != "25" || p["freeQuantity"] != "2" || out["orderNet"] != "225" {
		t.Fatalf("exclusive proposal = %v order net %v; want 25 charged, 2 extra", p, out["orderNet"])
	}
	code, out := call(t, h, http.MethodPost, "/v1/pricing/free-goods-rules", campaignDuties, fmt.Sprintf(freeGoodsBody, legalEntity, "INCLUSIVE", "WHOLE_UNITS"), "Idempotency-Key", "fg-y")
	if code != http.StatusConflict || out["code"] != "PRC-0002" {
		t.Fatalf("overlapping rule = %d %v; want 409 PRC-0002", code, out)
	}
}

func TestFreeGoods_ValidationAndDuties(t *testing.T) {
	h := router(newFake())
	bad := `{"legalEntityId":"` + legalEntity + `","customerKind":"all","productId":"P1","unit":"EA","kind":"INCLUSIVE","calcRule":"WHOLE_UNITS",` +
		`"everyQuantity":"10","freeQuantity":"10","validFrom":"2026-02-01","validTo":"2026-01-01"}`
	code, out := call(t, h, http.MethodPost, "/v1/pricing/free-goods-rules", campaignDuties, bad)
	if code != http.StatusUnprocessableEntity {
		t.Fatalf("invalid rule = %d %v; want 422", code, out)
	}
	errs := out["errors"].(map[string]any)
	if errs["freeQuantity"] == nil || errs["validity"] == nil {
		t.Fatalf("field errors = %v", errs)
	}
	if code, _ := call(t, h, http.MethodPost, "/v1/pricing/free-goods-rules", "pricing.view", fmt.Sprintf(freeGoodsBody, legalEntity, "INCLUSIVE", "WHOLE_UNITS")); code != http.StatusForbidden {
		t.Fatalf("create without maintain = %d; want 403", code)
	}
	if code, _ := call(t, h, http.MethodGet, "/v1/pricing/free-goods-rules/00000000-0000-0000-0011-999999999999", "pricing.view", ""); code != http.StatusNotFound {
		t.Fatalf("unknown rule = %d; want 404", code)
	}
	if code, _ := call(t, h, http.MethodGet, "/v1/pricing/free-goods-rules/not-a-uuid", "pricing.view", ""); code != http.StatusNotFound {
		t.Fatalf("malformed id = %d; want 404", code)
	}
	created := createFreeGoods(t, h, "INCLUSIVE", "PROPORTIONAL", "fg-v")
	code, list := call(t, h, http.MethodGet, "/v1/pricing/free-goods-rules?legalEntityId="+legalEntity+"&productId=P1", "pricing.view", "")
	if code != http.StatusOK || len(list["items"].([]any)) != 1 {
		t.Fatalf("list = %d %v", code, list)
	}
	again := createFreeGoods(t, h, "INCLUSIVE", "PROPORTIONAL", "fg-v")
	if again["id"] != created["id"] {
		t.Fatalf("replayed create id %v, want %v", again["id"], created["id"])
	}
}

// An omitted calcRule takes the PRD default of free_goods_calc_rule, WHOLE_UNITS (§5.26.13).
func TestFreeGoods_CalcRuleDefaultsToWholeUnits(t *testing.T) {
	h := router(newFake())
	body := `{"legalEntityId":"` + legalEntity + `","customerKind":"all","productId":"P1","unit":"EA","kind":"INCLUSIVE",` +
		`"everyQuantity":"10","freeQuantity":"1","validFrom":"2026-01-01","validTo":"2026-12-31"}`
	code, out := call(t, h, http.MethodPost, "/v1/pricing/free-goods-rules", campaignDuties, body)
	if code != http.StatusCreated || out["calcRule"] != "WHOLE_UNITS" || out["minQuantity"] != "0" {
		t.Fatalf("create without calcRule = %d %v; want WHOLE_UNITS, minimum 0", code, out)
	}
}
