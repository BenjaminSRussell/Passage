package api

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/BenjaminSRussell/passage/platform/ids"
)

func rmaBody(le, gross, restocking, net string) string {
	return `{"rmaId":"` + ids.New() + `","rmaNumber":"RMA-0000001","legalEntityId":"` + le + `","invoiceNumber":"INV-30001","postingDate":"2027-03-01",
		"gross":"` + gross + `","restocking":"` + restocking + `","net":"` + net + `"}`
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
		{"payment duty is not enough", rmaBody(testLE, "400.00", "60.00", "340.00"), "ARPaymentPost", http.StatusForbidden, ""},
		{"view duty is not enough", rmaBody(testLE, "400.00", "60.00", "340.00"), "ARView", http.StatusForbidden, ""},
		{"legal entity outside the caller's scope", rmaBody("00000000-0000-0000-0000-0000000000ff", "400.00", "60.00", "340.00"), "ar.rmacredit.post", http.StatusForbidden, ""},
		{"missing invoice number", strings.Replace(rmaBody(testLE, "400.00", "60.00", "340.00"), `"invoiceNumber":"INV-30001"`, `"invoiceNumber":""`, 1), "ar.rmacredit.post", http.StatusBadRequest, "invoiceNumber"},
		{"bad posting date", strings.Replace(rmaBody(testLE, "400.00", "60.00", "340.00"), "2027-03-01", "03/01/2027", 1), "ar.rmacredit.post", http.StatusBadRequest, "postingDate"},
		{"negative net", rmaBody(testLE, "100.00", "200.00", "-100.00"), "ar.rmacredit.post", http.StatusUnprocessableEntity, "amounts"},
		{"negative restocking", rmaBody(testLE, "100.00", "-10.00", "110.00"), "ar.rmacredit.post", http.StatusUnprocessableEntity, "amounts"},
		{"gross is not restocking plus net", rmaBody(testLE, "400.00", "60.00", "300.00"), "ar.rmacredit.post", http.StatusUnprocessableEntity, "amounts"},
		{"zero net", rmaBody(testLE, "60.00", "60.00", "0"), "ar.rmacredit.post", http.StatusUnprocessableEntity, "amounts"},
		{"valid", rmaBody(testLE, "400.00", "60.00", "340.00"), "ar.rmacredit.post", http.StatusCreated, ""},
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
