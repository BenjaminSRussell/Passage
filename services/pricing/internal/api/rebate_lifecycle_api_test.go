package api_test

import (
	"net/http"
	"testing"
)

const agreementsURL = "/v1/pricing/rebates/agreements"

func newAgreementID(t *testing.T, h http.Handler) string {
	t.Helper()
	code, out := call(t, h, http.MethodPost, agreementsURL, allDuties, agreementBody(legalEntity, "Quarterly"))
	if code != http.StatusCreated {
		t.Fatalf("create = %d %v", code, out)
	}
	if out["version"] != float64(1) {
		t.Fatalf("created version = %v; want 1", out["version"])
	}
	return out["id"].(string)
}

func accrueOne(t *testing.T, h http.Handler, id, line, date string) {
	t.Helper()
	body := `{"agreementId":"` + id + `","invoiceLineId":"` + line + `","invoiceDate":"` + date + `","value":"1000","quantity":"1","currency":"USD"}`
	if code, out := call(t, h, http.MethodPost, "/v1/pricing/rebates/accruals", allDuties, body); code != http.StatusCreated {
		t.Fatalf("accrue = %d %v", code, out)
	}
}

func TestRebateLifecycle_ReviseNameAndEndDate(t *testing.T) {
	h := router(newFake())
	id := newAgreementID(t, h)
	code, out := call(t, h, http.MethodPost, agreementsURL+"/"+id+":revise", allDuties, `{"version":1,"name":"Quarterly 2026","validTo":"2027-03-31"}`)
	if code != http.StatusOK || out["name"] != "Quarterly 2026" || out["validTo"] != "2027-03-31" || out["version"] != float64(2) {
		t.Fatalf("revise = %d %v", code, out)
	}
	if code, out := call(t, h, http.MethodPost, agreementsURL+"/"+id+":revise", allDuties, `{"version":1,"name":"Stale"}`); code != http.StatusPreconditionFailed || out["code"] != "conflict_version" {
		t.Fatalf("stale revise = %d %v; want 412", code, out)
	}
}

func TestRebateLifecycle_ReviseRules(t *testing.T) {
	h := router(newFake())
	id := newAgreementID(t, h)
	for name, body := range map[string]string{
		"nothing to change": `{"version":1}`,
		"no version":        `{"name":"x"}`,
		"bad date":          `{"version":1,"validTo":"soon"}`,
		"bad tier":          `{"version":1,"tiers":[{"from":"zero","ratePct":"1"}]}`,
	} {
		if code, out := call(t, h, http.MethodPost, agreementsURL+"/"+id+":revise", allDuties, body); code != http.StatusUnprocessableEntity {
			t.Fatalf("%s = %d %v; want 422", name, code, out)
		}
	}
	accrueOne(t, h, id, "INV-1", "2026-06-15")
	if code, out := call(t, h, http.MethodPost, agreementsURL+"/"+id+":revise", allDuties, `{"version":1,"tiers":[{"from":"0","ratePct":"4"}]}`); code != http.StatusUnprocessableEntity || out["code"] != "PRC-0049" {
		t.Fatalf("tiers after accrual = %d %v; want PRC-0049", code, out)
	}
	if code, out := call(t, h, http.MethodPost, agreementsURL+"/"+id+":revise", allDuties, `{"version":1,"validTo":"2026-06-01"}`); code != http.StatusUnprocessableEntity || out["code"] != "PRC-0048" {
		t.Fatalf("end before accrual = %d %v; want PRC-0048", code, out)
	}
	if code, out := call(t, h, http.MethodPost, agreementsURL+"/"+id+":revise", "pricing.view", `{"version":1,"name":"x"}`); code != http.StatusForbidden {
		t.Fatalf("revise without duty = %d %v; want 403", code, out)
	}
	if code, _ := call(t, h, http.MethodPost, agreementsURL+"/not-a-uuid:revise", allDuties, `{"version":1,"name":"x"}`); code != http.StatusNotFound {
		t.Fatalf("revise bad id = %d; want 404", code)
	}
}

func TestRebateLifecycle_Close(t *testing.T) {
	h := router(newFake())
	id := newAgreementID(t, h)
	accrueOne(t, h, id, "INV-1", "2026-06-15")
	if code, out := call(t, h, http.MethodPost, agreementsURL+"/"+id+":close", allDuties, `{"version":1,"closeDate":"2026-06-01"}`); code != http.StatusUnprocessableEntity || out["code"] != "PRC-0048" {
		t.Fatalf("close before accrual = %d %v; want PRC-0048", code, out)
	}
	if code, out := call(t, h, http.MethodPost, agreementsURL+"/"+id+":close", allDuties, `{"version":1,"closeDate":"2027-02-01"}`); code != http.StatusUnprocessableEntity {
		t.Fatalf("close after validity = %d %v; want 422", code, out)
	}
	if code, out := call(t, h, http.MethodPost, agreementsURL+"/"+id+":close", allDuties, `{"version":1}`); code != http.StatusUnprocessableEntity {
		t.Fatalf("close without date = %d %v; want 422", code, out)
	}
	code, out := call(t, h, http.MethodPost, agreementsURL+"/"+id+":close", allDuties, `{"version":1,"closeDate":"2026-09-30"}`)
	if code != http.StatusOK || out["status"] != "CLOSED" || out["validTo"] != "2026-09-30" || out["version"] != float64(2) {
		t.Fatalf("close = %d %v", code, out)
	}
	if code, out := call(t, h, http.MethodPost, agreementsURL+"/"+id+":close", allDuties, `{"version":2,"closeDate":"2026-09-30"}`); code != http.StatusConflict || out["code"] != "PRC-0031" {
		t.Fatalf("close twice = %d %v; want 409 PRC-0031", code, out)
	}
	if code, out := call(t, h, http.MethodPost, agreementsURL+"/"+id+":revise", allDuties, `{"version":2,"name":"x"}`); code != http.StatusConflict {
		t.Fatalf("revise closed = %d %v; want 409", code, out)
	}
	if code, out := call(t, h, http.MethodGet, agreementsURL+"/"+id, allDuties, ""); code != http.StatusOK || out["status"] != "CLOSED" {
		t.Fatalf("read after close = %d %v", code, out)
	}
}

