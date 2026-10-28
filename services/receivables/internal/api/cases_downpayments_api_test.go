package api

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/receivables/internal/store"
)

// Zero-value stubs so fakeStore satisfies store.Store; slice3Fake overrides them.
func (f *fakeStore) CreateCase(context.Context, store.Case, string) (store.Case, error) {
	return store.Case{}, nil
}
func (f *fakeStore) ListCases(context.Context, store.CaseFilter, int) ([]store.Case, error) {
	return []store.Case{}, nil
}
func (f *fakeStore) GetCase(context.Context, string) (store.Case, error) { return store.Case{}, nil }
func (f *fakeStore) AssignCase(context.Context, string, int, string) (store.Case, error) {
	return store.Case{}, nil
}
func (f *fakeStore) CloseCase(context.Context, string, int, *string) (store.Case, error) {
	return store.Case{}, nil
}
func (f *fakeStore) CreateConfirmationRun(context.Context, store.ConfirmationRun, string) (store.ConfirmationRun, error) {
	return store.ConfirmationRun{}, nil
}
func (f *fakeStore) ListConfirmationRuns(context.Context, int) ([]store.ConfirmationRun, error) {
	return []store.ConfirmationRun{}, nil
}
func (f *fakeStore) GetConfirmationRun(context.Context, string) (store.ConfirmationRun, error) {
	return store.ConfirmationRun{}, nil
}
func (f *fakeStore) RecordConfirmationReply(context.Context, string, store.ConfirmationReply) (store.Confirmation, error) {
	return store.Confirmation{}, nil
}
func (f *fakeStore) ConfirmationDifferences(context.Context, string) ([]store.Confirmation, error) {
	return []store.Confirmation{}, nil
}
func (f *fakeStore) CreateDownPayment(context.Context, store.DownPayment, string) (store.DownPayment, error) {
	return store.DownPayment{}, nil
}
func (f *fakeStore) ListDownPayments(context.Context, store.DownPaymentFilter, int) ([]store.DownPayment, error) {
	return []store.DownPayment{}, nil
}
func (f *fakeStore) GetDownPayment(context.Context, string) (store.DownPayment, error) {
	return store.DownPayment{}, nil
}
func (f *fakeStore) ReceiveDownPayment(context.Context, string, int, string) (store.DownPayment, error) {
	return store.DownPayment{}, nil
}
func (f *fakeStore) CancelDownPayment(context.Context, string, int) (store.DownPayment, error) {
	return store.DownPayment{}, nil
}
func (f *fakeStore) ClearDownPayment(context.Context, string, int, string, *decimal.Decimal) (store.DownPayment, error) {
	return store.DownPayment{}, nil
}
func (f *fakeStore) ReleaseDownPayment(context.Context, string, int) (store.DownPayment, error) {
	return store.DownPayment{}, nil
}

// slice3Fake records what the case, confirmation and down payment handlers forward.
type slice3Fake struct {
	fakeStore
	createdCase store.Case
	owner       string
	note        *string
	run         store.ConfirmationRun
	reply       store.ConfirmationReply
	action      string
	version     int
	received    string
	clearItem   string
	clearAmount *decimal.Decimal
}

