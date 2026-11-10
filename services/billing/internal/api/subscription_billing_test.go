package api

import (
	"fmt"
	"net/http"
	"testing"
)

func usageBody(externalID, meter, date, qty, price string) string {
	return fmt.Sprintf(`{"externalId":%q,"meterCode":%q,"usageDate":%q,"quantity":%q,"unitPrice":%q}`, externalID, meter, date, qty, price)
}

func recordUsage(t *testing.T, h http.Handler, subID, body string, want int) {
	t.Helper()
	if rec := do(h, http.MethodPost, "/v1/billing/subscriptions/"+subID+"/usage", "", body); rec.Code != want {
		t.Fatalf("usage %s: %d %s, want %d", body, rec.Code, rec.Body, want)
	}
}

type dueOut struct {
	Considered int      `json:"considered"`
	Billed     int      `json:"billed"`
	Failed     int      `json:"failed"`
	InvoiceIDs []string `json:"invoiceIds"`
}

func billDue(t *testing.T, h http.Handler, asOf string) dueOut {
	t.Helper()
	rec := do(h, http.MethodPost, "/v1/billing/subscriptions:bill-due", "", fmt.Sprintf(`{"legalEntityId":%q,"asOf":%q}`, leID, asOf))
	if rec.Code != http.StatusOK {
		t.Fatalf("bill-due %s: %d %s", asOf, rec.Code, rec.Body)
	}
	var out dueOut
	decode(t, rec, &out)
	return out
}

func invoiceOf(t *testing.T, h http.Handler, id string) invoiceDTO {
	t.Helper()
	var inv invoiceDTO
	decode(t, do(h, http.MethodGet, "/v1/billing/invoices/"+id, "", ""), &inv)
	return inv
}

func TestSubscriptionUsage_IsInvoicedOnceWithThePeriodItFallsIn(t *testing.T) {
	h := subscriptionHandler(t, exemptTax(t))
	sub := newSubscription(t, h, subscriptionBody("USD", "100.00", 1, "2027-01-01"))
	recordUsage(t, h, sub.ID, usageBody("r-1", "api-calls", "2027-01-10", "400", "0.002"), http.StatusCreated)
	recordUsage(t, h, sub.ID, usageBody("r-2", "api-calls", "2027-01-20", "600", "0.002"), http.StatusCreated)
	recordUsage(t, h, sub.ID, usageBody("r-3", "api-calls", "2027-02-05", "50", "0.002"), http.StatusCreated)
	// A resent reading is a replay; other values under its id are refused; a date before the start is refused.
	recordUsage(t, h, sub.ID, usageBody("r-1", "api-calls", "2027-01-10", "400", "0.002"), http.StatusOK)
	recordUsage(t, h, sub.ID, usageBody("r-1", "api-calls", "2027-01-10", "999", "0.002"), http.StatusUnprocessableEntity)
	recordUsage(t, h, sub.ID, usageBody("r-0", "api-calls", "2026-12-31", "1", "0.002"), http.StatusUnprocessableEntity)
	recordUsage(t, h, sub.ID, usageBody("r-9", "api-calls", "2027-01-10", "0", "0.002"), http.StatusBadRequest)

	jan := billPeriod(h, sub.ID, "2027-01-01")
	if jan.Code != http.StatusCreated {
		t.Fatalf("bill january: %d %s", jan.Code, jan.Body)
	}
	var inv invoiceDTO
	decode(t, jan, &inv)
	if len(inv.Lines) != 2 || inv.Lines[1].Qty != "1000" || inv.Lines[1].Net != "2.00" || inv.Totals.Net != "102.00" {
		t.Fatalf("january invoice %+v", inv)
	}
	// The usage of 2027-02-05 waits for the February invoice, and january's is not billed twice.
	feb := billPeriod(h, sub.ID, "2027-02-01")
	var febInv invoiceDTO
	decode(t, feb, &febInv)
	if feb.Code != http.StatusCreated || len(febInv.Lines) != 2 || febInv.Lines[1].Qty != "50" || febInv.Lines[1].Net != "0.10" {
		t.Fatalf("february invoice %d %+v", feb.Code, febInv)
	}
	// Usage in an invoiced period is refused.
	recordUsage(t, h, sub.ID, usageBody("late", "api-calls", "2027-01-30", "1", "0.002"), http.StatusUnprocessableEntity)
}

func TestSubscriptionBillDue_BillsEveryDuePeriodOnceAndSkipsTheFuture(t *testing.T) {
	h := subscriptionHandler(t, exemptTax(t))
	monthly := newSubscription(t, h, subscriptionBody("USD", "100.00", 1, "2027-01-15"))
	quarterly := newSubscription(t, h, subscriptionBody("USD", "300.00", 3, "2027-02-01"))
	recordUsage(t, h, monthly.ID, usageBody("m-1", "seats", "2027-02-03", "5", "1.00"), http.StatusCreated)

	// On 2027-01-31 only the monthly subscription has a period open (January, prorated from the 15th).
	first := billDue(t, h, "2027-01-31")
	if first.Considered != 1 || first.Billed != 1 || first.Failed != 0 || len(first.InvoiceIDs) != 1 {
		t.Fatalf("first run %+v", first)
	}
	if inv := invoiceOf(t, h, first.InvoiceIDs[0]); inv.Type != "SUBSCRIPTION" || inv.InvoiceDate != "2027-01-01" || inv.Totals.Net != "54.84" {
		t.Fatalf("january invoice %+v", inv)
	}
	if again := billDue(t, h, "2027-01-31"); again.Billed != 0 {
		t.Fatalf("a repeat of the run billed %+v", again)
	}
	// By 2027-03-31 the monthly one owes February and March and the quarterly one its first quarter.
	later := billDue(t, h, "2027-03-31")
	if later.Billed != 3 || later.Failed != 0 {
		t.Fatalf("later run %+v", later)
	}
	var withUsage int
	for _, id := range later.InvoiceIDs {
		if inv := invoiceOf(t, h, id); len(inv.Lines) == 2 && inv.Lines[1].Description == "seats usage 2027-02-03 to 2027-02-03" {
			withUsage++
		}
	}
	if withUsage != 1 {
		t.Fatalf("the seats usage was invoiced %d times", withUsage)
	}
	if done := billDue(t, h, "2027-03-31"); done.Billed != 0 {
		t.Fatalf("nothing is left to bill, got %+v", done)
	}
	_ = quarterly
}

func TestSubscriptionBillDue_ValidatesAndStaysInScope(t *testing.T) {
	h := subscriptionHandler(t, exemptTax(t))
	for _, body := range []string{`{}`, `{"legalEntityId":"nope"}`, fmt.Sprintf(`{"legalEntityId":%q,"asOf":"yesterday"}`, leID)} {
		if rec := do(h, http.MethodPost, "/v1/billing/subscriptions:bill-due", "", body); rec.Code != http.StatusBadRequest {
			t.Errorf("%s: %d, want 400", body, rec.Code)
		}
	}
	other := `{"legalEntityId":"0190f0a0-0000-7000-8000-0000000000ff"}`
	if rec := do(h, http.MethodPost, "/v1/billing/subscriptions:bill-due", "", other); rec.Code != http.StatusForbidden {
		t.Errorf("another legal entity: %d, want 403", rec.Code)
	}
}
