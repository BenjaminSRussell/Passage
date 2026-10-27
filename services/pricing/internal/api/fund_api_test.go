package api_test

import (
	"fmt"
	"net/http"
	"strings"
	"testing"
)

const (
	fundDuties = "pricing.fund.maintain"
	fundBody   = `{"legalEntityId":"%s","name":"Spring TPM","type":"TRADE","currency":"USD","owner":"tm-lead","budgetPerPeriod":"1000","customerScope":"ALL"}`
	noFund     = "00000000-0000-0000-0009-999999999999"
)

func createFund(t *testing.T, h http.Handler, extra string) string {
	t.Helper()
	body := strings.TrimSuffix(fmt.Sprintf(fundBody, legalEntity), "}") + extra + "}"
	code, out := call(t, h, http.MethodPost, "/v1/pricing/funds", fundDuties, body)
	if code != http.StatusCreated || out["version"] != float64(1) || out["closed"] != false {
		t.Fatalf("create fund = %d %v; want 201 open at version 1", code, out)
	}
	return out["id"].(string)
}

func attribute(t *testing.T, h http.Handler, id, period, source, amount string, headers ...string) (int, map[string]any) {
	t.Helper()
	body := fmt.Sprintf(`{"period":"%s","source":"%s","reference":"INV-1","amount":"%s"}`, period, source, amount)
	return call(t, h, http.MethodPost, "/v1/pricing/funds/"+id+":attribute", fundDuties, body, headers...)
}

func fundCommand(t *testing.T, h http.Handler, id, verb, body string) (int, map[string]any) {
	t.Helper()
	return call(t, h, http.MethodPost, "/v1/pricing/funds/"+id+":"+verb, fundDuties, body)
}

func TestFund_AttributionTracksRemainingAndRejectsOverBudget(t *testing.T) {
	h := router(newFake())
	id := createFund(t, h, "")
	code, out := attribute(t, h, id, "2027-03", "CLAIM", "600")
	if code != http.StatusCreated || out["remaining"] != "400" || out["used"] != "600" || out["budget"] != "1000" {
		t.Fatalf("attribution = %d %v; want 201 used 600 remaining 400", code, out)
	}
	code, out = attribute(t, h, id, "2027-03", "REBATE_ACCRUAL", "500")
	if code != http.StatusUnprocessableEntity || out["code"] != "PRC-0012" || !strings.Contains(out["detail"].(string), "exceeded by 100.00") {
		t.Fatalf("over budget = %d %v; want 422 PRC-0012 excess 100.00", code, out)
	}
	if code, out := attribute(t, h, id, "2027-04", "CLAIM", "500"); code != http.StatusCreated || out["remaining"] != "500" {
		t.Fatalf("next period has its own budget: %d %v", code, out)
	}
}

func TestFund_ReplayedAttributionAddsNothing(t *testing.T) {
	h := router(newFake())
	id := createFund(t, h, "")
	for i := 0; i < 2; i++ {
		if code, out := attribute(t, h, id, "2027-03", "CLAIM", "600", "Idempotency-Key", "use-1"); code != http.StatusCreated || out["used"] != "600" {
			t.Fatalf("attempt %d = %d %v; want used 600 both times", i, code, out)
		}
	}
	if code, out := attribute(t, h, id, "2027-03", "CLAIM", "400"); code != http.StatusCreated || out["remaining"] != "0" {
		t.Fatalf("budget must still have 400 left after the replay: %d %v", code, out)
	}
}

func TestFund_OverspendAllowedWhenFundPermits(t *testing.T) {
	h := router(newFake())
	id := createFund(t, h, `,"allowOverspend":true`)
	if code, out := attribute(t, h, id, "2027-03", "CLAIM", "1500"); code != http.StatusCreated || out["remaining"] != "-500" {
		t.Fatalf("overspend = %d %v; want 201 remaining -500", code, out)
	}
}

