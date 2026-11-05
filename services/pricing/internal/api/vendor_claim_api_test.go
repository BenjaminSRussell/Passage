package api_test

import (
	"fmt"
	"net/http"
	"testing"
)

const vendorID = "00000000-0000-0000-0008-000000000001"

func vendorAgreementBody(extra string) string {
	return `{"legalEntityId":"` + legalEntity + `","name":"Supplier bill-back","partyRole":"VENDOR","vendorId":"` + vendorID + `",
		"basis":"NET_AMOUNT","period":"MONTH","tiers":[{"from":"0","ratePct":"3"}],"currency":"USD",
		"validFrom":"2027-01-01","validTo":"2027-12-31"` + extra + `}`
}

func invoiceLine(agreementID, line, date, value string) string {
	return fmt.Sprintf(`{"agreementId":"%s","invoiceLineId":"%s","invoiceDate":"%s","value":"%s","currency":"USD"}`, agreementID, line, date, value)
}

func createVendorAgreement(t *testing.T, h http.Handler) string {
	t.Helper()
	code, out := call(t, h, http.MethodPost, "/v1/pricing/rebates/agreements", allDuties, vendorAgreementBody(""))
	if code != http.StatusCreated {
		t.Fatalf("create vendor agreement = %d %v", code, out)
	}
	return out["id"].(string)
}

// AC-26.11 over HTTP: a 3% vendor rebate and a 20,000.00 USD vendor invoice accrue 600.00
// on the vendor feed, the agreement posts Dr 2160 / Cr 5100, and the claim preview shows 600.
func TestVendorRebate_AccrualAndPreview(t *testing.T) {
	h := router(newFake())
	aid := createVendorAgreement(t, h)
	_, got := call(t, h, http.MethodGet, "/v1/pricing/rebates/agreements/"+aid, allDuties, "")
	if got["partyRole"] != "VENDOR" || got["vendorId"] != vendorID || got["accrualAccount"] != "2160" || got["expenseAccount"] != "5100" {
		t.Fatalf("vendor agreement = %v; want VENDOR with 2160 / 5100", got)
	}
	code, acc := call(t, h, http.MethodPost, "/v1/pricing/rebates/vendor-accruals", allDuties, invoiceLine(aid, "VI-1/1", "2027-03-10", "20000.00"))
	if code != http.StatusCreated || acc["amount"] != "600" || acc["periodId"] != "2027-03" {
		t.Fatalf("vendor accrual = %d %v; want 600 in 2027-03", code, acc)
	}
	code, preview := call(t, h, http.MethodGet, "/v1/pricing/rebates/agreements/"+aid+"/claim-preview?period=2027-03", "pricing.rebate.view", "")
	if code != http.StatusOK || preview["amount"] != "600" || preview["accrued"] != "600" || preview["priorClaimed"] != "0" ||
		preview["settlementMode"] != "CREDIT_NOTE" || preview["vendorId"] != vendorID {
		t.Fatalf("claim preview = %d %v; want 600 by CREDIT_NOTE", code, preview)
	}

	// The two invoice feeds never cross: sales invoices do not accrue vendor agreements and vice versa.
	if code, out := call(t, h, http.MethodPost, "/v1/pricing/rebates/accruals", allDuties, invoiceLine(aid, "INV-1/1", "2027-03-10", "100")); code != http.StatusUnprocessableEntity || out["code"] != "PRC-0045" {
		t.Fatalf("sales invoice on a vendor agreement = %d %v; want 422 PRC-0045", code, out)
	}
	_, customer := call(t, h, http.MethodPost, "/v1/pricing/rebates/agreements", allDuties, agreementBody(legalEntity, "Customer"))
	if customer["partyRole"] != "CUSTOMER" || customer["accrualAccount"] != "2150" {
		t.Fatalf("customer agreement = %v; want CUSTOMER with 2150", customer)
	}
	cid := customer["id"].(string)
	if code, out := call(t, h, http.MethodPost, "/v1/pricing/rebates/vendor-accruals", allDuties, invoiceLine(cid, "VI-2/1", "2026-03-10", "100")); code != http.StatusUnprocessableEntity || out["code"] != "PRC-0045" {
		t.Fatalf("vendor invoice on a customer agreement = %d %v; want 422 PRC-0045", code, out)
	}
	if code, out := call(t, h, http.MethodGet, "/v1/pricing/rebates/agreements/"+cid+"/claim-preview?period=2026-03", allDuties, ""); code != http.StatusUnprocessableEntity || out["code"] != "PRC-0045" {
		t.Fatalf("preview on a customer agreement = %d %v; want 422 PRC-0045", code, out)
	}
	if code, out := call(t, h, http.MethodGet, "/v1/pricing/rebates/agreements/"+aid+"/claim-preview?period=March", allDuties, ""); code != http.StatusUnprocessableEntity {
		t.Fatalf("preview with a bad period = %d %v; want 422", code, out)
	}

	_, vendors := call(t, h, http.MethodGet, "/v1/pricing/rebates/agreements?partyRole=VENDOR", allDuties, "")
	if items := vendors["items"].([]any); len(items) != 1 || items[0].(map[string]any)["id"] != aid {
		t.Fatalf("vendor agreements = %v; want only %s", vendors, aid)
	}
	if code, _ := call(t, h, http.MethodGet, "/v1/pricing/rebates/agreements?partyRole=BROKER", allDuties, ""); code != http.StatusUnprocessableEntity {
		t.Fatalf("unknown partyRole filter = %d; want 422", code)
	}
}