func (f *slice3Fake) CreateCase(_ context.Context, in store.Case, _ string) (store.Case, error) {
	f.createdCase = in
	in.ID, in.Version, in.Status = ids.New(), 1, "OPEN"
	return in, nil
}
func (f *slice3Fake) AssignCase(_ context.Context, id string, version int, owner string) (store.Case, error) {
	f.action, f.version, f.owner = "assign", version, owner
	return store.Case{ID: id, Version: version + 1, Owner: owner, Status: "OPEN"}, nil
}
func (f *slice3Fake) CloseCase(_ context.Context, id string, version int, note *string) (store.Case, error) {
	f.action, f.version, f.note = "close", version, note
	return store.Case{ID: id, Version: version + 1, Status: "CLOSED"}, nil
}
func (f *slice3Fake) CreateConfirmationRun(_ context.Context, in store.ConfirmationRun, _ string) (store.ConfirmationRun, error) {
	f.run = in
	in.ID = testRun
	return in, nil
}
func (f *slice3Fake) RecordConfirmationReply(_ context.Context, runID string, in store.ConfirmationReply) (store.Confirmation, error) {
	f.reply = in
	return store.Confirmation{ID: in.ConfirmationID, RunID: runID, Status: in.Reply}, nil
}
func (f *slice3Fake) ReceiveDownPayment(_ context.Context, id string, version int, date string) (store.DownPayment, error) {
	f.action, f.version, f.received = "receive", version, date
	return store.DownPayment{ID: id, Version: version + 1, Status: "RECEIVED"}, nil
}
func (f *slice3Fake) ClearDownPayment(_ context.Context, id string, version int, item string, amount *decimal.Decimal) (store.DownPayment, error) {
	f.action, f.version, f.clearItem, f.clearAmount = "clear", version, item, amount
	return store.DownPayment{ID: id, Version: version + 1, Status: "CLEARED"}, nil
}
func (f *slice3Fake) ReleaseDownPayment(_ context.Context, id string, version int) (store.DownPayment, error) {
	f.action, f.version = "release", version
	return store.DownPayment{ID: id, Version: version + 1, Status: "RELEASED"}, nil
}

const (
	testCase = "00000000-0000-0000-0000-000000000010"
	testCust = "00000000-0000-0000-0000-0000000000c1"
	testConf = "00000000-0000-0000-0000-000000000012"
	testDown = "00000000-0000-0000-0000-000000000013"
)

func TestCreateCase_Validation(t *testing.T) {
	key := map[string]string{"Idempotency-Key": "c-1"}
	body := func(extra string) string {
		return `{"legalEntityId":"` + testLE + `","customerId":"` + testCust + `","category":"COLLECTION","owner":" ana ","reason":"Overdue 60 days"` + extra + `}`
	}
	tests := []struct {
		name    string
		body    string
		headers map[string]string
		duties  string
		status  int
		field   string
	}{
		{"missing key", body(`,"currency":"USD"`), nil, "CollectionsMaintain", http.StatusBadRequest, "Idempotency-Key"},
		{"without the duty", body(`,"currency":"USD"`), key, "ARView", http.StatusForbidden, ""},
		{"unknown category", strings.Replace(body(`,"currency":"USD"`), "COLLECTION", "LEGAL", 1), key, "CollectionsMaintain", http.StatusBadRequest, "category"},
		{"no currency and no item", body(""), key, "CollectionsMaintain", http.StatusBadRequest, "currency"},
		{"bad priority", body(`,"currency":"USD","priority":"URGENT"`), key, "CollectionsMaintain", http.StatusBadRequest, "priority"},
		{"negative attached amount", body(`,"currency":"USD","attachedAmount":"-1"`), key, "CollectionsMaintain", http.StatusBadRequest, "attachedAmount"},
		{"item supplies the currency", body(`,"openItemId":"` + testItem + `"`), key, "CollectionsMaintain", http.StatusCreated, ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			f := &slice3Fake{}
			rec := do(router(f), http.MethodPost, "/v1/receivables/cases", tc.body, tc.duties, tc.headers)
			if rec.Code != tc.status {
				t.Fatalf("status = %d, want %d: %s", rec.Code, tc.status, rec.Body.String())
			}
			if tc.field != "" && !strings.Contains(rec.Body.String(), tc.field) {
				t.Fatalf("want field %q in %s", tc.field, rec.Body.String())
			}
			if tc.status == http.StatusCreated && (f.createdCase.Owner != "ana" || f.createdCase.Priority != "NORMAL") {
				t.Fatalf("forwarded %+v, want the owner trimmed and priority NORMAL", f.createdCase)
			}
		})
	}
}

