package api_test

import (
	"fmt"
	"net/http"
	"testing"
)

const (
	basesURL = "/v1/pricing/commodity/bases"
	calcsURL = "/v1/pricing/commodity/calculations"
	calcBody = `{"legalEntityId":"%s","name":"Copper rod EA","basisId":"%s","factor":"%s","marginKind":"%s","margin":"%s",` +
		`"customerKind":"all","productId":"P1","unit":"EA","currency":"USD","rounding":2}`
)

func newBasis(t *testing.T, h http.Handler, code string) string {
	t.Helper()
	body := fmt.Sprintf(`{"legalEntityId":"%s","code":"%s","name":"LME copper cash","currency":"USD","unit":"MT"}`, legalEntity, code)
	status, out := call(t, h, http.MethodPost, basesURL, allDuties, body, "Idempotency-Key", "basis-"+code)
	if status != http.StatusCreated {
		t.Fatalf("create basis = %d %v", status, out)
	}
	return out["id"].(string)
}

func newCalc(t *testing.T, h http.Handler, basis, factor, kind, margin string) map[string]any {
	t.Helper()
	status, out := call(t, h, http.MethodPost, calcsURL, allDuties, fmt.Sprintf(calcBody, legalEntity, basis, factor, kind, margin))
	if status != http.StatusCreated {
		t.Fatalf("create calculation = %d %v", status, out)
	}
	return out
}

// The quotes inquiry carries the affected-agreements count of every quote; a quote just
// recorded (the create response) does not.
func TestCommodity_QuotesInquiryCarriesAffectedAgreements(t *testing.T) {
	h := router(newFake())
	basis := newBasis(t, h, "CU-LME")
	if created := recordIndex(t, h, basis, "2026-06-08", "9000"); created["affectedAgreements"] != nil {
		t.Fatalf("create response = %v; want no affectedAgreements", created)
	}
	status, list := call(t, h, http.MethodGet, basesURL+"/"+basis+"/index-values", "pricing.view", "")
	items, _ := list["items"].([]any)
	if status != http.StatusOK || len(items) != 1 {
		t.Fatalf("quotes = %d %v", status, list)
	}
	if n, ok := items[0].(map[string]any)["affectedAgreements"].(float64); !ok || n != 0 {
		t.Fatalf("quote = %v; want affectedAgreements 0 present", items[0])
	}
}

func recordIndex(t *testing.T, h http.Handler, basis, day, value string) map[string]any {
	t.Helper()
	status, out := call(t, h, http.MethodPost, basesURL+"/"+basis+"/index-values", allDuties, fmt.Sprintf(`{"asOf":"%s","value":"%s","source":"LME"}`, day, value))
	if status != http.StatusCreated {
		t.Fatalf("record index = %d %v", status, out)
	}
	return out
}

// TestCommodity_RunCreatesValidatedJournal covers FR-26.21: index x factor + margin reprices
// the ACTIVE PRICE record, the run is previewable, and the journal waits for review.
func TestCommodity_RunCreatesValidatedJournal(t *testing.T) {
	h := router(newFake())
	activeP1Record(t, h, "9.00", "")
	basis := newBasis(t, h, "CU-LME")
	calc := newCalc(t, h, basis, "0.0012", "PERCENT", "10")
	id := calc["id"].(string)

	if status, out := call(t, h, http.MethodPost, calcsURL+"/"+id+":preview", allDuties, `{"asOf":"2026-06-10"}`); status != http.StatusUnprocessableEntity || out["code"] != "PRC-0052" {
		t.Fatalf("preview with no index = %d %v; want 422 PRC-0052", status, out)
	}
	recordIndex(t, h, basis, "2026-06-08", "9000")
	if rev := recordIndex(t, h, basis, "2026-06-08", "9100"); rev["revision"] != float64(2) {
		t.Fatalf("second quote of a day = %v; want revision 2", rev)
	}

	// 9100 x 0.0012 = 10.92 (2 decimals); + 10% = 12.012 at the 6-decimal unit price.
	status, pv := call(t, h, http.MethodPost, calcsURL+"/"+id+":preview", "pricing.view", `{"asOf":"2026-06-10"}`)
	if status != http.StatusOK || pv["price"] != "12.012" || pv["stale"] != false || len(pv["changes"].([]any)) != 1 {
		t.Fatalf("preview = %d %v", status, pv)
	}
	ch := pv["changes"].([]any)[0].(map[string]any)
	if ch["currentAmount"] != "9" || ch["newAmount"] != "12.012" {
		t.Fatalf("change = %v; want 9 -> 12.012", ch)
	}
	if _, list := call(t, h, http.MethodGet, "/v1/pricing/journals", allDuties, ""); len(list["items"].([]any)) != 0 {
		t.Fatalf("preview wrote a journal: %v", list)
	}

	status, run := call(t, h, http.MethodPost, calcsURL+"/"+id+":run", allDuties, `{"asOf":"2026-06-10"}`, "Idempotency-Key", "run-1")
	if status != http.StatusCreated || run["changedCount"] != float64(1) || run["journalId"] == nil || run["indexRevision"] != float64(2) {
		t.Fatalf("run = %d %v", status, run)
	}
	_, j := call(t, h, http.MethodGet, "/v1/pricing/journals/"+run["journalId"].(string), allDuties, "")
	if j["status"] != "VALIDATED" {
		t.Fatalf("journal = %v; want VALIDATED", j)
	}
	_, again := call(t, h, http.MethodPost, calcsURL+"/"+id+":run", allDuties, `{"asOf":"2026-06-10"}`, "Idempotency-Key", "run-1")
	if again["id"] != run["id"] {
		t.Fatalf("replayed run = %v; want %v", again["id"], run["id"])
	}
	_, runs := call(t, h, http.MethodGet, calcsURL+"/"+id+"/runs", "pricing.view", "")
	if len(runs["items"].([]any)) != 1 {
		t.Fatalf("runs = %v; want one", runs)
	}
}