func TestVendorRebate_AgreementValidation(t *testing.T) {
	h := router(newFake())
	cases := map[string]struct {
		body, field string
	}{
		"vendor without vendorId":  {`{"legalEntityId":"` + legalEntity + `","name":"x","partyRole":"VENDOR","basis":"NET_AMOUNT","period":"MONTH","tiers":[{"from":"0","ratePct":"3"}],"currency":"USD","validFrom":"2027-01-01","validTo":"2027-12-31"}`, "vendorId"},
		"vendor naming a customer": {vendorAgreementBody(`,"customerId":"00000000-0000-0000-0005-000000000001"`), "customer"},
		"malformed vendorId":       {`{"legalEntityId":"` + legalEntity + `","name":"x","partyRole":"VENDOR","vendorId":"V1","basis":"NET_AMOUNT","period":"MONTH","tiers":[{"from":"0","ratePct":"3"}],"currency":"USD","validFrom":"2027-01-01","validTo":"2027-12-31"}`, "vendorId"},
		"unknown party role":       {`{"legalEntityId":"` + legalEntity + `","name":"x","partyRole":"BROKER","vendorId":"` + vendorID + `","basis":"NET_AMOUNT","period":"MONTH","tiers":[{"from":"0","ratePct":"3"}],"currency":"USD","validFrom":"2027-01-01","validTo":"2027-12-31"}`, "partyRole"},
	}
	for name, c := range cases {
		code, out := call(t, h, http.MethodPost, "/v1/pricing/rebates/agreements", allDuties, c.body)
		errs, _ := out["errors"].(map[string]any)
		if code != http.StatusUnprocessableEntity || errs[c.field] == nil {
			t.Errorf("%s = %d %v; want 422 naming %s", name, code, out, c.field)
		}
	}
}