func TestCaseActions_IfMatchDutyAndBodies(t *testing.T) {
	f := &slice3Fake{}
	h := router(f)
	base := "/v1/receivables/cases/" + testCase
	match := map[string]string{"If-Match": `"3"`}
	if rec := do(h, http.MethodPost, base+":close", "", "CollectionsMaintain", nil); rec.Code != http.StatusPreconditionRequired {
		t.Fatalf("close without If-Match: %d", rec.Code)
	}
	if rec := do(h, http.MethodPost, base+":close", "", "ARView", match); rec.Code != http.StatusForbidden {
		t.Fatalf("close without CollectionsMaintain: %d", rec.Code)
	}
	if rec := do(h, http.MethodPost, base+":assign", `{"owner":"  "}`, "CollectionsMaintain", match); rec.Code != http.StatusBadRequest {
		t.Fatalf("assign to nobody: %d", rec.Code)
	}
	rec := do(h, http.MethodPost, base+":assign", `{"owner":"ben"}`, "CollectionsMaintain", match)
	if rec.Code != http.StatusOK || f.action != "assign" || f.owner != "ben" || f.version != 3 || rec.Header().Get("ETag") != `"4"` {
		t.Fatalf("assign: %d %s owner %q version %d etag %q", rec.Code, f.action, f.owner, f.version, rec.Header().Get("ETag"))
	}
	// A close with no body is valid; a note is carried when given.
	if rec := do(h, http.MethodPost, base+":close", "", "CollectionsMaintain", match); rec.Code != http.StatusOK || f.action != "close" || f.note != nil {
		t.Fatalf("close without body: %d note %v", rec.Code, f.note)
	}
	if rec := do(h, http.MethodPost, base+":close", `{"note":"paid in full"}`, "CollectionsMaintain", match); rec.Code != http.StatusOK || f.note == nil || *f.note != "paid in full" {
		t.Fatalf("close with note: %d", rec.Code)
	}
	if rec := do(h, http.MethodPost, base+":escalate", "", "CollectionsMaintain", match); rec.Code != http.StatusNotFound {
		t.Fatalf("unknown action: %d", rec.Code)
	}
}

func TestConfirmationRun_DefaultsAndValidation(t *testing.T) {
	f := &slice3Fake{}
	h := router(f)
	key := map[string]string{"Idempotency-Key": "bc-1"}
	good := `{"legalEntityId":"` + testLE + `","asOfDate":"2027-03-31"}`
	rec := do(h, http.MethodPost, "/v1/receivables/balance-confirmations", good, "BalanceConfirmationRun", key)
	if rec.Code != http.StatusCreated || !f.run.Threshold.Equal(decimal.NewFromInt(10000)) || f.run.ReminderDays != 14 {
		t.Fatalf("create: %d threshold %s reminder %d", rec.Code, f.run.Threshold, f.run.ReminderDays)
	}
	if rec := do(h, http.MethodPost, "/v1/receivables/balance-confirmations", good, "ARView", key); rec.Code != http.StatusForbidden {
		t.Fatalf("create without BalanceConfirmationRun: %d", rec.Code)
	}
	for field, body := range map[string]string{
		"asOfDate":     `{"legalEntityId":"` + testLE + `","asOfDate":"31/03/2027"}`,
		"threshold":    `{"legalEntityId":"` + testLE + `","asOfDate":"2027-03-31","threshold":"-1"}`,
		"sampleSize":   `{"legalEntityId":"` + testLE + `","asOfDate":"2027-03-31","sampleSize":0}`,
		"reminderDays": `{"legalEntityId":"` + testLE + `","asOfDate":"2027-03-31","reminderDays":61}`,
	} {
		if rec := do(h, http.MethodPost, "/v1/receivables/balance-confirmations", body, "BalanceConfirmationRun", key); rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), field) {
			t.Errorf("%s: %d %s", field, rec.Code, rec.Body.String())
		}
	}
}

