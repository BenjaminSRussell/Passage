package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/BenjaminSRussell/passage/platform/ids"
)

const rmaInvoiceID = "00000000-0000-0000-0000-0000000000b1"

func rmaBody(le, gross, restocking, net string) string {
	return `{"rmaId":"` + ids.New() + `","rmaNumber":"RMA-0000001","legalEntityId":"` + le + `","invoiceNumber":"INV-30001","invoiceId":"` + rmaInvoiceID + `","postingDate":"2027-03-01",
		"gross":"` + gross + `","restocking":"` + restocking + `","net":"` + net + `"}`
}

func withTax(body, lines string) string {
	return strings.Replace(body, `"net":`, `"taxLines":`+lines+`,"net":`, 1)
}

func withLines(body, lines string) string {
	return strings.Replace(body, `"net":`, `"invoiceLines":`+lines+`,"net":`, 1)
}

func TestPostRMACredit_Validation(t *testing.T) {
	h := router(&fakeStore{})
	tests := []struct {
		name   string
		body   string
		duties string
		status int
		field  string
	}{
		{"payment duty is not enough", rmaBody(testLE, "400.00", "60.00", "340.00"), "receivables.payment.post", http.StatusForbidden, ""},
		{"view duty is not enough", rmaBody(testLE, "400.00", "60.00", "340.00"), "receivables.view", http.StatusForbidden, ""},
		{"legal entity outside the caller's scope", rmaBody("00000000-0000-0000-0000-0000000000ff", "400.00", "60.00", "340.00"), "ar.rmacredit.post", http.StatusForbidden, ""},
		{"missing invoice number", strings.Replace(rmaBody(testLE, "400.00", "60.00", "340.00"), `"invoiceNumber":"INV-30001"`, `"invoiceNumber":""`, 1), "ar.rmacredit.post", http.StatusBadRequest, "invoiceNumber"},
		{"bad posting date", strings.Replace(rmaBody(testLE, "400.00", "60.00", "340.00"), "2027-03-01", "03/01/2027", 1), "ar.rmacredit.post", http.StatusBadRequest, "postingDate"},
		{"negative net", rmaBody(testLE, "100.00", "200.00", "-100.00"), "ar.rmacredit.post", http.StatusUnprocessableEntity, "amounts"},
		{"negative restocking", rmaBody(testLE, "100.00", "-10.00", "110.00"), "ar.rmacredit.post", http.StatusUnprocessableEntity, "amounts"},
		{"gross is not restocking plus net", rmaBody(testLE, "400.00", "60.00", "300.00"), "ar.rmacredit.post", http.StatusUnprocessableEntity, "amounts"},
		{"zero net", rmaBody(testLE, "60.00", "60.00", "0"), "ar.rmacredit.post", http.StatusUnprocessableEntity, "amounts"},
		{"valid", rmaBody(testLE, "400.00", "60.00", "340.00"), "ar.rmacredit.post", http.StatusCreated, ""},
		{"valid with tax lines", withTax(rmaBody(testLE, "400.00", "60.00", "340.00"), `[{"code":"DE19","amount":"57.00"},{"code":"DE7","amount":"7.00"}]`), "ar.rmacredit.post", http.StatusCreated, ""},
		{"tax lines without the billing invoice id", withTax(strings.Replace(rmaBody(testLE, "400.00", "60.00", "340.00"), rmaInvoiceID, "", 1), `[{"code":"DE19","amount":"57.00"}]`), "ar.rmacredit.post", http.StatusBadRequest, "invoiceId"},
		{"tax line without amount", withTax(rmaBody(testLE, "400.00", "60.00", "340.00"), `[{"code":"DE19","amount":"0"}]`), "ar.rmacredit.post", http.StatusUnprocessableEntity, "taxLines"},
		{"tax code listed twice", withTax(rmaBody(testLE, "400.00", "60.00", "340.00"), `[{"code":"DE19","amount":"1"},{"code":"DE19","amount":"2"}]`), "ar.rmacredit.post", http.StatusUnprocessableEntity, "taxLines"},
		{"valid with invoice lines", withLines(rmaBody(testLE, "400.00", "60.00", "340.00"), `[{"lineNo":1,"qty":"2"}]`), "ar.rmacredit.post", http.StatusCreated, ""},
		{"invoice lines without the billing invoice id", withLines(strings.Replace(rmaBody(testLE, "400.00", "60.00", "340.00"), rmaInvoiceID, "", 1), `[{"lineNo":1,"qty":"2"}]`), "ar.rmacredit.post", http.StatusBadRequest, "invoiceId"},
		{"invoice line listed twice", withLines(rmaBody(testLE, "400.00", "60.00", "340.00"), `[{"lineNo":1,"qty":"1"},{"lineNo":1,"qty":"2"}]`), "ar.rmacredit.post", http.StatusUnprocessableEntity, "invoiceLines"},
		{"invoice line without a quantity", withLines(rmaBody(testLE, "400.00", "60.00", "340.00"), `[{"lineNo":1,"qty":"0"}]`), "ar.rmacredit.post", http.StatusUnprocessableEntity, "invoiceLines"},
		{"tax line with an unknown field", withTax(rmaBody(testLE, "400.00", "60.00", "340.00"), `[{"code":"DE19","amount":"1","rate":"19"}]`), "ar.rmacredit.post", http.StatusBadRequest, ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rec := do(h, http.MethodPost, "/v1/receivables/rma-credits", tc.body, tc.duties, nil)
			if rec.Code != tc.status {
				t.Fatalf("status = %d, want %d: %s", rec.Code, tc.status, rec.Body.String())
			}
			if tc.field != "" {
				var p struct {
					Errors map[string]string `json:"errors"`
				}
				if err := json.Unmarshal(rec.Body.Bytes(), &p); err != nil || p.Errors[tc.field] == "" {
					t.Fatalf("want field error %q, got %s", tc.field, rec.Body.String())
				}
			}
		})
	}
}