// FR-26.11: the product scope of an agreement is revised until the first invoice accrues, and
// the agreement list filters by it.
func TestRebateLifecycle_ReviseProductScopeAndFilter(t *testing.T) {
	h := router(newFake())
	id := newAgreementID(t, h)
	const prod, grp = "0f8fad5b-d9cb-469f-a165-70867728950e", "7c9e6679-7425-40de-944b-e07fc1f90ae7"
	code, out := call(t, h, http.MethodPost, agreementsURL+"/"+id+":revise", allDuties, `{"version":1,"productScope":{"productId":"`+prod+`"}}`)
	if code != http.StatusOK || out["productId"] != prod || out["version"] != float64(2) {
		t.Fatalf("scope to product = %d %v", code, out)
	}
	_, list := call(t, h, http.MethodGet, agreementsURL+"?productId="+prod, allDuties, "")
	if items, _ := list["items"].([]any); len(items) != 1 {
		t.Fatalf("filter by product = %v; want the one agreement", list)
	}
	_, list = call(t, h, http.MethodGet, agreementsURL+"?productId="+grp, allDuties, "")
	if items, _ := list["items"].([]any); len(items) != 0 {
		t.Fatalf("filter by another product = %v; want none", list)
	}
	if code, out := call(t, h, http.MethodGet, agreementsURL+"?productGroupId=nope", allDuties, ""); code != http.StatusUnprocessableEntity {
		t.Fatalf("bad productGroupId filter = %d %v; want 422", code, out)
	}
	for name, body := range map[string]string{
		"both":    `{"version":2,"productScope":{"productId":"` + prod + `","productGroupId":"` + grp + `"}}`,
		"not id":  `{"version":2,"productScope":{"productId":"x"}}`,
		"not grp": `{"version":2,"productScope":{"productGroupId":"x"}}`,
	} {
		if code, out := call(t, h, http.MethodPost, agreementsURL+"/"+id+":revise", allDuties, body); code != http.StatusUnprocessableEntity {
			t.Fatalf("%s = %d %v; want 422", name, code, out)
		}
	}
	// {} takes every line again.
	code, out = call(t, h, http.MethodPost, agreementsURL+"/"+id+":revise", allDuties, `{"version":2,"productScope":{}}`)
	if _, has := out["productId"]; code != http.StatusOK || has {
		t.Fatalf("clear scope = %d %v", code, out)
	}
	accrueOne(t, h, id, "INV-1", "2026-02-10")
	code, out = call(t, h, http.MethodPost, agreementsURL+"/"+id+":revise", allDuties, `{"version":3,"productScope":{"productGroupId":"`+grp+`"}}`)
	if code != http.StatusUnprocessableEntity || out["code"] != "PRC-0071" {
		t.Fatalf("scope after accrual = %d %v; want 422 PRC-0071", code, out)
	}
}

// A scope id is compared in canonical form (an upper-case spelling of the same group is the same
// scope, at the accrual and at a restating revise), and a revise without productScope keeps it.
func TestRebateLifecycle_ProductScopeCanonicalAndKept(t *testing.T) {
	h := router(newFake())
	id := newAgreementID(t, h)
	const grp, upper = "7c9e6679-7425-40de-944b-e07fc1f90ae7", "7C9E6679-7425-40DE-944B-E07FC1F90AE7"
	code, out := call(t, h, http.MethodPost, agreementsURL+"/"+id+":revise", allDuties, `{"version":1,"productScope":{"productGroupId":"`+upper+`"}}`)
	if code != http.StatusOK || out["productGroupId"] != grp {
		t.Fatalf("scope to group = %d %v; want the canonical id", code, out)
	}
	code, out = call(t, h, http.MethodPost, agreementsURL+"/"+id+":revise", allDuties, `{"version":2,"name":"Renamed"}`)
	if code != http.StatusOK || out["productGroupId"] != grp {
		t.Fatalf("revise without productScope = %d %v; want the scope kept", code, out)
	}
	body := `{"agreementId":"` + id + `","invoiceLineId":"INV-U1","invoiceDate":"2026-02-10","value":"1000","quantity":"1","currency":"USD","productGroupId":"` + upper + `"}`
	if code, out := call(t, h, http.MethodPost, "/v1/pricing/rebates/accruals", allDuties, body); code != http.StatusCreated {
		t.Fatalf("accrue upper-case group = %d %v", code, out)
	}
	if code, out := call(t, h, http.MethodPost, agreementsURL+"/"+id+":revise", allDuties, `{"version":3,"productScope":{"productGroupId":"`+upper+`"}}`); code != http.StatusOK {
		t.Fatalf("restate the scope after an accrual = %d %v; want 200", code, out)
	}
	if _, list := call(t, h, http.MethodGet, agreementsURL+"?productGroupId="+upper, allDuties, ""); len(list["items"].([]any)) != 1 {
		t.Fatalf("filter by upper-case group = %v; want the agreement", list)
	}
}