// BR-26.11: a claim settles the period's accruals less its earlier claims, RC-numbered; a
// fully claimed period is PRC-0015; an over-claimed period produces a negative claim that
// settles by vendor invoice; the creator cannot approve (four eyes).
func TestVendorRebate_ClaimLifecycle(t *testing.T) {
	h := router(newFake())
	aid := createVendorAgreement(t, h)
	accrue := func(line, value string) {
		t.Helper()
		if code, out := call(t, h, http.MethodPost, "/v1/pricing/rebates/vendor-accruals", allDuties, invoiceLine(aid, line, "2027-03-10", value)); code != http.StatusCreated {
			t.Fatalf("accrue %s = %d %v", line, code, out)
		}
	}
	claim := func(user, mode string) (int, map[string]any) {
		t.Helper()
		body := fmt.Sprintf(`{"agreementId":"%s","period":"2027-03","settlementMode":"%s"}`, aid, mode)
		return call(t, h, http.MethodPost, "/v1/pricing/rebates/vendor-claims", allDuties, body, "X-Passage-User", user, "Idempotency-Key", "k-"+user+mode)
	}

	if code, out := claim("u1", ""); code != http.StatusUnprocessableEntity || out["code"] != "PRC-0043" {
		t.Fatalf("claim with nothing accrued = %d %v; want 422 PRC-0043", code, out)
	}
	accrue("VI-1/1", "20000")
	code, first := claim("u1", "")
	if code != http.StatusCreated || first["number"] != "RC-000001" || first["amount"] != "600" || first["status"] != "OPEN" ||
		first["settlementMode"] != "CREDIT_NOTE" || first["vendorId"] != vendorID || first["createdBy"] != "u1" {
		t.Fatalf("first claim = %d %v; want RC-000001 for 600 OPEN by CREDIT_NOTE", code, first)
	}
	if code, out := claim("u1", "PAYMENT_DEDUCTION"); code != http.StatusConflict || out["code"] != "PRC-0015" {
		t.Fatalf("claim of a fully claimed period = %d %v; want 409 PRC-0015", code, out)
	}

	accrue("VI-2/1", "5000")
	code, second := claim("u1", "PAYMENT_DEDUCTION")
	if code != http.StatusCreated || second["number"] != "RC-000002" || second["amount"] != "150" || second["priorClaimed"] != "600" ||
		second["accruedTotal"] != "750" || second["settlementMode"] != "PAYMENT_DEDUCTION" {
		t.Fatalf("second claim = %d %v; want RC-000002 for the 150 difference by PAYMENT_DEDUCTION", code, second)
	}
	accrue("VI-3/1", "-5000")
	code, refund := claim("u3", "PAYMENT_DEDUCTION")
	if code != http.StatusCreated || refund["amount"] != "-150" || refund["settlementMode"] != "VENDOR_INVOICE" {
		t.Fatalf("over-claimed period = %d %v; want -150 settled by VENDOR_INVOICE", code, refund)
	}

	approve := func(id, user, duties string, version int) (int, map[string]any) {
		return call(t, h, http.MethodPost, "/v1/pricing/rebates/vendor-claims/"+id+":approve", duties, fmt.Sprintf(`{"version":%d}`, version), "X-Passage-User", user)
	}
	if code, out := approve(first["id"].(string), "u2", allDuties, 1); code != http.StatusOK || out["status"] != "APPROVED" {
		t.Fatalf("approval of RC-000001 = %d %v", code, out)
	}
	id := second["id"].(string)
	if code, out := approve(id, "u1", allDuties, 1); code != http.StatusForbidden || out["code"] != "PRC-0046" {
		t.Fatalf("approval by the creator = %d %v; want 403 PRC-0046", code, out)
	}
	if code, out := approve(id, "u2", "pricing.rebate.maintain,pricing.rebate.view", 1); code != http.StatusForbidden {
		t.Fatalf("approval without pricing.rebate-claim.approve = %d %v; want 403", code, out)
	}
	if code, out := approve(id, "u2", allDuties, 3); code != http.StatusPreconditionFailed {
		t.Fatalf("approval at a stale version = %d %v; want 412", code, out)
	}
	code, approved := approve(id, "u2", allDuties, 1)
	if code != http.StatusOK || approved["status"] != "APPROVED" || approved["version"] != float64(2) || approved["approvedBy"] != "u2" || approved["approvedAt"] == nil {
		t.Fatalf("approval = %d %v; want APPROVED at version 2 by u2", code, approved)
	}
	if code, out := approve(id, "u2", allDuties, 2); code != http.StatusConflict || out["code"] != "PRC-0031" {
		t.Fatalf("second approval = %d %v; want 409 PRC-0031", code, out)
	}

	_, list := call(t, h, http.MethodGet, "/v1/pricing/rebates/vendor-claims?agreementId="+aid+"&status=OPEN", "pricing.rebate.view", "")
	if items := list["items"].([]any); len(items) != 1 || items[0].(map[string]any)["number"] != "RC-000003" {
		t.Fatalf("open claims = %v; want RC-000003 only (RC-000001 and RC-000002 approved)", list)
	}
	if code, got := call(t, h, http.MethodGet, "/v1/pricing/rebates/vendor-claims/"+id, "pricing.rebate.view", ""); code != http.StatusOK || got["number"] != "RC-000002" {
		t.Fatalf("read claim = %d %v", code, got)
	}
	if code, out := call(t, h, http.MethodGet, "/v1/pricing/rebates/vendor-claims/00000000-0000-0000-0009-999999999999", allDuties, ""); code != http.StatusNotFound || out["code"] != "PRC-0044" {
		t.Fatalf("unknown claim = %d %v; want 404 PRC-0044", code, out)
	}
	if code, _ := call(t, h, http.MethodGet, "/v1/pricing/rebates/vendor-claims?status=SETTLED", allDuties, ""); code != http.StatusUnprocessableEntity {
		t.Fatalf("unknown status filter = %d; want 422", code)
	}
}

func TestVendorRebate_ClaimRequestValidation(t *testing.T) {
	h := router(newFake())
	aid := createVendorAgreement(t, h)
	cases := map[string]string{
		"period":         `{"agreementId":"` + aid + `","period":"2027-13"}`,
		"agreementId":    `{"agreementId":"A1","period":"2027-03"}`,
		"settlementMode": `{"agreementId":"` + aid + `","period":"2027-03","settlementMode":"VENDOR_INVOICE"}`,
	}
	for field, body := range cases {
		code, out := call(t, h, http.MethodPost, "/v1/pricing/rebates/vendor-claims", allDuties, body)
		errs, _ := out["errors"].(map[string]any)
		if code != http.StatusUnprocessableEntity || errs[field] == nil {
			t.Errorf("bad %s = %d %v; want 422 naming it", field, code, out)
		}
	}
	if code, out := call(t, h, http.MethodPost, "/v1/pricing/rebates/vendor-claims", "pricing.rebate.view", `{"agreementId":"`+aid+`","period":"2027-03"}`); code != http.StatusForbidden {
		t.Fatalf("claim without pricing.rebate.maintain = %d %v; want 403", code, out)
	}
}