func TestPostRMACredit_RejectsCallerSuppliedCustomerAndLedger(t *testing.T) {
	h := router(&fakeStore{})
	body := strings.Replace(rmaBody(testLE, "400.00", "60.00", "340.00"), `"rmaNumber"`, `"customerId":"`+ids.New()+`","ledgerId":"`+ids.New()+`","rmaNumber"`, 1)
	rec := do(h, http.MethodPost, "/v1/receivables/rma-credits", body, "ar.rmacredit.post", nil)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 for body fields the service now derives: %s", rec.Code, rec.Body.String())
	}
}

func TestRMACreditOperatorRoutes(t *testing.T) {
	f := &fakeStore{}
	h := router(f)
	id := ids.New()
	tests := []struct {
		name, method, path, body, duties string
		status                           int
	}{
		{"list needs receivables.view", http.MethodGet, "/v1/receivables/rma-credits", "", "receivables.payment-file.import", http.StatusForbidden},
		{"list parked", http.MethodGet, "/v1/receivables/rma-credits?status=PENDING&parked=true", "", "receivables.view", http.StatusOK},
		{"list bad status", http.MethodGet, "/v1/receivables/rma-credits?status=OPEN", "", "receivables.view", http.StatusBadRequest},
		{"list bad parked", http.MethodGet, "/v1/receivables/rma-credits?parked=yes", "", "receivables.view", http.StatusBadRequest},
		{"list other legal entity", http.MethodGet, "/v1/receivables/rma-credits?legalEntityId=00000000-0000-0000-0000-0000000000ff", "", "receivables.view", http.StatusForbidden},
		{"retry needs receivables.payment.post", http.MethodPost, "/v1/receivables/rma-credits/" + id + ":retry", "", "receivables.view", http.StatusForbidden},
		{"retry service duty is not enough", http.MethodPost, "/v1/receivables/rma-credits/" + id + ":retry", "", "ar.rmacredit.post", http.StatusForbidden},
		{"backfill by RMA is a service call", http.MethodPost, "/v1/receivables/rma-credits:backfill-reservation", `{"rmaId":"` + id + `","legalEntityId":"` + testLE + `","invoiceId":"` + id + `","invoiceLines":[{"lineNo":1,"qty":"1"}]}`, "receivables.payment.post", http.StatusForbidden},
		{"backfill by RMA bad ids", http.MethodPost, "/v1/receivables/rma-credits:backfill-reservation", `{"rmaId":"x","legalEntityId":"` + testLE + `"}`, "ar.rmacredit.backfill", http.StatusBadRequest},
		{"backfill by RMA other legal entity", http.MethodPost, "/v1/receivables/rma-credits:backfill-reservation", `{"rmaId":"` + id + `","legalEntityId":"00000000-0000-0000-0000-0000000000ff","invoiceId":"` + id + `","invoiceLines":[{"lineNo":1,"qty":"1"}]}`, "ar.rmacredit.backfill", http.StatusForbidden},
		{"backfill by RMA", http.MethodPost, "/v1/receivables/rma-credits:backfill-reservation", `{"rmaId":"` + id + `","legalEntityId":"` + testLE + `","invoiceId":"` + id + `","invoiceLines":[{"lineNo":1,"qty":"1"}]}`, "ar.rmacredit.backfill", http.StatusOK},
		{"retry bad date", http.MethodPost, "/v1/receivables/rma-credits/" + id + ":retry", `{"postingDate":"03/01/2027"}`, "receivables.payment.post", http.StatusBadRequest},
		{"retry unknown action", http.MethodPost, "/v1/receivables/rma-credits/" + id + ":post", "", "receivables.payment.post", http.StatusNotFound},
		{"retry re-dated", http.MethodPost, "/v1/receivables/rma-credits/" + id + ":retry", `{"postingDate":"2027-04-01"}`, "receivables.payment.post", http.StatusOK},
		{"retry as is", http.MethodPost, "/v1/receivables/rma-credits/" + id + ":retry", "", "receivables.payment.post", http.StatusOK},
		{"sweep needs receivables.payment.post", http.MethodPost, "/v1/receivables/rma-credits:sweep", "", "receivables.view", http.StatusForbidden},
		{"sweep", http.MethodPost, "/v1/receivables/rma-credits:sweep", "", "receivables.payment.post", http.StatusOK},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rec := do(h, tc.method, tc.path, tc.body, tc.duties, nil)
			if rec.Code != tc.status {
				t.Fatalf("status = %d, want %d: %s", rec.Code, tc.status, rec.Body.String())
			}
		})
	}
	// An all-scope caller (no legal-entity claim) cannot sign the ledger post, so it may not retry or sweep.
	for _, path := range []string{"/v1/receivables/rma-credits/" + id + ":retry", "/v1/receivables/rma-credits:sweep"} {
		req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(""))
		req.Header.Set("X-Passage-Tenant", testTenant)
		req.Header.Set("X-Passage-Duties", "receivables.payment.post")
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusBadRequest && rec.Code != http.StatusForbidden {
			t.Fatalf("%s without a legal-entity claim: %d %s", path, rec.Code, rec.Body.String())
		}
	}
	if len(f.rmaRetries) != 3 {
		t.Fatalf("an unscoped retry reached the store: %v", f.rmaRetries)
	}
	if !f.rmaFilter.ParkedOnly || f.rmaFilter.Status != "PENDING" {
		t.Fatalf("list filter %+v", f.rmaFilter)
	}
	if len(f.rmaRetries) != 3 || f.rmaRetries[0] != testLE+"|"+id+"|backfill-for|"+id || f.rmaRetries[1] != id+"|2027-04-01" || f.rmaRetries[2] != id+"|" {
		t.Fatalf("retries forwarded %v", f.rmaRetries)
	}
}
