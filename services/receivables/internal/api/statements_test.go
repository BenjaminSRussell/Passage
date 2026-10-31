package api

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/BenjaminSRussell/passage/services/receivables/internal/domain"
)

func TestStatement_ForwardsTheQueryAndReturnsSections(t *testing.T) {
	f := &fakeStore{}
	rec := do(router(f), http.MethodGet, "/v1/receivables/statements?legalEntityId="+testLE+"&customer="+testItem+"&asOf=2027-03-31", "", "ARView", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body %s", rec.Code, rec.Body.String())
	}
	if f.statement != (statementCall{legalEntity: testLE, customer: testItem, asOf: "2027-03-31"}) {
		t.Fatalf("store got %+v", f.statement)
	}
	var body struct {
		LegalEntityID string                    `json:"legalEntityId"`
		CustomerID    string                    `json:"customerId"`
		AsOf          string                    `json:"asOf"`
		Sections      []domain.StatementSection `json:"sections"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.CustomerID != testItem || body.LegalEntityID != testLE || body.AsOf != "2027-03-31" {
		t.Errorf("echo = %+v", body)
	}
	if len(body.Sections) != 1 || body.Sections[0].Lines[0].DocumentNumber != "INV-9" || body.Sections[0].ClosingBalance.String() != "100" {
		t.Errorf("sections = %+v", body.Sections)
	}
	// Money travels as decimal strings, never JSON numbers.
	if !strings.Contains(rec.Body.String(), `"closingBalance":"100"`) {
		t.Errorf("closing balance is not a decimal string: %s", rec.Body.String())
	}
}

func TestStatement_DefaultsToToday(t *testing.T) {
	f := &fakeStore{}
	rec := do(router(f), http.MethodGet, "/v1/receivables/statements?legalEntityId="+testLE+"&customer="+testItem, "", "ARView", nil)
	if rec.Code != http.StatusOK || f.statement.asOf != time.Now().UTC().Format("2006-01-02") {
		t.Fatalf("status %d, asOf %q, want today", rec.Code, f.statement.asOf)
	}
}

func TestStatement_RejectsBadQueriesAndChecksDutyAndScope(t *testing.T) {
	h := router(&fakeStore{})
	base := "/v1/receivables/statements?legalEntityId=" + testLE + "&customer=" + testItem
	for path, field := range map[string]string{
		"/v1/receivables/statements?customer=" + testItem:                       "legalEntityId",
		"/v1/receivables/statements?legalEntityId=" + testLE:                    "customer",
		"/v1/receivables/statements?legalEntityId=" + testLE + "&customer=nope": "customer",
		base + "&asOf=31-03-2027":                                                   "asOf",
	} {
		rec := do(h, http.MethodGet, path, "", "ARView", nil)
		if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), `"`+field+`"`) {
			t.Errorf("%s: status %d body %s, want 400 naming %s", path, rec.Code, rec.Body.String(), field)
		}
	}
	if rec := do(h, http.MethodGet, base, "", "ARPaymentPost", nil); rec.Code != http.StatusForbidden {
		t.Errorf("without ARView: status %d, want 403", rec.Code)
	}
	rec := do(h, http.MethodGet, base, "", "ARView", map[string]string{"X-Passage-Legal-Entity": "00000000-0000-0000-0000-0000000000ff"})
	if rec.Code != http.StatusForbidden {
		t.Errorf("other legal entity in scope: status %d, want 403", rec.Code)
	}
}

func TestAgingSnapshot_RunsForTheLegalEntity(t *testing.T) {
	f := &fakeStore{}
	day := time.Now().UTC().AddDate(0, 0, -1).Format("2006-01-02")
	rec := do(router(f), http.MethodPost, "/v1/receivables/aging-snapshots", `{"legalEntityId":"`+testLE+`","asOf":"`+day+`"}`, "ARAgingRun", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body %s", rec.Code, rec.Body.String())
	}
	if f.statement.legalEntity != testLE || f.statement.asOf != day {
		t.Fatalf("store got %+v", f.statement)
	}
	var res struct {
		AsOf    string `json:"asOf"`
		Rows    int    `json:"rows"`
		Changed int    `json:"changed"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &res); err != nil || res.AsOf != day || res.Rows != 2 || res.Changed != 1 {
		t.Errorf("result = %+v (%v)", res, err)
	}
	// Without a date the job runs for today.
	if rec := do(router(f), http.MethodPost, "/v1/receivables/aging-snapshots", `{"legalEntityId":"`+testLE+`"}`, "ARAgingRun", nil); rec.Code != http.StatusOK ||
		f.statement.asOf != time.Now().UTC().Format("2006-01-02") {
		t.Errorf("default date: status %d asOf %q", rec.Code, f.statement.asOf)
	}
}

