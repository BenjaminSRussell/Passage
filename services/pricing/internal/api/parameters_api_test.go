package api_test

import (
	"fmt"
	"net/http"
	"strings"
	"testing"
)

const parametersURL = "/v1/pricing/parameters"

// inEntity scopes a call to the test legal entity, as the gateway's legal-entity claim does.
// allValues are the other values every PUT carries.
const allValues = `"pricingDateBasis":"SHIP_DATE","totalDiscountBasis":"AFTER_LINE_DISC","smartRoundingBands":[{"upTo":"10","ending":"0.99"},{"ending":"0"}],`

var inEntity = []string{"X-Passage-Legal-Entity", legalEntity}

func TestParameters_DefaultsThenSetWithVersion(t *testing.T) {
	h := router(newFake())
	status, out := call(t, h, http.MethodGet, parametersURL+"?legalEntityId="+legalEntity, "pricing.view", "", inEntity...)
	if status != http.StatusOK || out["journalFourEyes"] != false || out["version"] != float64(0) || out["pricingDateBasis"] != "SHIP_DATE" || out["totalDiscountBasis"] != "AFTER_LINE_DISC" || len(out["smartRoundingBands"].([]any)) != 3 {
		t.Fatalf("defaults = %d %v", status, out)
	}
	body := fmt.Sprintf(`{"legalEntityId":"%s","journalFourEyes":true,%s"version":0}`, legalEntity, allValues)
	status, out = call(t, h, http.MethodPut, parametersURL, "pricing.admin", body, inEntity...)
	if status != http.StatusOK || out["journalFourEyes"] != true || out["version"] != float64(1) {
		t.Fatalf("set = %d %v", status, out)
	}
	status, out = call(t, h, http.MethodGet, parametersURL+"?legalEntityId="+legalEntity, "pricing.view", "", inEntity...)
	if status != http.StatusOK || out["journalFourEyes"] != true || out["version"] != float64(1) {
		t.Fatalf("read after set = %d %v", status, out)
	}
	// A second write with the stale version 0 is a version conflict.
	status, out = call(t, h, http.MethodPut, parametersURL, "pricing.admin", body, inEntity...)
	if status != http.StatusPreconditionFailed || out["code"] != "conflict_version" {
		t.Fatalf("stale set = %d %v", status, out)
	}
}

func TestParameters_DutiesScopeAndValidation(t *testing.T) {
	h := router(newFake())
	body := fmt.Sprintf(`{"legalEntityId":"%s","journalFourEyes":true,%s"version":0}`, legalEntity, allValues)
	if status, _ := call(t, h, http.MethodPut, parametersURL, "pricing.view,pricing.maintain,pricing.journal.post", body, inEntity...); status != http.StatusForbidden {
		t.Fatalf("set without pricing.admin = %d, want 403", status)
	}
	if status, _ := call(t, h, http.MethodGet, parametersURL+"?legalEntityId="+legalEntity, "", "", inEntity...); status != http.StatusForbidden {
		t.Fatalf("get without duty = %d, want 403", status)
	}
	// A legal entity outside the caller's scope is refused, not shown as the defaults.
	if status, out := call(t, h, http.MethodGet, parametersURL+"?legalEntityId="+otherEntity, "pricing.view", "", inEntity...); status != http.StatusForbidden || out["code"] != "AUTH-0003" {
		t.Fatalf("get of another legal entity = %d %v, want 403 AUTH-0003", status, out)
	}
	other := fmt.Sprintf(`{"legalEntityId":"%s","journalFourEyes":true,%s"version":0}`, otherEntity, allValues)
	if status, out := call(t, h, http.MethodPut, parametersURL, "pricing.admin", other, inEntity...); status != http.StatusForbidden || out["code"] != "AUTH-0003" {
		t.Fatalf("set of another legal entity = %d %v, want 403 AUTH-0003", status, out)
	}
	if status, _ := call(t, h, http.MethodGet, parametersURL+"?legalEntityId="+legalEntity, "pricing.view", ""); status != http.StatusForbidden {
		t.Fatalf("get without a legal-entity claim = %d, want 403", status)
	}
	if status, _ := call(t, h, http.MethodGet, parametersURL+"?legalEntityId=x", "pricing.view", "", inEntity...); status != http.StatusUnprocessableEntity {
		t.Fatalf("bad legal entity = %d, want 422", status)
	}
	status, out := call(t, h, http.MethodPut, parametersURL, "pricing.admin", `{"legalEntityId":"x","version":-1}`, inEntity...)
	fields, _ := out["errors"].(map[string]any)
	if status != http.StatusUnprocessableEntity || fields["legalEntityId"] == nil || fields["version"] == nil || fields["journalFourEyes"] == nil {
		t.Fatalf("bad body = %d %v, want 422 naming legalEntityId, version and journalFourEyes", status, out)
	}
	// The PUT replaces every value: an omitted journalFourEyes is not saved as false.
	missing := fmt.Sprintf(`{"legalEntityId":"%s","version":0}`, legalEntity)
	if status, _ := call(t, h, http.MethodPut, parametersURL, "pricing.admin", missing, inEntity...); status != http.StatusUnprocessableEntity {
		t.Fatalf("set without journalFourEyes = %d, want 422", status)
	}
}

