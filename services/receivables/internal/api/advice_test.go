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

func TestBankAccountRoutes(t *testing.T) {
	f := &fakeStore{}
	h := router(f)
	put := func(body, duty string) int {
		return do(h, http.MethodPut, "/v1/receivables/bank-accounts", body, duty, nil).Code
	}
	good := `{"legalEntityId":"` + testLE + `","currency":"USD","accountRef":"us 00 12","glAccount":"1010","name":"Operating"}`
	if code := put(good, "receivables.view"); code != http.StatusForbidden {
		t.Errorf("a viewer may not write: %d", code)
	}
	for name, body := range map[string]string{
		"lower case currency": `{"legalEntityId":"` + testLE + `","currency":"usd","glAccount":"1010"}`,
		"account with space":  `{"legalEntityId":"` + testLE + `","currency":"USD","glAccount":"10 10"}`,
		"no entity":           `{"currency":"USD","glAccount":"1010"}`,
		"unknown field":       `{"legalEntityId":"` + testLE + `","currency":"USD","glAccount":"1010","bank":"x"}`,
	} {
		if code := put(body, "receivables.bankaccount.manage"); code != http.StatusBadRequest {
			t.Errorf("%s: %d, want 400", name, code)
		}
	}
	if len(f.banks) != 0 {
		t.Fatal("an invalid mapping reached the store")
	}
	if code := put(good, "receivables.bankaccount.manage"); code != http.StatusOK || len(f.banks) != 1 || f.banks[0].AccountRef != "US0012" {
		t.Fatalf("a valid mapping: %d %+v", code, f.banks)
	}
	if rec := do(h, http.MethodGet, "/v1/receivables/bank-accounts", "", "receivables.view", nil); rec.Code != http.StatusOK {
		t.Errorf("list: %d", rec.Code)
	}
	del := "/v1/receivables/bank-accounts?legalEntityId=" + testLE + "&currency=USD&accountRef=US0012"
	if rec := do(h, http.MethodDelete, del, "", "receivables.bankaccount.manage", nil); rec.Code != http.StatusNoContent || f.deletedBank != testLE+"|USD|US0012" {
		t.Errorf("delete: %d %q", rec.Code, f.deletedBank)
	}
	if rec := do(h, http.MethodDelete, "/v1/receivables/bank-accounts?currency=USD", "", "receivables.bankaccount.manage", nil); rec.Code != http.StatusBadRequest {
		t.Errorf("delete without an entity: %d", rec.Code)
	}
}
