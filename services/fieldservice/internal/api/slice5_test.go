package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/fieldservice/internal/store"
)

// Validation tests stop before the store, so the nil store of newTestRouter is never reached.

func TestSlice5_ValidationBeforeStore(t *testing.T) {
	h := newTestRouter()
	key := map[string]string{"Idempotency-Key": "k"}
	guarded := map[string]string{"Idempotency-Key": "k", "If-Match": `"1"`}
	claim := "/v1/service/supplier-claims/" + uuid1
	cases := []struct {
		name, method, path, body string
		headers                  map[string]string
		duty                     string
		want                     []string
	}{
		{"claim without vendor, kind or sources", http.MethodPost, "/v1/service/supplier-claims", `{"laborRate":"-1","windowDays":400}`, key, dutyClaim,
			[]string{"vendorPartyId", "sourceKind", "sourceIds", "laborRate", "windowDays"}},
		{"SERVICE_WO claim takes nothing from the caller", http.MethodPost, "/v1/service/supplier-claims",
			`{"vendorPartyId":"` + uuid1 + `","sourceKind":"SERVICE_WO","sourceIds":["` + uuid1 + `"],"currency":"USD"}`, key, dutyClaim, []string{"sourceKind"}},
		{"repeated source", http.MethodPost, "/v1/service/supplier-claims",
			`{"vendorPartyId":"` + uuid1 + `","sourceKind":"SERVICE_WO","sourceIds":["` + uuid1 + `","` + uuid1 + `"]}`, key, dutyClaim, []string{"sourceIds"}},
		{"EAM_WO claim needs its facts", http.MethodPost, "/v1/service/supplier-claims",
			`{"vendorPartyId":"` + uuid1 + `","sourceKind":"EAM_WO","sourceIds":["` + uuid1 + `"],"currency":"usd","lines":[{"itemId":"x","partAmount":"-5"}]}`,
			key, dutyClaim, []string{"legalEntityId", "currency", "completedOn", "lines[0].itemId", "lines[0]"}},
		{"unknown claim status filter", http.MethodGet, "/v1/service/supplier-claims?status=PAID", "", nil, dutyInquire, []string{"status"}},
		{"decision outside the three", http.MethodPost, claim + "/decision", `{"decision":"MAYBE","acceptedAmounts":["-1"]}`, guarded, dutyClaim,
			[]string{"decision", "acceptedAmounts[0]"}},
		{"rejection without a reason", http.MethodPost, claim + "/decision", `{"decision":"REJECTED"}`, guarded, dutyClaim, []string{"reason"}},
		{"settle without the credit note", http.MethodPost, claim + ":settle", `{"amount":"0","currency":"US"}`, guarded, dutyClaim,
			[]string{"creditNoteId", "vendorReference", "amount", "currency"}},
		{"mailbox needs an address", http.MethodPost, "/v1/case-mailboxes", `{"address":"not-mail","legalEntityId":"x"}`, nil, dutyCaseSetup,
			[]string{"address", "legalEntityId", "categoryId"}},
		{"inbound message facts", http.MethodPost, "/v1/inbound-messages",
			`{"mailbox":"service","messageId":"has space","fromAddress":"","attachments":[{"name":"","objectRef":"o"}]}`, nil, dutyGenCreate,
			[]string{"mailbox", "messageId", "fromAddress", "receivedAt", "attachments[0]"}},
		{"inbound list route", http.MethodGet, "/v1/inbound-messages?route=LOST&caseId=x", "", nil, dutyGenMaintain, []string{"route", "caseId"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := do(t, h, tc.method, tc.path, tc.body, tc.headers, tc.duty)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
			}
			_, fields := problemOf(t, rec)
			for _, f := range tc.want {
				if fields[f] == "" {
					t.Errorf("errors %v lack %s", fields, f)
				}
			}
		})
	}
}