func TestFund_CloseFreezesAttributionAndReopenRestoresIt(t *testing.T) {
	h := router(newFake())
	id := createFund(t, h, "")
	if code, out := fundCommand(t, h, id, "close", `{"version":2}`); code != http.StatusPreconditionFailed || out["code"] != "conflict_version" {
		t.Fatalf("stale version = %d %v; want 412 conflict_version", code, out)
	}
	if code, out := fundCommand(t, h, id, "close", `{}`); code != http.StatusUnprocessableEntity {
		t.Fatalf("missing version = %d %v; want 422", code, out)
	}
	code, out := fundCommand(t, h, id, "close", `{"version":1}`)
	if code != http.StatusOK || out["closed"] != true || out["version"] != float64(2) {
		t.Fatalf("close = %d %v; want closed at version 2", code, out)
	}
	if code, out := attribute(t, h, id, "2027-03", "CLAIM", "10"); code != http.StatusConflict || out["code"] != "PRC-0011" {
		t.Fatalf("closed fund = %d %v; want 409 PRC-0011", code, out)
	}
	if code, out := fundCommand(t, h, id, "close", `{"version":2}`); code != http.StatusConflict || out["code"] != "PRC-0031" {
		t.Fatalf("second close = %d %v; want 409 PRC-0031", code, out)
	}
	if code, out := fundCommand(t, h, id, "reopen", `{"version":2}`); code != http.StatusOK || out["closed"] != false {
		t.Fatalf("reopen = %d %v", code, out)
	}
	if code, _ := attribute(t, h, id, "2027-03", "CLAIM", "10"); code != http.StatusCreated {
		t.Fatalf("a reopened fund takes usage again, got %d", code)
	}
}

func TestFund_ReviseBudgetIsTheOverBudgetRemedy(t *testing.T) {
	h := router(newFake())
	id := createFund(t, h, "")
	attribute(t, h, id, "2027-03", "CLAIM", "800")
	if code, out := fundCommand(t, h, id, "revise", `{"version":1,"budgetPerPeriod":"700"}`); code != http.StatusUnprocessableEntity || out["code"] != "PRC-0012" {
		t.Fatalf("budget below usage = %d %v; want 422 PRC-0012", code, out)
	}
	code, out := fundCommand(t, h, id, "revise", `{"version":1,"budgetPerPeriod":"1500"}`)
	if code != http.StatusOK || out["budgetPerPeriod"] != "1500" || out["version"] != float64(2) {
		t.Fatalf("raise budget = %d %v", code, out)
	}
	if code, out := attribute(t, h, id, "2027-03", "CLAIM", "700"); code != http.StatusCreated || out["remaining"] != "0" {
		t.Fatalf("after the raise = %d %v; want remaining 0", code, out)
	}
	if code, _ := fundCommand(t, h, id, "revise", `{"version":1,"budgetPerPeriod":"2000"}`); code != http.StatusPreconditionFailed {
		t.Fatalf("stale revise must be 412, got %d", code)
	}
	if code, out := fundCommand(t, h, id, "revise", `{"version":2,"budgetPerPeriod":"-1"}`); code != http.StatusUnprocessableEntity || out["errors"] == nil {
		t.Fatalf("negative budget = %d %v; want 422 with field errors", code, out)
	}
}

