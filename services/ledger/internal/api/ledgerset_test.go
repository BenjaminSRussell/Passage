package api

import (
	"net/http"
	"strings"
	"testing"

	"github.com/BenjaminSRussell/passage/services/ledger/internal/domain"
	"github.com/BenjaminSRussell/passage/services/ledger/internal/store"
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
	empty := `{"code":"IFRS","name":"Parallel","leadingLedgerId":"` + testLedger + `","parallelLedgerIds":[]}`
	if rec := call(h, http.MethodPost, "/v1/ledger/ledger-sets", empty, dutySetupWrite, nil); rec.Code != http.StatusBadRequest || len(fs.setInputs) != 0 {
		t.Fatalf("empty parallelLedgerIds: status %d, store calls %d", rec.Code, len(fs.setInputs))
	}
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

func TestCreateAccountMapping_OnLeadingLedgerIs422WithViolations(t *testing.T) {
	fs := &fakeStore{err: &domain.RejectedError{Violations: []domain.Violation{
		{Code: store.CodeMappingOnLeading, Field: "ledgerId", Message: "account mappings are not applied to a set's leading ledger"},
	}}}
	rec := call(server(fs), http.MethodPost, "/v1/ledger/ledgers/"+testLedger+"/account-mappings",
		`{"sourceAccount":"1600","targetAccount":"6100","effectiveFrom":"2026-01-01"}`, dutySetupWrite, nil)
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	m := decode(t, rec)
	v := m["violations"].([]any)
	if m["code"] != store.CodeMappingOnLeading || len(v) != 1 || v[0].(map[string]any)["field"] != "ledgerId" {
		t.Fatalf("body: %v", m)
	}
}

func TestPostLedgerSpecificDuty_IsPassedToTheStoreOnlyWhenHeld(t *testing.T) {
	for _, tc := range []struct {
		name, duties string
		want         bool
	}{
		{"post duty alone", dutyPost, false},
		{"post and ledger-specific duties", dutyPost + "," + store.DutyPostLedgerSpecific, true},
	} {
		fs := &fakeStore{}
		rec := call(server(fs), http.MethodPost, "/v1/ledger/journal-entries", entryBody, tc.duties, map[string]string{"Idempotency-Key": "k1"})
		if rec.Code != http.StatusCreated || len(fs.posted) != 1 || fs.posted[0].CanPostLedgerSpecific != tc.want {
			t.Errorf("%s: status %d, posted %+v", tc.name, rec.Code, fs.posted)
		}
		fs = &fakeStore{}
		rec = call(server(fs), http.MethodPost, batchPath+"/post", "", journalDuties+","+tc.duties, map[string]string{"Idempotency-Key": "k1"})
		if rec.Code != http.StatusCreated || len(fs.batchCalls) != 1 || fs.batchCalls[0].post.CanPostLedgerSpecific != tc.want {
			t.Errorf("%s: batch post status %d, calls %+v", tc.name, rec.Code, fs.batchCalls)
		}
	}
}

func TestPostLedgerSpecificDuty_AloneOpensNoRoute(t *testing.T) {
	fs := &fakeStore{}
	rec := call(server(fs), http.MethodPost, "/v1/ledger/journal-entries", entryBody, store.DutyPostLedgerSpecific, map[string]string{"Idempotency-Key": "k1"})
	if rec.Code != http.StatusForbidden || len(fs.posted) != 0 {
		t.Fatalf("the ledger-specific duty must not replace the posting duty: status %d, posted %d", rec.Code, len(fs.posted))
	}
}

func TestReverseAndValidate_PassTheLedgerSpecificDutyOnlyWhenHeld(t *testing.T) {
	for _, tc := range []struct {
		name, duties string
		want         bool
	}{
		{"reverse duty alone", dutyReverse, false},
		{"reverse and ledger-specific duties", dutyReverse + "," + store.DutyPostLedgerSpecific, true},
	} {
		fs := &fakeStore{}
		rec := call(server(fs), http.MethodPost, "/v1/ledger/journal-entries/"+testEntry+"/reverse", `{}`, tc.duties, map[string]string{"Idempotency-Key": "k1"})
		if rec.Code != http.StatusCreated || len(fs.reversed) != 1 || fs.reversed[0].CanPostLedgerSpecific != tc.want {
			t.Errorf("%s: reverse status %d, calls %+v", tc.name, rec.Code, fs.reversed)
		}
	}
	for _, tc := range []struct {
		name, duties string
		want         bool
	}{
		{"post duty alone", dutyPost, false},
		{"post and ledger-specific duties", dutyPost + "," + store.DutyPostLedgerSpecific, true},
	} {
		fs := &fakeStore{}
		rec := call(server(fs), http.MethodPost, "/v1/ledger/validate", entryBody, tc.duties, nil)
		if rec.Code != http.StatusOK || len(fs.posted) != 1 || fs.posted[0].CanPostLedgerSpecific != tc.want {
			t.Errorf("%s: validate status %d, calls %+v", tc.name, rec.Code, fs.posted)
		}
	}
}

const setLedgerLinesBody = `{"documentId":"` + testDoc + `","postingDate":"2026-03-31",
  "source":{"system":"ap","type":"vendor_invoice","id":"7F3A"},
  "lines":[{"account":"6010","debitCredit":"D","amount":{"currency":"USD","value":"10.00"}}],
  "ledgerLines":[{"ledgerId":"` + testLedger + `","lines":[{"account":"2100","debitCredit":"C","amount":{"currency":"USD","value":"10.00"}}]}]}`

func TestPostSetEntry_LedgerLinesReachTheStoreWithTheDutyFlagAndChangeTheHash(t *testing.T) {
	fs := &fakeStore{}
	h := server(fs)
	path := "/v1/ledger/ledger-sets/" + testSet + "/journal-entries"
	headers := map[string]string{"Idempotency-Key": "k1"}
	rec := call(h, http.MethodPost, path, setLedgerLinesBody, dutyPost+","+store.DutyPostLedgerSpecific, headers)
	if rec.Code != http.StatusCreated || len(fs.setPosts) != 1 {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	got := fs.setPosts[0].in
	if lines := got.LedgerLines[testLedger]; len(lines) != 1 || lines[0].Account != "2100" || lines[0].Side != domain.SideCredit || len(got.Lines) != 1 || !got.CanPostLedgerSpecific {
		t.Fatalf("set post: %+v", got)
	}
	changed := strings.Replace(setLedgerLinesBody, `"value":"10.00"}}]}]`, `"value":"11.00"}}]}]`, 1)
	call(h, http.MethodPost, path, changed, dutyPost, headers)
	if len(fs.setPosts) != 2 || string(fs.setPosts[1].in.Hash) == string(got.Hash) || fs.setPosts[1].in.CanPostLedgerSpecific {
		t.Fatalf("a changed ledger line must change the payload hash and the duty flag follows the caller: %+v", fs.setPosts)
	}
}

func TestPostSetEntry_LedgerLinesValidation(t *testing.T) {
	fs := &fakeStore{}
	h := server(fs)
	path := "/v1/ledger/ledger-sets/" + testSet + "/journal-entries"
	group := `{"ledgerId":"` + testLedger + `","lines":[{"account":"2100","debitCredit":"C","amount":{"currency":"USD","value":"10.00"}}]}`
	for name, body := range map[string]string{
		"bad ledger id":   strings.Replace(setLedgerLinesBody, `"ledgerId":"`+testLedger, `"ledgerId":"nope`, 1),
		"duplicate":       strings.Replace(setLedgerLinesBody, `}}]}]}`, `}}]},`+group+`]}`, 1),
		"empty group":     strings.Replace(setLedgerLinesBody, `"lines":[{"account":"2100","debitCredit":"C","amount":{"currency":"USD","value":"10.00"}}]}]`, `"lines":[]}]`, 1),
		"bad side":        strings.Replace(setLedgerLinesBody, `"debitCredit":"C"`, `"debitCredit":"X"`, 1),
		"no lines at all": strings.Replace(strings.Replace(setLedgerLinesBody, `"lines":[{"account":"6010","debitCredit":"D","amount":{"currency":"USD","value":"10.00"}}],`, `"lines":[],`, 1), `"ledgerLines":[`+group+`]`, `"ledgerLines":[]`, 1),
	} {
		if rec := call(h, http.MethodPost, path, body, dutyPost, map[string]string{"Idempotency-Key": "k"}); rec.Code != http.StatusBadRequest {
			t.Errorf("%s: status %d: %s", name, rec.Code, rec.Body.String())
		}
	}
	if len(fs.setPosts) != 0 {
		t.Fatalf("invalid requests must not reach the store: %d", len(fs.setPosts))
	}
	onlyLedgerLines := strings.Replace(setLedgerLinesBody, `"lines":[{"account":"6010","debitCredit":"D","amount":{"currency":"USD","value":"10.00"}}],`, `"lines":[],`, 1)
	if rec := call(h, http.MethodPost, path, onlyLedgerLines, dutyPost, map[string]string{"Idempotency-Key": "k"}); rec.Code != http.StatusCreated {
		t.Errorf("empty common lines with ledger lines: status %d: %s", rec.Code, rec.Body.String())
	}
}