func TestSlice5_HeadersAndDuties(t *testing.T) {
	h := newTestRouter()
	claim := "/v1/service/supplier-claims/" + uuid1
	if rec := do(t, h, http.MethodPost, claim+":submit", `{}`, map[string]string{"Idempotency-Key": "k"}, dutyClaim); rec.Code != http.StatusPreconditionRequired {
		t.Fatalf("submit without If-Match: %d", rec.Code)
	}
	if rec := do(t, h, http.MethodPost, claim+":close", ``, map[string]string{"If-Match": `"1"`}, dutyClaim); rec.Code != http.StatusBadRequest {
		t.Fatalf("close without Idempotency-Key: %d", rec.Code)
	}
	for _, tc := range []struct{ method, path, duty string }{
		{http.MethodPost, "/v1/service/supplier-claims", dutyInquire},
		{http.MethodPost, claim + "/decision", dutyInquire},
		{http.MethodGet, "/v1/service/supplier-claims", dutyGenMaintain},
		{http.MethodPost, "/v1/case-mailboxes", dutyGenMaintain},
		{http.MethodPost, "/v1/inbound-messages", dutyGenMaintain},
		{http.MethodGet, "/v1/inbound-messages", dutyGenCreate},
	} {
		if rec := do(t, h, tc.method, tc.path, `{}`, nil, tc.duty); rec.Code != http.StatusForbidden {
			t.Errorf("%s %s with %s: %d", tc.method, tc.path, tc.duty, rec.Code)
		}
	}
}