func TestConfirmationReply_ValidatesAndForwards(t *testing.T) {
	base := "/v1/receivables/balance-confirmations/" + testRun
	tests := []struct {
		name   string
		path   string
		body   string
		duties string
		status int
	}{
		{"agreed", base + ":reply", `{"confirmationId":"` + testConf + `","reply":"AGREED"}`, "BalanceConfirmationRun", http.StatusOK},
		{"disagreed with balance", base + ":reply", `{"confirmationId":"` + testConf + `","reply":"DISAGREED","customerBalance":"9500.00","comment":"credit note missing"}`, "BalanceConfirmationRun", http.StatusOK},
		{"disagreed without balance", base + ":reply", `{"confirmationId":"` + testConf + `","reply":"DISAGREED"}`, "BalanceConfirmationRun", http.StatusBadRequest},
		{"no reply is not a reply", base + ":reply", `{"confirmationId":"` + testConf + `","reply":"NO_REPLY"}`, "BalanceConfirmationRun", http.StatusBadRequest},
		{"bad confirmation id", base + ":reply", `{"confirmationId":"x","reply":"AGREED"}`, "BalanceConfirmationRun", http.StatusBadRequest},
		{"without the duty", base + ":reply", `{"confirmationId":"` + testConf + `","reply":"AGREED"}`, "ARView", http.StatusForbidden},
		{"unknown action", base + ":remind", `{}`, "BalanceConfirmationRun", http.StatusNotFound},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			f := &slice3Fake{}
			rec := do(router(f), http.MethodPost, tc.path, tc.body, tc.duties, nil)
			if rec.Code != tc.status {
				t.Fatalf("status = %d, want %d: %s", rec.Code, tc.status, rec.Body.String())
			}
			if tc.name == "disagreed with balance" && (f.reply.CustomerBalance == nil || !f.reply.CustomerBalance.Equal(decimal.RequireFromString("9500")) || f.reply.Reply != "DISAGREED") {
				t.Fatalf("forwarded %+v", f.reply)
			}
		})
	}
}

func TestDownPaymentActions_DutiesPerAction(t *testing.T) {
	base := "/v1/receivables/down-payments/" + testDown
	match := map[string]string{"If-Match": `"2"`}
	tests := []struct {
		name   string
		path   string
		body   string
		duties string
		status int
	}{
		{"receive needs ARPaymentPost", base + ":receive", "", "ARSettle", http.StatusForbidden},
		{"receive with a date", base + ":receive", `{"receivedDate":"2027-03-02"}`, "ARPaymentPost", http.StatusOK},
		{"receive with a bad date", base + ":receive", `{"receivedDate":"2 March"}`, "ARPaymentPost", http.StatusBadRequest},
		{"clear needs ARSettle", base + ":clear", `{"openItemId":"` + testItem + `"}`, "ARPaymentPost", http.StatusForbidden},
		{"clear the proposal", base + ":clear", `{"openItemId":"` + testItem + `"}`, "ARSettle", http.StatusOK},
		{"clear a zero amount", base + ":clear", `{"openItemId":"` + testItem + `","amount":"0"}`, "ARSettle", http.StatusBadRequest},
		{"clear without an item", base + ":clear", `{}`, "ARSettle", http.StatusBadRequest},
		{"release needs ARRefund", base + ":release", "", "ARSettle", http.StatusForbidden},
		{"release", base + ":release", "", "ARRefund", http.StatusOK},
		{"unknown action", base + ":refund", "", "ARRefund", http.StatusNotFound},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			f := &slice3Fake{}
			rec := do(router(f), http.MethodPost, tc.path, tc.body, tc.duties, match)
			if rec.Code != tc.status {
				t.Fatalf("status = %d, want %d: %s", rec.Code, tc.status, rec.Body.String())
			}
			if tc.status == http.StatusOK && (f.version != 2 || rec.Header().Get("ETag") != `"3"`) {
				t.Fatalf("version %d etag %q", f.version, rec.Header().Get("ETag"))
			}
			if tc.name == "receive with a date" && f.received != "2027-03-02" {
				t.Fatalf("received date %q", f.received)
			}
			if tc.name == "clear the proposal" && (f.clearItem != testItem || f.clearAmount != nil) {
				t.Fatalf("clear forwarded %q %v, want the item and no amount", f.clearItem, f.clearAmount)
			}
		})
	}
	if rec := do(router(&slice3Fake{}), http.MethodPost, base+":release", "", "ARRefund", nil); rec.Code != http.StatusPreconditionRequired {
		t.Fatalf("release without If-Match: %d", rec.Code)
	}
}

