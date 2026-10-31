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
