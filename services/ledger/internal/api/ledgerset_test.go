package api

import (
	"net/http"
	"strings"
	"testing"
)

const setEntryBody = `{"documentId":"` + testDoc + `","postingDate":"2026-03-31",
  "source":{"system":"ap","type":"vendor_invoice","id":"7F3A"},
  "lines":[{"account":"6010","debitCredit":"D","amount":{"currency":"USD","value":"10.00"}},
           {"account":"2100","debitCredit":"C","amount":{"currency":"USD","value":"10.00"}}]}`

func TestPostSetEntry_PassesSetAndCountsEveryEntry(t *testing.T) {
	fs := &fakeStore{}
	h := server(fs)
	path := "/v1/ledger/ledger-sets/" + testSet + "/journal-entries"
	rec := call(h, http.MethodPost, path, setEntryBody, dutyPost, map[string]string{"Idempotency-Key": "k1"})
	if rec.Code != http.StatusCreated || len(fs.setPosts) != 1 {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	if got := fs.setPosts[0]; got.setID != testSet || got.in.Key != "k1" || len(got.in.Hash) != 32 || got.in.Actor != "clerk-1" {
		t.Fatalf("set post: %+v", got)
	}
	for _, tc := range []struct {
		name, key, body string
	}{
		{"missing key", "", setEntryBody},
		{"ledgerId sent", "k", strings.Replace(setEntryBody, `{"documentId"`, `{"ledgerId":"`+testLedger+`","documentId"`, 1)},
		{"bad date", "k", strings.Replace(setEntryBody, "2026-03-31", "nope", 1)},
	} {
		headers := map[string]string{}
		if tc.key != "" {
			headers["Idempotency-Key"] = tc.key
		}
		if rec := call(h, http.MethodPost, path, tc.body, dutyPost, headers); rec.Code != http.StatusBadRequest {
			t.Errorf("%s: status %d", tc.name, rec.Code)
		}
	}
	if rec := call(h, http.MethodPost, path, setEntryBody, dutyRead, map[string]string{"Idempotency-Key": "k"}); rec.Code != http.StatusForbidden {
		t.Errorf("without the post duty: status %d", rec.Code)
	}
	if rec := call(h, http.MethodPost, "/v1/ledger/ledger-sets/nope/journal-entries", setEntryBody, dutyPost, map[string]string{"Idempotency-Key": "k"}); rec.Code != http.StatusNotFound {
		t.Errorf("malformed set id: status %d", rec.Code)
	}
}

func TestCreateLedgerSet_Validation(t *testing.T) {
	fs := &fakeStore{}
	h := server(fs)
	bad := `{"code":"bad code","name":"","leadingLedgerId":"x","parallelLedgerIds":["y"]}`
	rec := call(h, http.MethodPost, "/v1/ledger/ledger-sets", bad, dutySetupWrite, nil)
	if rec.Code != http.StatusBadRequest || len(fs.setInputs) != 0 {
		t.Fatalf("status %d, store calls %d", rec.Code, len(fs.setInputs))
	}
	good := `{"code":"IFRS","name":"Parallel","leadingLedgerId":"` + testLedger + `","parallelLedgerIds":["` + testDoc + `"]}`
	if rec := call(h, http.MethodPost, "/v1/ledger/ledger-sets", good, dutyRead, nil); rec.Code != http.StatusForbidden {
		t.Fatalf("without the setup duty: status %d", rec.Code)
	}
	if rec := call(h, http.MethodPost, "/v1/ledger/ledger-sets", good, dutySetupWrite, nil); rec.Code != http.StatusCreated || len(fs.setInputs) != 1 {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
}

func TestCreateAccountMapping_Validation(t *testing.T) {
	fs := &fakeStore{}
	h := server(fs)
	path := "/v1/ledger/ledgers/" + testLedger + "/account-mappings"
	for _, body := range []string{
		`{"sourceAccount":"1600","targetAccount":"1600","effectiveFrom":"2026-01-01"}`,
		`{"sourceAccount":"1600","targetAccount":"6100","effectiveFrom":"soon"}`,
	} {
		if rec := call(h, http.MethodPost, path, body, dutySetupWrite, nil); rec.Code != http.StatusBadRequest {
			t.Errorf("%s: status %d", body, rec.Code)
		}
	}
	rec := call(h, http.MethodPost, path, `{"sourceAccount":"1600","targetAccount":"6100","effectiveFrom":"2026-01-01"}`, dutySetupWrite, nil)
	if rec.Code != http.StatusCreated || len(fs.mappings) != 1 || fs.mappings[0].LedgerID != testLedger {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
}
