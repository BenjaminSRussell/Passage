package api

import (
	"net/http"
	"strings"
	"testing"

	"github.com/BenjaminSRussell/passage/services/ledger/internal/domain"
	"github.com/BenjaminSRussell/passage/services/ledger/internal/store"
)

func TestPutReasonCode_CreatedThenReplaced(t *testing.T) {
	fs := &fakeStore{created: true}
	h := server(fs)
	body := `{"name":"Wrong vendor","appliesTo":["ledger"],"negativePostingAllowed":true}`
	rec := call(h, http.MethodPut, "/v1/ledger/reason-codes/WRONGVEND", body, dutyReasonWrite, nil)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create status %d: %s", rec.Code, rec.Body.String())
	}
	in := fs.putReasons[0]
	if in.Code != "WRONGVEND" || !in.NegativePostingAllowed || !in.AlternativePostingDateAllowed || !in.Active {
		t.Fatalf("defaults not applied: %+v", in)
	}

	fs.created = false
	rec = call(h, http.MethodPut, "/v1/ledger/reason-codes/WRONGVEND", body, dutyReasonWrite, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("replace status %d: %s", rec.Code, rec.Body.String())
	}
}

func TestPutReasonCode_Validation(t *testing.T) {
	tests := []struct {
		name      string
		path      string
		body      string
		wantField string
	}{
		{"lowercase code", "/v1/ledger/reason-codes/wrong", `{"name":"x","appliesTo":["ledger"]}`, "code"},
		{"code too long", "/v1/ledger/reason-codes/ELEVENCHARS", `{"name":"x","appliesTo":["ledger"]}`, "code"},
		{"empty name", "/v1/ledger/reason-codes/OK", `{"name":"","appliesTo":["ledger"]}`, "name"},
		{"no modules", "/v1/ledger/reason-codes/OK", `{"name":"x","appliesTo":[]}`, "appliesTo"},
		{"unknown module", "/v1/ledger/reason-codes/OK", `{"name":"x","appliesTo":["payroll"]}`, "appliesTo"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			fs := &fakeStore{}
			rec := call(server(fs), http.MethodPut, tc.path, tc.body, dutyReasonWrite, nil)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
			}
			errs, _ := decode(t, rec)["errors"].(map[string]any)
			if _, ok := errs[tc.wantField]; !ok {
				t.Fatalf("want field error %q, got %v", tc.wantField, errs)
			}
			if len(fs.putReasons) != 0 {
				t.Fatal("an invalid reason code must not reach the store")
			}
		})
	}
}

func TestReasonCodes_ReadRoutes(t *testing.T) {
	fs := &fakeStore{reasons: []store.ReasonCode{{Code: "WRONGVEND", Name: "Wrong vendor", AppliesTo: []string{"ledger"}, Active: true}}}
	h := server(fs)
	rec := call(h, http.MethodGet, "/v1/ledger/reason-codes", "", dutyRead, nil)
	if rec.Code != http.StatusOK || len(decode(t, rec)["items"].([]any)) != 1 {
		t.Fatalf("list: status %d: %s", rec.Code, rec.Body.String())
	}
	rec = call(h, http.MethodGet, "/v1/ledger/reason-codes/WRONGVEND", "", dutyRead, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("get: status %d", rec.Code)
	}
	rec = call(h, http.MethodGet, "/v1/ledger/reason-codes/NOPE", "", dutyRead, nil)
	if rec.Code != http.StatusNotFound || decode(t, rec)["code"] != "LEDGER_REASON_NOT_FOUND" {
		t.Fatalf("missing code: status %d", rec.Code)
	}
}

func TestReverseEntry_ReasonFieldsReachStore(t *testing.T) {
	fs := &fakeStore{}
	rec := call(server(fs), http.MethodPost, "/v1/ledger/journal-entries/"+testEntry+"/reverse",
		`{"reasonCode":"WRONGVEND","reasonComment":"posted to the wrong vendor"}`, dutyReverse, map[string]string{"Idempotency-Key": "rev-r1"})
	if rec.Code != http.StatusCreated {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	in := fs.reversed[0]
	if in.ReasonCode != "WRONGVEND" || in.ReasonComment != "posted to the wrong vendor" {
		t.Fatalf("reason fields not mapped: %+v", in)
	}
}

func TestReverseEntry_ReasonFieldValidation(t *testing.T) {
	long := strings.Repeat("x", 256)
	tests := []struct {
		name, body, field string
	}{
		{"malformed code", `{"reasonCode":"err-1"}`, "reasonCode"},
		{"code too long", `{"reasonCode":"ELEVENCHARS"}`, "reasonCode"},
		{"comment too long", `{"reasonCode":"ERR01","reasonComment":"` + long + `"}`, "reasonComment"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			fs := &fakeStore{}
			rec := call(server(fs), http.MethodPost, "/v1/ledger/journal-entries/"+testEntry+"/reverse", tc.body, dutyReverse,
				map[string]string{"Idempotency-Key": "rev-v"})
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
			}
			errs, _ := decode(t, rec)["errors"].(map[string]any)
			if _, ok := errs[tc.field]; !ok {
				t.Fatalf("want field error %q, got %v", tc.field, errs)
			}
			if len(fs.reversed) != 0 {
				t.Fatal("an invalid reversal must not reach the store")
			}
		})
	}
}

func TestReverseEntry_StoreRejectionIs422(t *testing.T) {
	fs := &fakeStore{err: &domain.RejectedError{Violations: []domain.Violation{{Code: "LEDGER_REASON_REQUIRED", Field: "reasonCode",
		Message: "a reason code is required for this reversal"}}}}
	rec := call(server(fs), http.MethodPost, "/v1/ledger/journal-entries/"+testEntry+"/reverse", `{}`, dutyReverse,
		map[string]string{"Idempotency-Key": "rev-422"})
	if rec.Code != http.StatusUnprocessableEntity || decode(t, rec)["code"] != "LEDGER_REASON_REQUIRED" {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
}

func TestCreateLedger_ReversalParameterDefaults(t *testing.T) {
	h := server(&fakeStore{})
	base := `"legalEntityId":"` + testDoc + `","code":"GL","name":"General ledger","chartCode":"STD","currency":"USD"`
	inScope := map[string]string{"X-Passage-Legal-Entity": testDoc}
	rec := call(h, http.MethodPost, "/v1/ledger/ledgers", `{`+base+`}`, dutyLedgerWrite, inScope)
	body := decode(t, rec)
	if rec.Code != http.StatusCreated || body["reversalReasonRequired"] != true || body["negativePostingEnabled"] != false {
		t.Fatalf("defaults: status %d: %s", rec.Code, rec.Body.String())
	}
	rec = call(h, http.MethodPost, "/v1/ledger/ledgers", `{`+base+`,"reversalReasonRequired":false,"negativePostingEnabled":true}`, dutyLedgerWrite, inScope)
	body = decode(t, rec)
	if rec.Code != http.StatusCreated || body["reversalReasonRequired"] != false || body["negativePostingEnabled"] != true {
		t.Fatalf("explicit: status %d: %s", rec.Code, rec.Body.String())
	}
}