func TestFund_UsageGroupsByPeriodAndSource(t *testing.T) {
	h := router(newFake())
	id := createFund(t, h, "")
	attribute(t, h, id, "2027-03", "CLAIM", "100")
	attribute(t, h, id, "2027-03", "CLAIM", "50")
	attribute(t, h, id, "2027-03", "DEDUCTION", "25")
	code, out := call(t, h, http.MethodGet, "/v1/pricing/funds/"+id+"/usage", "pricing.view", "")
	if code != http.StatusOK {
		t.Fatalf("usage status %d: %v", code, out)
	}
	items := out["items"].([]any)
	first, second := items[0].(map[string]any), items[1].(map[string]any)
	if len(items) != 2 || first["source"] != "CLAIM" || first["amount"] != "150" || second["source"] != "DEDUCTION" || second["amount"] != "25" {
		t.Fatalf("usage items = %v; want CLAIM 150 then DEDUCTION 25", items)
	}
	periods := out["periods"].([]any)
	if len(periods) != 1 || periods[0].(map[string]any)["used"] != "175" || periods[0].(map[string]any)["remaining"] != "825" {
		t.Fatalf("periods = %v; want one period used 175 remaining 825", periods)
	}
	if out["fund"].(map[string]any)["id"] != id {
		t.Fatalf("usage must carry the fund: %v", out["fund"])
	}
}

func TestFund_ValidationDutiesAndNotFound(t *testing.T) {
	h := router(newFake())
	id := createFund(t, h, "")
	code, out := call(t, h, http.MethodPost, "/v1/pricing/funds/"+id+":attribute", fundDuties,
		`{"period":"2027-13","source":"OTHER","reference":"x","amount":"0"}`)
	errs, _ := out["errors"].(map[string]any)
	if code != http.StatusUnprocessableEntity || errs["period"] == nil || errs["source"] == nil || errs["amount"] == nil {
		t.Fatalf("bad period, source and amount = %d %v; want 422 naming all three", code, out)
	}
	if code, _ := call(t, h, http.MethodPost, "/v1/pricing/funds", "pricing.view", fmt.Sprintf(fundBody, legalEntity)); code != http.StatusForbidden {
		t.Fatalf("a viewer must not create funds, got %d", code)
	}
	if code, _ := call(t, h, http.MethodGet, "/v1/pricing/funds", "pricing.view", ""); code != http.StatusOK {
		t.Fatalf("PricingView reads funds, got %d", code)
	}
	if code, _ := call(t, h, http.MethodGet, "/v1/pricing/funds", "pricing.rebate.view", ""); code != http.StatusForbidden {
		t.Fatalf("an unrelated duty must not read funds, got %d", code)
	}
	if code, _ := call(t, h, http.MethodPost, "/v1/pricing/funds", fundDuties, `{"legalEntityId":"x","name":"","currency":"usd"}`); code != http.StatusUnprocessableEntity {
		t.Fatalf("invalid fund must be 422, got %d", code)
	}
	if code, _ := call(t, h, http.MethodGet, "/v1/pricing/funds/not-a-uuid", fundDuties, ""); code != http.StatusNotFound {
		t.Fatalf("malformed id must be 404, got %d", code)
	}
	if code, out := call(t, h, http.MethodGet, "/v1/pricing/funds/"+noFund, fundDuties, ""); code != http.StatusNotFound || out["code"] != "PRC-0039" {
		t.Fatalf("missing fund = %d %v; want 404 PRC-0039", code, out)
	}
	if code, out := call(t, h, http.MethodGet, "/v1/pricing/funds/"+noFund+"/usage", fundDuties, ""); code != http.StatusNotFound || out["code"] != "PRC-0039" {
		t.Fatalf("usage of a missing fund = %d %v; want 404 PRC-0039", code, out)
	}
}

func promoBody(weeks int, weekly, fundID, currency string) string {
	items := make([]string, weeks)
	for i := range items {
		items[i] = `"` + weekly + `"`
	}
	fund := ""
	if fundID != "" {
		fund = `"fundId":"` + fundID + `",`
	}
	return fmt.Sprintf(`{"legalEntityId":"%s",%s"name":"Spring push","eventCategory":"FEATURE","eventType":"TPR","productId":"P-1","currency":"%s","startDate":"2027-03-01","endDate":"2027-03-28","baselineWeeklyUnits":[%s],"liftPct":"25","plannedSpend":"500"}`,
		legalEntity, fund, currency, strings.Join(items, ","))
}