func TestCommodity_StaleIndexWarnsAndWithdrawnRefuses(t *testing.T) {
	h := router(newFake())
	activeP1Record(t, h, "9.00", "")
	basis := newBasis(t, h, "CU-LME")
	calc := newCalc(t, h, basis, "0.0012", "AMOUNT", "1")
	id := calc["id"].(string)
	recordIndex(t, h, basis, "2026-05-01", "9000")
	status, pv := call(t, h, http.MethodPost, calcsURL+"/"+id+":preview", allDuties, `{"asOf":"2026-06-10"}`)
	w, _ := pv["warnings"].([]any)
	if status != http.StatusOK || pv["stale"] != true || len(w) != 1 || w[0].(map[string]any)["code"] != "PRC-0016" {
		t.Fatalf("stale preview = %d %v; want PRC-0016 warning", status, pv)
	}
	// 9000 x 0.0012 + 1 = 11.80.
	if pv["price"] != "11.8" {
		t.Fatalf("price = %v; want 11.8", pv["price"])
	}
	if status, w := call(t, h, http.MethodPost, calcsURL+"/"+id+":withdraw", allDuties, `{"version":1}`); status != http.StatusOK || w["withdrawn"] != true {
		t.Fatalf("withdraw = %d %v", status, w)
	}
	if status, out := call(t, h, http.MethodPost, calcsURL+"/"+id+":run", allDuties, `{"asOf":"2026-06-10"}`); status != http.StatusConflict {
		t.Fatalf("run of a withdrawn calculation = %d %v; want 409", status, out)
	}
	if status, _ := call(t, h, http.MethodPost, calcsURL+"/"+id+":withdraw", allDuties, `{"version":1}`); status != http.StatusPreconditionFailed {
		t.Fatalf("stale withdraw = %d; want 412", status)
	}
}

func TestCommodity_ValidationAndDuties(t *testing.T) {
	h := router(newFake())
	basis := newBasis(t, h, "CU-LME")
	if status, out := call(t, h, http.MethodPost, basesURL, allDuties, fmt.Sprintf(`{"legalEntityId":"%s","code":"CU-LME","name":"dup","currency":"USD","unit":"MT"}`, legalEntity)); status != http.StatusConflict || out["code"] != "PRC-0054" {
		t.Fatalf("duplicate code = %d %v; want 409 PRC-0054", status, out)
	}
	if status, out := call(t, h, http.MethodPost, basesURL, allDuties, `{"legalEntityId":"x","code":"cu","name":"","currency":"usd","unit":""}`); status != http.StatusUnprocessableEntity || len(out["errors"].(map[string]any)) < 5 {
		t.Fatalf("invalid basis = %d %v", status, out)
	}
	if status, _ := call(t, h, http.MethodPost, basesURL+"/"+basis+"/index-values", allDuties, `{"asOf":"2026-06-08","value":"0"}`); status != http.StatusUnprocessableEntity {
		t.Fatalf("zero index = %d; want 422", status)
	}
	bad := fmt.Sprintf(calcBody, legalEntity, basis, "-1", "RATIO", "-3")
	if status, out := call(t, h, http.MethodPost, calcsURL, allDuties, bad); status != http.StatusUnprocessableEntity || out["errors"].(map[string]any)["marginKind"] == nil {
		t.Fatalf("invalid calculation = %d %v", status, out)
	}
	c := newCalc(t, h, basis, "0.5", "AMOUNT", "0")
	for _, tc := range []struct{ method, path, duties, body string }{
		{http.MethodPost, basesURL, "pricing.view", `{}`},
		{http.MethodPost, calcsURL + "/" + c["id"].(string) + ":run", "pricing.view", `{"asOf":"2026-06-10"}`},
		{http.MethodPost, calcsURL + "/" + c["id"].(string) + ":withdraw", "pricing.view", `{"version":1}`},
		{http.MethodGet, calcsURL, "", ""},
	} {
		if status, _ := call(t, h, tc.method, tc.path, tc.duties, tc.body); status != http.StatusForbidden && status != http.StatusUnauthorized {
			t.Errorf("%s %s with %q = %d; want 401/403", tc.method, tc.path, tc.duties, status)
		}
	}
	if status, _ := call(t, h, http.MethodGet, calcsURL+"/not-a-uuid", allDuties, ""); status != http.StatusNotFound {
		t.Fatalf("bad id = %d; want 404", status)
	}
}