func TestParameters_BasesAndBandsValidated(t *testing.T) {
	h := router(newFake())
	put := func(extra string) (int, map[string]any) {
		body := fmt.Sprintf(`{"legalEntityId":"%s","journalFourEyes":false,%s"version":0}`, legalEntity, extra)
		return call(t, h, http.MethodPut, parametersURL, "pricing.admin", body, inEntity...)
	}
	bad := map[string]string{
		"pricingDateBasis":             `"pricingDateBasis":"TODAY","totalDiscountBasis":"GROSS","smartRoundingBands":[{"ending":"0"}],`,
		"totalDiscountBasis":           `"pricingDateBasis":"ORDER_DATE","totalDiscountBasis":"NET","smartRoundingBands":[{"ending":"0"}],`,
		"smartRoundingBands[0].upTo":   `"pricingDateBasis":"ORDER_DATE","totalDiscountBasis":"GROSS","smartRoundingBands":[{"upTo":"0","ending":"0.99"},{"ending":"0"}],`,
		"smartRoundingBands[1].upTo":   `"pricingDateBasis":"ORDER_DATE","totalDiscountBasis":"GROSS","smartRoundingBands":[{"upTo":"10","ending":"0.99"},{"upTo":"5","ending":"0.95"},{"ending":"0"}],`,
		"smartRoundingBands[0].ending": `"pricingDateBasis":"ORDER_DATE","totalDiscountBasis":"GROSS","smartRoundingBands":[{"upTo":"10","ending":"1.5"},{"ending":"0"}],`,
	}
	for field, extra := range bad {
		status, out := put(extra)
		fields, _ := out["errors"].(map[string]any)
		if status != http.StatusUnprocessableEntity || fields[field] == nil {
			t.Fatalf("%s: %d %v, want 422 naming it", field, status, out)
		}
	}
	status, out := put(`"pricingDateBasis":"DELIVERY_DATE","totalDiscountBasis":"GROSS","smartRoundingBands":[{"upTo":"20","ending":"0.5"},{"ending":"0"}],`)
	if status != http.StatusOK || out["pricingDateBasis"] != "DELIVERY_DATE" || out["totalDiscountBasis"] != "GROSS" || len(out["smartRoundingBands"].([]any)) != 2 {
		t.Fatalf("valid set = %d %v", status, out)
	}
}

const basisResolve = `{"legalEntityId":"%s","customerId":"C1","currency":"USD",%s"lines":[{"productId":"P1","unit":"EA","quantity":"1"}]}`

// lineError is the error code of the first priced line ("" when it priced).
func lineError(out map[string]any) any {
	return out["lines"].([]any)[0].(map[string]any)["errorCode"]
}