func TestCreateDownPayment_Validation(t *testing.T) {
	key := map[string]string{"Idempotency-Key": "dp-1"}
	body := `{"legalEntityId":"` + testLE + `","customerId":"` + testCust + `","kind":"DOWN_PAYMENT","currency":"USD","amount":"2000.00"}`
	h := router(&slice3Fake{})
	if rec := do(h, http.MethodPost, "/v1/receivables/down-payments", body, "ARPaymentPost", key); rec.Code != http.StatusCreated {
		t.Fatalf("valid: %d %s", rec.Code, rec.Body.String())
	}
	for field, bad := range map[string]string{
		"kind":         strings.Replace(body, "DOWN_PAYMENT", "PREPAYMENT", 1),
		"amount":       strings.Replace(body, `"2000.00"`, `"0"`, 1),
		"currency":     strings.Replace(body, `"USD"`, `"usd"`, 1),
		"salesOrderId": strings.Replace(body, `"kind"`, `"salesOrderId":"SO-4000","kind"`, 1),
	} {
		if rec := do(h, http.MethodPost, "/v1/receivables/down-payments", bad, "ARPaymentPost", key); rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), field) {
			t.Errorf("%s: %d %s", field, rec.Code, rec.Body.String())
		}
	}
	if rec := do(h, http.MethodPost, "/v1/receivables/down-payments", body, "ARView", key); rec.Code != http.StatusForbidden {
		t.Fatalf("without ARPaymentPost: %d", rec.Code)
	}
}

func TestSlice3Lists_ValidateFiltersAndNeedView(t *testing.T) {
	h := router(&slice3Fake{})
	tests := []struct {
		path   string
		duties string
		status int
	}{
		{"/v1/receivables/cases?status=OPEN&category=DISPUTE", "ARView", http.StatusOK},
		{"/v1/receivables/cases?status=PENDING", "ARView", http.StatusBadRequest},
		{"/v1/receivables/cases?customer=x", "ARView", http.StatusBadRequest},
		{"/v1/receivables/cases", "CollectionsMaintain", http.StatusForbidden},
		{"/v1/receivables/cases/nope", "ARView", http.StatusNotFound},
		{"/v1/receivables/balance-confirmations", "ARView", http.StatusOK},
		{"/v1/receivables/balance-confirmations?limit=0", "ARView", http.StatusBadRequest},
		{"/v1/receivables/balance-confirmations/" + testRun + "/differences", "ARView", http.StatusOK},
		{"/v1/receivables/balance-confirmations/nope/differences", "ARView", http.StatusNotFound},
		{"/v1/receivables/down-payments?kind=SECURITY_DEPOSIT&status=RECEIVED", "ARView", http.StatusOK},
		{"/v1/receivables/down-payments?status=OPEN", "ARView", http.StatusBadRequest},
		{"/v1/receivables/down-payments", "", http.StatusForbidden},
	}
	for _, tc := range tests {
		t.Run(tc.path, func(t *testing.T) {
			rec := do(h, http.MethodGet, tc.path, "", tc.duties, nil)
			if rec.Code != tc.status {
				t.Fatalf("status = %d, want %d: %s", rec.Code, tc.status, rec.Body.String())
			}
			if tc.status == http.StatusOK && !strings.Contains(rec.Body.String(), `"items":[]`) {
				t.Errorf("list body = %s, want an items array", rec.Body.String())
			}
		})
	}
}
