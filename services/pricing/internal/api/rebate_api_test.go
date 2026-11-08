package api_test

import (
	"fmt"
	"net/http"
	"testing"
)

const otherLegalEntity = "00000000-0000-0000-0007-000000000002"

func agreementBody(le, name string) string {
	return `{"legalEntityId":"` + le + `","name":"` + name + `","customerId":"00000000-0000-0000-0005-000000000001",
		"basis":"NET_AMOUNT","period":"QUARTER","tiers":[{"from":"0","ratePct":"1"},{"from":"50000","ratePct":"2.5"}],"currency":"USD",
		"validFrom":"2026-01-01","validTo":"2026-12-31"}`
}

func TestRebate_ListAgreementsFiltersByLegalEntity(t *testing.T) {
	h := router(newFake())
	for _, name := range []string{"Quarterly A", "Quarterly B"} {
		if code, out := call(t, h, http.MethodPost, "/v1/pricing/rebates/agreements", allDuties, agreementBody(legalEntity, name)); code != http.StatusCreated {
			t.Fatalf("create %s = %d %v", name, code, out)
		}
	}
	if code, out := call(t, h, http.MethodPost, "/v1/pricing/rebates/agreements", allDuties, agreementBody(otherLegalEntity, "Elsewhere")); code != http.StatusCreated {
		t.Fatalf("create other = %d %v", code, out)
	}

	code, list := call(t, h, http.MethodGet, "/v1/pricing/rebates/agreements?legalEntityId="+legalEntity, allDuties, "")
	items, _ := list["items"].([]any)
	if code != http.StatusOK || len(items) != 2 {
		t.Fatalf("list for %s = %d %v; want two agreements", legalEntity, code, list)
	}
	first := items[0].(map[string]any)
	if first["status"] != "ACTIVE" || first["period"] != "QUARTER" || first["accrualAccount"] != "2150" {
		t.Fatalf("agreement view = %v", first)
	}
	if tiers, _ := first["tiers"].([]any); len(tiers) != 2 {
		t.Fatalf("tiers = %v; want two", first["tiers"])
	}

	code, all := call(t, h, http.MethodGet, "/v1/pricing/rebates/agreements", allDuties, "")
	if code != http.StatusOK || len(all["items"].([]any)) != 3 {
		t.Fatalf("unfiltered list = %d %v; want three", code, all)
	}
}

func TestRebate_ListAgreementsRejectsBadFilterAndNeedsDuty(t *testing.T) {
	h := router(newFake())
	if code, out := call(t, h, http.MethodGet, "/v1/pricing/rebates/agreements?legalEntityId=nope", allDuties, ""); code != http.StatusUnprocessableEntity {
		t.Fatalf("bad legalEntityId must be 422, got %d %v", code, out)
	}
	if code, out := call(t, h, http.MethodGet, fmt.Sprintf("/v1/pricing/rebates/agreements?legalEntityId=%s", legalEntity), "pricing.view", ""); code != http.StatusForbidden {
		t.Fatalf("pricing.view alone must not list rebates, got %d %v", code, out)
	}
}

func TestRebate_ReadAgreementAndTierCrossingOverHTTP(t *testing.T) {
	// BR-26.8 over HTTP: 3,000 at 1% accrues 30; the next 3,000 crosses 5,000 and the period
	// is recomputed at 2.5% (150), so that invoice accrues the 120 delta.
	h := router(newFake())
	body := `{"legalEntityId":"` + legalEntity + `","name":"Tiered","customerGroupId":"00000000-0000-0000-0005-000000000009",
		"basis":"NET_AMOUNT","period":"QUARTER","tiers":[{"from":"0","ratePct":"1"},{"from":"5000","ratePct":"2.5"}],"currency":"USD",
		"validFrom":"2026-01-01","validTo":"2026-12-31"}`
	_, created := call(t, h, http.MethodPost, "/v1/pricing/rebates/agreements", allDuties, body)
	aid := created["id"].(string)

	code, got := call(t, h, http.MethodGet, "/v1/pricing/rebates/agreements/"+aid, "pricing.rebate.view", "")
	if code != http.StatusOK || got["customerGroupId"] != "00000000-0000-0000-0005-000000000009" || got["expenseAccount"] != "4150" {
		t.Fatalf("read = %d %v", code, got)
	}
	if code, _ := call(t, h, http.MethodGet, "/v1/pricing/rebates/agreements/00000000-0000-0000-0005-999999999999", allDuties, ""); code != http.StatusNotFound {
		t.Fatalf("unknown agreement must be 404, got %d", code)
	}

	accrue := func(line, date, value string) map[string]any {
		_, out := call(t, h, http.MethodPost, "/v1/pricing/rebates/accruals", allDuties,
			fmt.Sprintf(`{"agreementId":"%s","invoiceLineId":"%s","invoiceDate":"%s","value":"%s","currency":"USD"}`, aid, line, date, value))
		return out
	}
	if first := accrue("L1", "2026-04-02", "3000"); first["amount"] != "30" || first["periodId"] != "2026-Q2" {
		t.Fatalf("first accrual = %v; want 30 in 2026-Q2", first)
	}
	if second := accrue("L2", "2026-05-10", "3000"); second["amount"] != "120" {
		t.Fatalf("tier-crossing accrual = %v; want the 120 delta", second)
	}
	if code, out := call(t, h, http.MethodPost, "/v1/pricing/rebates/accruals", allDuties,
		fmt.Sprintf(`{"agreementId":"%s","invoiceLineId":"L2","invoiceDate":"2026-05-10","value":"9999","currency":"USD"}`, aid)); code != http.StatusUnprocessableEntity {
		t.Fatalf("a redelivered line with another value must be 422, got %d %v", code, out)
	}
	if code, out := call(t, h, http.MethodGet, "/v1/pricing/rebates/accruals?period=2026-13", allDuties, ""); code != http.StatusUnprocessableEntity {
		t.Fatalf("a malformed period must be 422, got %d %v", code, out)
	}
}

