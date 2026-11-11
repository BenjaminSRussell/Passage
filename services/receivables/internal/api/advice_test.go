package api

import (
	"encoding/json"
	"net/http"
	"testing"
)

func adviceBody(t *testing.T, extra map[string]any) string {
	t.Helper()
	m := map[string]any{"legalEntityId": testLE, "format": "CSV", "content": "paymentRef,reference,amount\nWIRE-1,INV-1,100.00\n"}
	for k, v := range extra {
		m[k] = v
	}
	b, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// 30-4: createMissing needs a currency, and what the caller sent reaches the store.
func TestImportAdvice_CreateMissingNeedsACurrencyAndPassesTheOptionsOn(t *testing.T) {
	f := &fakeStore{}
	h := router(f)
	post := func(extra map[string]any) int {
		return do(h, http.MethodPost, "/v1/receivables/remittance-advices", adviceBody(t, extra), "receivables.payment.post", nil).Code
	}
	for name, extra := range map[string]map[string]any{
		"no currency":  {"createMissing": true},
		"bad currency": {"createMissing": true, "currency": "usd"},
		"bad date":     {"createMissing": true, "currency": "USD", "valueDate": "yesterday"},
		"bad mode":     {"createMissing": true, "currency": "USD", "paymentMode": "BARTER"},
	} {
		if code := post(extra); code != http.StatusBadRequest {
			t.Errorf("%s: status %d, want 400", name, code)
		}
	}
	if code := post(map[string]any{"createMissing": true, "currency": "EUR", "valueDate": "2027-03-18", "paymentMode": "ACH"}); code != http.StatusOK {
		t.Fatalf("valid createMissing: %d", code)
	}
	if o := f.adviceOptions; !o.CreateMissing || o.Currency != "EUR" || o.ValueDate != "2027-03-18" || o.PaymentMode != "ACH" {
		t.Errorf("options reaching the store = %+v", o)
	}
	if code := post(nil); code != http.StatusOK || f.adviceOptions.CreateMissing {
		t.Errorf("without createMissing: %d %+v", code, f.adviceOptions)
	}
}