// AC-45.13 and AC-45.14 over HTTP against Postgres as passage_app.
func TestAPI_ClaimAndEmailIntakeFlow(t *testing.T) {
	h, as := genericRouter(t)
	clerk := as(ids.New(), dutyClaim)
	admin := as(ids.New(), dutyCaseSetup)
	hub := as(ids.New(), dutyGenCreate)
	agent := as(ids.New(), dutyGenMaintain)
	decode := func(rec *httptest.ResponseRecorder, v any) {
		t.Helper()
		if err := json.Unmarshal(rec.Body.Bytes(), v); err != nil {
			t.Fatalf("%v: %s", err, rec.Body.String())
		}
	}

	// An EAM work order claim: 180.00 part and 1.5 hours at 60.00 claim 270.00.
	today := time.Now().UTC().Format(dateLayout)
	body := `{"vendorPartyId":"` + ids.New() + `","sourceKind":"EAM_WO","sourceIds":["` + ids.New() + `"],"laborRate":"60.00",
		"legalEntityId":"` + ids.New() + `","currency":"USD","completedOn":"` + today + `","failureCode":"BEARING",
		"lines":[{"itemId":"` + ids.New() + `","serial":"PUMP-1","partAmount":"180.00","laborHours":"1.5"}]}`
	rec := call(t, h, clerk, http.MethodPost, "/v1/service/supplier-claims", body, map[string]string{"Idempotency-Key": "claim"})
	var c store.SupplierClaim
	decode(rec, &c)
	if rec.Code != http.StatusCreated || rec.Header().Get("ETag") != `"1"` || c.ClaimedTotal.String() != "270" || c.Lines[0].FailureCode != "BEARING" {
		t.Fatalf("create: %d %s", rec.Code, rec.Body.String())
	}
	move := func(path, body, etag, key string) *httptest.ResponseRecorder {
		return call(t, h, clerk, http.MethodPost, "/v1/service/supplier-claims/"+c.ID+path, body, map[string]string{"Idempotency-Key": key, "If-Match": etag})
	}
	if rec := move(":submit", `{"vendorReference":"VR-1"}`, `"1"`, "sub"); rec.Code != http.StatusOK || rec.Header().Get("ETag") != `"2"` {
		t.Fatalf("submit: %d %s", rec.Code, rec.Body.String())
	}
	if rec := move(":submit", `{"vendorReference":"VR-1"}`, `"1"`, "sub"); rec.Code != http.StatusOK || rec.Header().Get("Idempotent-Replayed") != "true" {
		t.Fatalf("submit replay: %d %v", rec.Code, rec.Header())
	}
	if code, _ := problemOf(t, move("/decision", `{"decision":"PARTIALLY_ACCEPTED","acceptedAmounts":["300"]}`, `"2"`, "d0")); code != "SVC-SWC-003" {
		t.Fatalf("over-acceptance: %s", code)
	}
	if rec := move("/decision", `{"decision":"PARTIALLY_ACCEPTED","acceptedAmounts":["250.00"]}`, `"2"`, "d1"); rec.Code != http.StatusOK {
		t.Fatalf("decision: %d %s", rec.Code, rec.Body.String())
	}
	rec = move(":settle", `{"creditNoteId":"CN-9","vendorReference":"VR-1","amount":"250.00","currency":"USD"}`, `"3"`, "st")
	decode(rec, &c)
	if rec.Code != http.StatusOK || c.Status != "SETTLED" || c.RecoveredAmount.String() != "250" {
		t.Fatalf("settle: %d %s", rec.Code, rec.Body.String())
	}
	if rec := call(t, h, as(ids.New(), dutyInquire), http.MethodGet, "/v1/service/supplier-claims/"+c.ID, "", nil); rec.Code != http.StatusOK ||
		rec.Header().Get("ETag") != `"4"` {
		t.Fatalf("read: %d %v", rec.Code, rec.Header())
	}

	// Email intake: mailbox, new case with acknowledgment, token reply attached.
	rec = call(t, h, admin, http.MethodPost, "/v1/case-categories", `{"code":"INQ","name":"Inquiries","categoryType":"INTERNAL"}`,
		map[string]string{"Idempotency-Key": "cat"})
	var cat store.Category
	decode(rec, &cat)
	mailbox := `{"address":" Service@Example.test ","legalEntityId":"` + ids.New() + `","categoryId":"` + cat.ID + `"}`
	if rec := call(t, h, admin, http.MethodPost, "/v1/case-mailboxes", mailbox, nil); rec.Code != http.StatusCreated {
		t.Fatalf("mailbox: %d %s", rec.Code, rec.Body.String())
	}
	if rec := call(t, h, admin, http.MethodPost, "/v1/case-mailboxes", mailbox, nil); rec.Code != http.StatusOK {
		t.Fatalf("mailbox update: %d %s", rec.Code, rec.Body.String())
	}
	mail := func(id, subject string) *httptest.ResponseRecorder {
		return call(t, h, hub, http.MethodPost, "/v1/inbound-messages", `{"mailbox":"service@example.test","messageId":"`+id+
			`","fromAddress":"Pat@Customer.test","subject":"`+subject+`","receivedAt":"2026-10-28T09:00:00Z"}`, nil)
	}
	var first, reply struct {
		store.InboundMessage
		Message *infoMessage `json:"message"`
	}
	rec = mail("<m1@c>", "pump noise")
	decode(rec, &first)
	if rec.Code != http.StatusCreated || !first.Acknowledge || first.Route != "NEW_CASE" || first.Message != nil || first.FromAddress != "pat@customer.test" {
		t.Fatalf("first mail: %d %s", rec.Code, rec.Body.String())
	}
	rec = mail("<m2@c>", "Re: ["+strings.Replace(first.CaseNumber, "CS-00", "CS-", 1)+"] pump noise")
	decode(rec, &reply)
	if rec.Code != http.StatusOK || reply.CaseID != first.CaseID || reply.Acknowledge || reply.Message == nil || reply.Message.Code != "CASE-MAIL-001" {
		t.Fatalf("token reply: %d %s", rec.Code, rec.Body.String())
	}
	if rec := mail("<m2@c>", "Re: again"); rec.Code != http.StatusOK || rec.Header().Get("Idempotent-Replayed") != "true" {
		t.Fatalf("redelivery: %d %v", rec.Code, rec.Header())
	}
	var page store.Page[store.InboundMessage]
	rec = call(t, h, agent, http.MethodGet, "/v1/inbound-messages?caseId="+first.CaseID, "", nil)
	decode(rec, &page)
	if rec.Code != http.StatusOK || len(page.Items) != 2 {
		t.Fatalf("case messages: %d %s", rec.Code, rec.Body.String())
	}
}