func TestRebate_ProductScopeAccruesOnlyMatchingLines(t *testing.T) {
	const product, otherProduct, group = "00000000-0000-0000-0008-000000000001", "00000000-0000-0000-0008-000000000002", "00000000-0000-0000-0009-000000000001"
	h := router(newFake())
	body := `{"legalEntityId":"` + legalEntity + `","name":"Group only","customerId":"00000000-0000-0000-0005-000000000001","productGroupId":"` + group + `",
		"basis":"NET_AMOUNT","period":"MONTH","tiers":[{"from":"0","ratePct":"2"}],"currency":"USD","validFrom":"2026-01-01","validTo":"2026-12-31"}`
	code, created := call(t, h, http.MethodPost, "/v1/pricing/rebates/agreements", allDuties, body)
	if code != http.StatusCreated || created["productGroupId"] != group {
		t.Fatalf("create = %d %v; want 201 with productGroupId", code, created)
	}
	aid := created["id"].(string)
	line := func(id, extra string) (int, map[string]any) {
		return call(t, h, http.MethodPost, "/v1/pricing/rebates/accruals", allDuties,
			fmt.Sprintf(`{"agreementId":"%s","invoiceLineId":"%s","invoiceDate":"2026-10-05","value":"10000","currency":"USD"%s}`, aid, id, extra))
	}
	if code, out := line("L1", `,"productId":"`+product+`","productGroupId":"`+group+`"`); code != http.StatusCreated || out["amount"] != "200" {
		t.Fatalf("in-scope line = %d %v; want 201 with 200", code, out)
	}
	for name, extra := range map[string]string{"other group": `,"productGroupId":"` + product + `"`, "no keys": ``} {
		if code, out := line("L-"+name, extra); code != http.StatusUnprocessableEntity || out["code"] != "PRC-0070" {
			t.Fatalf("%s = %d %v; want 422 PRC-0070", name, code, out)
		}
	}
	if code, out := line("L2", `,"productId":"nope"`); code != http.StatusUnprocessableEntity {
		t.Fatalf("bad productId = %d %v; want 422", code, out)
	}
	both := `{"legalEntityId":"` + legalEntity + `","name":"Both","customerId":"00000000-0000-0000-0005-000000000001","productId":"` + otherProduct + `","productGroupId":"` + group + `",
		"basis":"NET_AMOUNT","period":"MONTH","tiers":[{"from":"0","ratePct":"2"}],"currency":"USD","validFrom":"2026-01-01","validTo":"2026-12-31"}`
	if code, out := call(t, h, http.MethodPost, "/v1/pricing/rebates/agreements", allDuties, both); code != http.StatusUnprocessableEntity {
		t.Fatalf("both scopes = %d %v; want 422", code, out)
	}
}

// The vendor feed honours the product scope too (FR-26.15 with FR-26.11): a vendor agreement for
// one product accrues that product's vendor invoice line and refuses one without a product.
func TestRebate_VendorAccrualRespectsProductScope(t *testing.T) {
	const product = "00000000-0000-0000-000a-000000000001"
	h := router(newFake())
	code, created := call(t, h, http.MethodPost, "/v1/pricing/rebates/agreements", allDuties, vendorAgreementBody(`,"productId":"`+product+`"`))
	if code != http.StatusCreated || created["productId"] != product {
		t.Fatalf("create = %d %v; want 201 with productId", code, created)
	}
	aid := created["id"].(string)
	if code, out := call(t, h, http.MethodPost, "/v1/pricing/rebates/vendor-accruals", allDuties, invoiceLine(aid, "VI-1/1", "2027-03-10", "20000.00")); code != http.StatusUnprocessableEntity || out["code"] != "PRC-0070" {
		t.Fatalf("line without a product = %d %v; want 422 PRC-0070", code, out)
	}
	scoped := fmt.Sprintf(`{"agreementId":"%s","invoiceLineId":"VI-1/2","invoiceDate":"2027-03-10","value":"20000.00","currency":"USD","productId":"%s"}`, aid, product)
	if code, out := call(t, h, http.MethodPost, "/v1/pricing/rebates/vendor-accruals", allDuties, scoped); code != http.StatusCreated || out["amount"] != "600" {
		t.Fatalf("line of the product = %d %v; want 201 with 600", code, out)
	}
}