func TestResolve_PricingDateFollowsTheBasis(t *testing.T) {
	h := router(newFake())
	_, created := call(t, h, http.MethodPost, "/v1/pricing/condition-records", allDuties, fmt.Sprintf(recordBody, legalEntity, "95.00"))
	call(t, h, http.MethodPost, "/v1/pricing/condition-records/"+created["id"].(string)+":activate", allDuties, `{"version":1}`)
	resolve := func(dates string) (int, map[string]any) {
		return call(t, h, http.MethodPost, "/v1/pricing/resolve", allDuties, fmt.Sprintf(basisResolve, legalEntity, dates))
	}
	// The default basis is the ship date.
	if status, out := resolve(`"shipDate":"2026-06-01","orderDate":"2027-06-01",`); status != http.StatusOK || lineError(out) != nil {
		t.Fatalf("ship date basis = %d %v; want the line priced on the ship date", status, out)
	}
	status, out := resolve(`"orderDate":"2026-06-01",`)
	fields, _ := out["errors"].(map[string]any)
	if status != http.StatusUnprocessableEntity || fields["pricingDate"] == nil || !strings.Contains(fields["pricingDate"].(string), "SHIP_DATE") {
		t.Fatalf("no ship date = %d %v; want 422 naming the basis", status, out)
	}
	put := fmt.Sprintf(`{"legalEntityId":"%s","journalFourEyes":false,"pricingDateBasis":"ORDER_DATE","totalDiscountBasis":"AFTER_LINE_DISC","smartRoundingBands":[{"ending":"0"}],"version":0}`, legalEntity)
	if status, out := call(t, h, http.MethodPut, parametersURL, "pricing.admin", put, inEntity...); status != http.StatusOK {
		t.Fatalf("set = %d %v", status, out)
	}
	if status, out := resolve(`"shipDate":"2026-06-01","orderDate":"2027-06-01",`); status != http.StatusOK || lineError(out) != "PRC-0001" {
		t.Fatalf("order date basis = %d %v; want no price on the order date 2027-06-01", status, out)
	}
	if status, out := resolve(`"orderDate":"2026-06-01",`); status != http.StatusOK || lineError(out) != nil || out["pricingDate"] != "2026-06-01" {
		t.Fatalf("order date = %d %v; want priced on 2026-06-01 and that date echoed", status, out)
	}
	// An explicit pricingDate wins over the basis.
	if status, out := resolve(`"pricingDate":"2026-06-01","orderDate":"2027-06-01",`); status != http.StatusOK || lineError(out) != nil {
		t.Fatalf("explicit pricing date = %d %v; want it to win", status, out)
	}
	// A malformed basis date is reported on its own field, not as a missing pricing date.
	if status, out := resolve(`"orderDate":"June 1",`); status != http.StatusUnprocessableEntity || out["errors"].(map[string]any)["orderDate"] == nil || out["errors"].(map[string]any)["pricingDate"] != nil {
		t.Fatalf("bad order date = %d %v; want 422 naming orderDate only", status, out)
	}
	// A malformed date the basis does not use is still refused, beside a valid pricing date.
	if status, out := resolve(`"pricingDate":"2026-06-01","shipDate":"soon",`); status != http.StatusUnprocessableEntity || out["errors"].(map[string]any)["shipDate"] == nil || out["errors"].(map[string]any)["pricingDate"] != nil {
		t.Fatalf("bad ship date = %d %v; want 422 naming shipDate only", status, out)
	}
	// Compare and simulate choose the pricing date the same way.
	sim := fmt.Sprintf(`{"legalEntityId":"%s","customerId":"C1","currency":"USD","orderDate":"2026-06-01","lines":[{"productId":"P1","unit":"EA","quantity":"1"}]}`, legalEntity)
	if status, out := call(t, h, http.MethodPost, "/v1/pricing/simulate?explain=false", allDuties, sim); status != http.StatusOK || out["pricingDate"] != "2026-06-01" {
		t.Fatalf("simulate on the order date = %d %v", status, out)
	}
	// Compare takes the order date too: it gets past the pricing date to look up the (unknown)
	// procedure versions.
	cmp := fmt.Sprintf(`{"legalEntityId":"%s","customerId":"C1","currency":"USD","orderDate":"2026-06-01","procedureIdA":"%s","procedureIdB":"%s","lines":[{"productId":"P1","unit":"EA","quantity":"1"}]}`,
		legalEntity, otherEntity, legalEntity)
	if status, out := call(t, h, http.MethodPost, "/v1/pricing/compare", allDuties, cmp); status == http.StatusUnprocessableEntity {
		t.Fatalf("compare on the order date = %d %v; want the pricing date taken from orderDate", status, out)
	}
}

func TestSimulate_TotalDiscountBasisFromTheLegalEntity(t *testing.T) {
	h := router(newFake())
	body := fmt.Sprintf(`{"legalEntityId":"%s","customerId":"C1","currency":"USD","pricingDate":"2026-06-01",
		"lines":[{"productId":"P1","unit":"EA","quantity":"2"}],
		"records":[
			{"id":"p","type":"PRICE","customerKind":"customer","customerKey":"C1","productKind":"product","productKey":"P1","currency":"USD","amount":"10.00","validFrom":"2026-01-01","validTo":"2026-12-31"},
			{"id":"l","type":"LINE_DISC","customerKind":"customer","customerKey":"C1","productKind":"all","currency":"USD","discPct1":"10","validFrom":"2026-01-01","validTo":"2026-12-31"},
			{"id":"t","type":"TOTAL_DISC","customerKind":"customer","customerKey":"C1","productKind":"all","currency":"USD","discPct1":"10","validFrom":"2026-01-01","validTo":"2026-12-31"}]}`, legalEntity)
	_, out := call(t, h, http.MethodPost, "/v1/pricing/simulate?explain=false", allDuties, body)
	if out["totalDiscount"] != "1.8" {
		t.Fatalf("after line discounts: total discount = %v; want 1.8", out["totalDiscount"])
	}
	put := fmt.Sprintf(`{"legalEntityId":"%s","journalFourEyes":false,"pricingDateBasis":"SHIP_DATE","totalDiscountBasis":"GROSS","smartRoundingBands":[{"ending":"0"}],"version":0}`, legalEntity)
	if status, out := call(t, h, http.MethodPut, parametersURL, "pricing.admin", put, inEntity...); status != http.StatusOK {
		t.Fatalf("set = %d %v", status, out)
	}
	after := out["explainRef"]
	_, out = call(t, h, http.MethodPost, "/v1/pricing/simulate?explain=false", allDuties, body)
	if out["totalDiscount"] != "2" || out["orderNet"] != "16" {
		t.Fatalf("gross: total discount %v net %v; want 2 and 16", out["totalDiscount"], out["orderNet"])
	}
	// The explanation says the basis, so the two answers do not share a content address.
	if out["explainRef"] == after {
		t.Fatalf("explainRef %v is the same under both bases", after)
	}
	_, out = call(t, h, http.MethodPost, "/v1/pricing/simulate", allDuties, body)
	var basis any
	for _, st := range out["trace"].([]any) {
		if st.(map[string]any)["condition"] == "TOTAL_DISC" {
			basis = st.(map[string]any)["basis"]
		}
	}
	if basis != "GROSS" {
		t.Fatalf("TOTAL_DISC trace basis = %v; want GROSS", basis)
	}
}