func TestAgingSnapshot_ValidatesInputAndNeedsItsDuty(t *testing.T) {
	h := router(&fakeStore{})
	tomorrow := time.Now().UTC().AddDate(0, 0, 1).Format("2006-01-02")
	for body, field := range map[string]string{
		`{"legalEntityId":"nope"}`:                                     "legalEntityId",
		`{"legalEntityId":"` + testLE + `","asOf":"yesterday"}`:        "asOf",
		`{"legalEntityId":"` + testLE + `","asOf":"` + tomorrow + `"}`: "asOf",
	} {
		rec := do(h, http.MethodPost, "/v1/receivables/aging-snapshots", body, "ARAgingRun", nil)
		if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), `"`+field+`"`) {
			t.Errorf("%s: status %d body %s, want 400 naming %s", body, rec.Code, rec.Body.String(), field)
		}
	}
	// Reading balances does not let a clerk write them.
	if rec := do(h, http.MethodPost, "/v1/receivables/aging-snapshots", `{"legalEntityId":"`+testLE+`"}`, "ARView", nil); rec.Code != http.StatusForbidden {
		t.Errorf("with ARView only: status %d, want 403", rec.Code)
	}
}

func TestListAgingSnapshots_FiltersAndValidates(t *testing.T) {
	f := &fakeStore{}
	h := router(f)
	rec := do(h, http.MethodGet, "/v1/receivables/aging-snapshots?legalEntityId="+testLE+"&asOf=2027-03-31&customer="+testItem, "", "ARView", nil)
	if rec.Code != http.StatusOK || f.snapshots.AsOf != "2027-03-31" || f.snapshots.CustomerID != testItem || f.snapshots.LegalEntityID != testLE {
		t.Fatalf("status %d filter %+v", rec.Code, f.snapshots)
	}
	if !strings.Contains(rec.Body.String(), `"totalOpen":"100"`) || !strings.Contains(rec.Body.String(), `"riskScore":45`) {
		t.Errorf("body = %s", rec.Body.String())
	}
	for _, q := range []string{"", "legalEntityId=" + testLE + "&asOf=2027-3-1", "legalEntityId=" + testLE + "&customer=x", "legalEntityId=" + testLE + "&limit=0"} {
		if rec := do(h, http.MethodGet, "/v1/receivables/aging-snapshots?"+q, "", "ARView", nil); rec.Code != http.StatusBadRequest {
			t.Errorf("%q: status %d, want 400", q, rec.Code)
		}
	}
}

func TestCreatePayment_HashesThePayerAccountAndKeepsTheReason(t *testing.T) {
	f := &fakeStore{}
	body := `{"legalEntityId":"` + testLE + `","paymentMode":"ACH","source":"MANUAL","sourceRef":"ACH-7",
		"amount":"950.00","currency":"USD","receivedDate":"2027-03-05","valueDate":"2027-03-05",
		"payerName":"Acme Inc","payerAccount":"DE89 3704 0044 0532 0130 00","shortPayReason":"  promo  "}`
	rec := do(router(f), http.MethodPost, "/v1/receivables/payments", body, "ARPaymentPost", map[string]string{"Idempotency-Key": "k-pay-7"})
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, body %s", rec.Code, rec.Body.String())
	}
	if f.payment.PayerAccountHash == nil || *f.payment.PayerAccountHash != domain.AccountHash("DE89370400440532013000") {
		t.Errorf("payer account hash = %v, want the hash of the normalized IBAN", f.payment.PayerAccountHash)
	}
	if strings.Contains(rec.Body.String(), "3704") {
		t.Errorf("the response echoes the account: %s", rec.Body.String())
	}
	if f.payment.ShortPayReason == nil || *f.payment.ShortPayReason != "promo" {
		t.Errorf("short-pay reason = %v, want trimmed promo", f.payment.ShortPayReason)
	}
}

func TestCreatePayment_RejectsBadPayerIdentity(t *testing.T) {
	h := router(&fakeStore{})
	for field, extra := range map[string]string{
		"payerAccount":   `"payerAccount":"12-34"`,
		"shortPayReason": `"shortPayReason":"   "`,
		"payerName":      `"payerName":"` + strings.Repeat("x", 141) + `"`,
	} {
		body := `{"legalEntityId":"` + testLE + `","paymentMode":"ACH","source":"MANUAL","amount":"950.00","currency":"USD",
			"receivedDate":"2027-03-05","valueDate":"2027-03-05",` + extra + `}`
		rec := do(h, http.MethodPost, "/v1/receivables/payments", body, "ARPaymentPost", map[string]string{"Idempotency-Key": "k-" + field})
		if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), `"`+field+`"`) {
			t.Errorf("%s: status %d body %s, want 400 naming it", field, rec.Code, rec.Body.String())
		}
	}
}

func TestScopeAllows_RefusesACallerWithNoLegalEntityScope(t *testing.T) {
	rec := do(router(&fakeStore{}), http.MethodPost, "/v1/receivables/aging-snapshots", `{"legalEntityId":"`+testLE+`"}`, "ARAgingRun",
		map[string]string{"X-Passage-Legal-Entity": ""})
	if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), "AUTH-0003") {
		t.Fatalf("no scope: %d %s", rec.Code, rec.Body.String())
	}
}
