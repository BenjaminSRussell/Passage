package api_test

import (
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"
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

const reviseBody = `{"version":%d,"calcRule":"%s","everyQuantity":"%s","freeQuantity":"%s","minQuantity":"%s","validFrom":"%s","validTo":"%s"}`

func reviseFreeGoods(t *testing.T, h http.Handler, id string, body string, headers ...string) (int, map[string]any) {
	t.Helper()
	return call(t, h, http.MethodPost, "/v1/pricing/free-goods-rules/"+id+":revise", campaignDuties, body, headers...)
}

// fromToday is the day n days from today (UTC), as the revise compares against today.
func fromToday(n int) string { return time.Now().UTC().AddDate(0, 0, n).Format("2006-01-02") }

// createFreeGoodsOn creates an INCLUSIVE 1-in-10 WHOLE_UNITS rule for P1 valid from..to.
func createFreeGoodsOn(t *testing.T, h http.Handler, from, to string) string {
	t.Helper()
	body := strings.NewReplacer(`"validFrom":"2026-01-01"`, `"validFrom":"`+from+`"`, `"validTo":"2026-12-31"`, `"validTo":"`+to+`"`).
		Replace(fmt.Sprintf(freeGoodsBody, legalEntity, "INCLUSIVE", "WHOLE_UNITS"))
	code, out := call(t, h, http.MethodPost, "/v1/pricing/free-goods-rules", campaignDuties, body)
	if code != http.StatusCreated {
		t.Fatalf("create free goods rule = %d %v", code, out)
	}
	return out["id"].(string)
}

// A revision of a rule that has not started changes what the next resolve proposes on its
// days (FR-26.22): 1 free in every 10 becomes 2 free in every 5, at the next version.
func TestFreeGoods_ReviseChangesTheProposal(t *testing.T) {
	h := router(newFake())
	rec := strings.NewReplacer(`"validFrom":"2026-01-01"`, `"validFrom":"`+fromToday(0)+`"`, `"validTo":"2026-12-31"`, `"validTo":"`+fromToday(400)+`"`).
		Replace(fmt.Sprintf(campaignRecordBody, legalEntity, "9.00", ""))
	code, r := call(t, h, http.MethodPost, "/v1/pricing/condition-records", allDuties, rec)
	if code != http.StatusCreated {
		t.Fatalf("create record = %d %v", code, r)
	}
	if code, out := call(t, h, http.MethodPost, "/v1/pricing/condition-records/"+r["id"].(string)+":activate", allDuties, `{"version":1}`); code != http.StatusOK {
		t.Fatalf("activate = %d %v", code, out)
	}
	from, to := fromToday(10), fromToday(100)
	id := createFreeGoodsOn(t, h, from, to)
	revised := fmt.Sprintf(reviseBody, 1, "WHOLE_UNITS", "5", "2", "0", from, to)
	code, out := reviseFreeGoods(t, h, id, revised, "Idempotency-Key", "rev-1")
	if code != http.StatusOK || out["version"] != float64(2) || out["everyQuantity"] != "5" || out["freeQuantity"] != "2" ||
		out["kind"] != "INCLUSIVE" || out["productId"] != "P1" {
		t.Fatalf("revise = %d %v", code, out)
	}
	resolve := strings.Replace(fmt.Sprintf(freeResolveBody, legalEntity, "25"), "2026-06-01", fromToday(20), 1)
	code, res := call(t, h, http.MethodPost, "/v1/pricing/resolve", allDuties, resolve)
	if code != http.StatusOK || len(res["freeGoods"].([]any)) != 1 {
		t.Fatalf("resolve after revise = %d %v", code, res)
	}
	if p := res["freeGoods"].([]any)[0].(map[string]any); p["freeQuantity"] != "10" || p["pricedQuantity"] != "15" {
		t.Fatalf("proposal after revise = %v; want 10 free, 15 priced", p)
	}
	// The retry with the same key replays; the same body without it is a stale version.
	if code, again := reviseFreeGoods(t, h, id, revised, "Idempotency-Key", "rev-1"); code != http.StatusOK || again["version"] != float64(2) {
		t.Fatalf("replayed revise = %d %v", code, again)
	}
	if code, _ := reviseFreeGoods(t, h, id, fmt.Sprintf(reviseBody, 1, "WHOLE_UNITS", "4", "2", "0", from, to)); code != http.StatusPreconditionFailed {
		t.Fatalf("stale revise = %d; want 412", code)
	}
}

// A started rule keeps what it proposed on past days (§5.26.4): its offer and start are fixed
// and its end moves only within yesterday and later; ending it is how a planner changes it.
func TestFreeGoods_ReviseKeepsThePast(t *testing.T) {
	h := router(newFake())
	from, to := fromToday(-30), fromToday(30)
	id := createFreeGoodsOn(t, h, from, to)
	code, out := reviseFreeGoods(t, h, id, fmt.Sprintf(reviseBody, 1, "PROPORTIONAL", "5", "2", "0", from, to))
	if code != http.StatusUnprocessableEntity {
		t.Fatalf("offer change on a started rule = %d %v; want 422", code, out)
	}
	for _, f := range []string{"calcRule", "everyQuantity", "freeQuantity"} {
		if out["errors"].(map[string]any)[f] == nil {
			t.Fatalf("offer change errors = %v; want %s named", out["errors"], f)
		}
	}
	if code, out := reviseFreeGoods(t, h, id, fmt.Sprintf(reviseBody, 1, "WHOLE_UNITS", "10", "1", "0", from, fromToday(-10))); code != http.StatusUnprocessableEntity || out["errors"].(map[string]any)["validTo"] == nil {
		t.Fatalf("end before yesterday = %d %v; want 422 naming validTo", code, out)
	}
	if code, out := reviseFreeGoods(t, h, id, fmt.Sprintf(reviseBody, 1, "WHOLE_UNITS", "10", "1", "0", from, fromToday(-1))); code != http.StatusOK || out["validTo"] != fromToday(-1) {
		t.Fatalf("end it yesterday = %d %v; want 200", code, out)
	}
	// The new offer then starts today as another rule, next to the ended one.
	createFreeGoodsOn(t, h, fromToday(0), to)
}

func TestFreeGoods_ReviseRefusals(t *testing.T) {
	h := router(newFake())
	from, to := fromToday(10), fromToday(100)
	id := createFreeGoodsOn(t, h, from, to)
	ok := func(every, free string) string {
		return fmt.Sprintf(reviseBody, 1, "WHOLE_UNITS", every, free, "0", from, to)
	}
	// An inclusive rule must give fewer units than it counts; a missing value is named.
	if code, out := reviseFreeGoods(t, h, id, ok("10", "10")); code != http.StatusUnprocessableEntity || out["errors"].(map[string]any)["freeQuantity"] == nil {
		t.Fatalf("free = every on an inclusive rule = %d %v; want 422 naming freeQuantity", code, out)
	}
	if code, out := reviseFreeGoods(t, h, id, `{"version":1}`); code != http.StatusUnprocessableEntity || out["errors"].(map[string]any)["calcRule"] == nil {
		t.Fatalf("empty body = %d %v; want 422 naming calcRule", code, out)
	}
	if code, _ := reviseFreeGoods(t, h, id, fmt.Sprintf(reviseBody, 1, "ROUNDED", "10", "1", "0", from, to)); code != http.StatusUnprocessableEntity {
		t.Fatalf("unknown calcRule = %d; want 422", code)
	}
	if code, _ := reviseFreeGoods(t, h, id, fmt.Sprintf(reviseBody, 1, "WHOLE_UNITS", "10", "1", "0", to, from)); code != http.StatusUnprocessableEntity {
		t.Fatalf("validTo before validFrom = %d; want 422", code)
	}
	if code, out := reviseFreeGoods(t, h, id, fmt.Sprintf(reviseBody, 1, "WHOLE_UNITS", "5", "1", "0", fromToday(-1), to)); code != http.StatusUnprocessableEntity || out["errors"].(map[string]any)["validFrom"] == nil {
		t.Fatalf("start moved into the past = %d %v; want 422 naming validFrom", code, out)
	}
	if code, _ := reviseFreeGoods(t, h, "00000000-0000-4000-8000-0000000000ff", ok("5", "1")); code != http.StatusNotFound {
		t.Fatalf("unknown rule = %d; want 404", code)
	}
	if code, _ := reviseFreeGoods(t, h, "x", ok("5", "1")); code != http.StatusNotFound && code != http.StatusUnprocessableEntity {
		t.Fatalf("bad id = %d; want 404 or 422", code)
	}
	if code, _ := call(t, h, http.MethodPost, "/v1/pricing/free-goods-rules/"+id+":revise", "pricing.view", ok("5", "1")); code != http.StatusForbidden {
		t.Fatalf("revise with pricing.view only = %d; want 403", code)
	}
	// A neighbour rule's dates cannot be overlapped.
	createFreeGoodsOn(t, h, fromToday(101), fromToday(200))
	if code, _ := reviseFreeGoods(t, h, id, fmt.Sprintf(reviseBody, 1, "WHOLE_UNITS", "10", "1", "0", from, fromToday(150))); code != http.StatusConflict {
		t.Fatalf("overlapping revise = %d; want 409", code)
	}
	// A withdrawn rule cannot be revised.
	if code, _ := call(t, h, http.MethodPost, "/v1/pricing/free-goods-rules/"+id+":withdraw", campaignDuties, `{"version":1}`); code != http.StatusOK {
		t.Fatalf("withdraw = %d", code)
	}
	if code, _ := reviseFreeGoods(t, h, id, fmt.Sprintf(reviseBody, 2, "WHOLE_UNITS", "5", "1", "0", from, to)); code != http.StatusConflict {
		t.Fatalf("revise of a withdrawn rule = %d; want 409", code)
	}
}

// A create that names no calcRule takes the legal entity's free_goods_calc_rule (§5.26.13).
func TestFreeGoods_CalcRuleDefaultFollowsTheParameter(t *testing.T) {
	h := router(newFake())
	put := fmt.Sprintf(`{"legalEntityId":"%s","journalFourEyes":false,%s"version":0}`, legalEntity,
		strings.Replace(allValues, `"freeGoodsCalcRule":"WHOLE_UNITS",`, `"freeGoodsCalcRule":"PROPORTIONAL",`, 1))
	if status, out := call(t, h, http.MethodPut, parametersURL, "pricing.admin", put, inEntity...); status != http.StatusOK || out["freeGoodsCalcRule"] != "PROPORTIONAL" {
		t.Fatalf("set parameter = %d %v", status, out)
	}
	body := `{"legalEntityId":"` + legalEntity + `","customerKind":"all","productId":"P1","unit":"EA","kind":"INCLUSIVE",` +
		`"everyQuantity":"10","freeQuantity":"1","validFrom":"2026-01-01","validTo":"2026-12-31"}`
	code, out := call(t, h, http.MethodPost, "/v1/pricing/free-goods-rules", campaignDuties, body)
	if code != http.StatusCreated || out["calcRule"] != "PROPORTIONAL" {
		t.Fatalf("create without calcRule = %d %v; want PROPORTIONAL", code, out)
	}
	// An explicit calcRule still wins.
	body = strings.Replace(body, `"unit":"EA"`, `"unit":"BX","calcRule":"UNIT_REFERENCE"`, 1)
	if code, out := call(t, h, http.MethodPost, "/v1/pricing/free-goods-rules", campaignDuties, body); code != http.StatusCreated || out["calcRule"] != "UNIT_REFERENCE" {
		t.Fatalf("create with calcRule = %d %v", code, out)
	}
	// The parameter is validated, and a PUT without it is refused.
	bad := strings.Replace(put, "PROPORTIONAL", "ROUNDED", 1)
	if status, out := call(t, h, http.MethodPut, parametersURL, "pricing.admin", bad, inEntity...); status != http.StatusUnprocessableEntity || out["errors"].(map[string]any)["freeGoodsCalcRule"] == nil {
		t.Fatalf("bad calc rule = %d %v", status, out)
	}
	missing := strings.Replace(put, `"freeGoodsCalcRule":"PROPORTIONAL",`, "", 1)
	if status, out := call(t, h, http.MethodPut, parametersURL, "pricing.admin", missing, inEntity...); status != http.StatusUnprocessableEntity || out["errors"].(map[string]any)["freeGoodsCalcRule"] == nil {
		t.Fatalf("missing calc rule = %d %v", status, out)
	}
}