func TestPromotion_BaselineForecastAndEffectiveness(t *testing.T) {
	h := router(newFake())
	code, out := call(t, h, http.MethodPost, "/v1/pricing/promotions", fundDuties, promoBody(13, "10", "", "USD"))
	// 4 weeks × baseline 10 × (1 + 25%) = 50.
	if code != http.StatusCreated || out["baseline"] != "10" || out["forecastUnits"] != "50" || out["eventType"] != "TPR" {
		t.Fatalf("create promotion = %d %v; want baseline 10 forecast 50", code, out)
	}
	if _, has := out["effectivenessPct"]; has {
		t.Fatalf("effectiveness must wait for actuals: %v", out)
	}
	id := out["id"].(string)
	// 240 units over 4 weeks = 60 a week against a baseline of 10: (60 − 10) / 10 = 500%.
	code, out = call(t, h, http.MethodPost, "/v1/pricing/promotions/"+id+":actuals", fundDuties, `{"units":"240","weeks":4,"spend":"480"}`)
	if code != http.StatusOK || out["effectivenessPct"] != "500" {
		t.Fatalf("actuals = %d %v; want effectiveness 500", code, out)
	}
	if code, out := call(t, h, http.MethodPost, "/v1/pricing/promotions/"+id+":actuals", fundDuties, `{"weeks":0}`); code != http.StatusUnprocessableEntity {
		t.Fatalf("missing units and zero weeks = %d %v; want 422", code, out)
	}
}

func TestPromotion_FundMustBeOpenAndMatch(t *testing.T) {
	h := router(newFake())
	if code, _ := call(t, h, http.MethodPost, "/v1/pricing/promotions", fundDuties, promoBody(12, "10", "", "USD")); code != http.StatusUnprocessableEntity {
		t.Fatalf("12-week baseline must be 422, got %d", code)
	}
	if code, out := call(t, h, http.MethodPost, "/v1/pricing/promotions", fundDuties, promoBody(13, "10", noFund, "USD")); code != http.StatusNotFound || out["code"] != "PRC-0039" {
		t.Fatalf("unknown fund = %d %v; want 404 PRC-0039", code, out)
	}
	id := createFund(t, h, "")
	code, out := call(t, h, http.MethodPost, "/v1/pricing/promotions", fundDuties, promoBody(13, "10", id, "EUR"))
	if errs, _ := out["errors"].(map[string]any); code != http.StatusUnprocessableEntity || errs["currency"] == nil {
		t.Fatalf("currency mismatch = %d %v; want 422 on currency", code, out)
	}
	fundCommand(t, h, id, "close", `{"version":1}`)
	if code, out := call(t, h, http.MethodPost, "/v1/pricing/promotions", fundDuties, promoBody(13, "10", id, "USD")); code != http.StatusConflict || out["code"] != "PRC-0011" {
		t.Fatalf("closed fund = %d %v; want 409 PRC-0011", code, out)
	}
}

func TestPromotion_ListAndGet(t *testing.T) {
	h := router(newFake())
	_, out := call(t, h, http.MethodPost, "/v1/pricing/promotions", fundDuties, promoBody(13, "4", "", "USD"))
	id := out["id"].(string)
	if code, got := call(t, h, http.MethodGet, "/v1/pricing/promotions/"+id, "pricing.view", ""); code != http.StatusOK || got["name"] != "Spring push" {
		t.Fatalf("get = %d %v", code, got)
	}
	if code, list := call(t, h, http.MethodGet, "/v1/pricing/promotions", fundDuties, ""); code != http.StatusOK || len(list["items"].([]any)) != 1 {
		t.Fatalf("list = %d %v", code, list)
	}
	if code, got := call(t, h, http.MethodGet, "/v1/pricing/promotions/"+noFund, fundDuties, ""); code != http.StatusNotFound || got["code"] != "PRC-0040" {
		t.Fatalf("missing promotion = %d %v; want 404 PRC-0040", code, got)
	}
}
