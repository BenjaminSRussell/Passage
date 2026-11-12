package api

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/services/billing/internal/domain"
	"github.com/BenjaminSRussell/passage/services/billing/internal/store"
)

// postedOrderInvoice is a posted shipment invoice of order uuid(70): support (product uuid(81), 1,200.00, tax 228.00) and a
// licence (product uuid(82), 800.00, tax 152.00).
func postedOrderInvoice(f *fakeStore) store.Invoice {
	d := decimal.RequireFromString
	inv := store.Invoice{ID: uuid(60), LegalEntityID: leID, CustomerID: custID, InvoiceAccountID: acctID, Type: domain.TypeShipment, Status: domain.StatusPosted,
		Currency: "EUR", Number: "INV-2027-0000001", InvoiceDate: time.Date(2027, 1, 5, 0, 0, 0, 0, time.UTC), PaymentTermDays: 30, IdempotencyKey: "orig",
		Totals: domain.Totals{Net: d("2000.00"), Charges: decimal.Zero, Tax: d("380.00"), Gross: d("2380.00")}}
	inv.Lines = []store.Line{
		{Line: domain.Line{No: 1, Description: "Support plan", Qty: d("12"), UnitPrice: d("100"), DiscountPct: decimal.Zero, RevenueAccount: "4000", TaxCode: "S19"},
			Net: d("1200.00"), Tax: d("228.00"), Source: domain.Source{OrderID: uuid(70), OrderLineID: uuid(71), ProductID: uuid(81)}},
		{Line: domain.Line{No: 2, Description: "Licence", Qty: d("1"), UnitPrice: d("800"), DiscountPct: decimal.Zero, RevenueAccount: "4000", TaxCode: "S19"},
			Net: d("800.00"), Tax: d("152.00"), Source: domain.Source{OrderID: uuid(70), OrderLineID: uuid(72), ProductID: uuid(82)}},
	}
	f.invoices[inv.ID] = inv
	return inv
}

func modificationBody(change string, lines string) string {
	return `{"modificationId":"` + uuid(90) + `","contractId":"` + uuid(91) + `","legalEntityId":"` + leID + `","orderId":"` + uuid(70) +
		`","customerId":"` + custID + `","currency":"EUR","priceChange":"` + change + `","effectiveDate":"2027-04-01","reason":"scope change","lines":` + lines + `}`
}

func TestBillModification_InvoicesIncreasesAndCreditsDecreases(t *testing.T) {
	f := newFakeStore()
	postedOrderInvoice(f)
	h := setup(t, f, &fakeTax{perLine: "19.00"})
	const duty = "billing.contract-modification.bill"
	body := modificationBody("-200.00", `[{"productId":"`+uuid(81)+`","amount":"-300.00"},{"productId":"`+uuid(82)+`","amount":"100.00"}]`)
	if rec := doAs(h, http.MethodPost, "/v1/billing/contract-modifications", "", body, "billing.invoice.maintain", nil); rec.Code != http.StatusForbidden {
		t.Fatalf("a person's duty: %d", rec.Code)
	}
	rec := doAs(h, http.MethodPost, "/v1/billing/contract-modifications", "", body, duty, nil)
	if rec.Code != http.StatusCreated {
		t.Fatalf("bill: %d %s", rec.Code, rec.Body.String())
	}
	var out modificationOut
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil || len(out.Documents) != 2 {
		t.Fatalf("documents %s (%v)", rec.Body.String(), err)
	}
	var inv, cn store.Invoice
	for _, d := range f.invoices {
		switch store.ModificationOfKey(d.IdempotencyKey) {
		case uuid(90):
			if d.Type == domain.TypeCreditNote {
				cn = d
			} else {
				inv = d
			}
		}
	}
	if inv.Status != domain.StatusProposal || len(inv.Lines) != 1 || inv.Lines[0].Source.ProductID != uuid(82) || inv.Lines[0].Source.OrderID != uuid(70) ||
		inv.Lines[0].Net.String() != "100" || inv.Lines[0].TaxCode != "S19" || inv.CustomerID != custID {
		t.Fatalf("invoice %+v", inv)
	}
	if cn.OriginalInvoiceID != uuid(60) || len(cn.Lines) != 1 || cn.Lines[0].OriginalLineNo != 1 || cn.Lines[0].Net.String() != "300" ||
		cn.Lines[0].Tax.String() != "57" || cn.Lines[0].Qty.String() != "3" {
		t.Fatalf("credit note %+v", cn)
	}
	// A retry returns both documents and creates nothing.
	before := len(f.invoices)
	if rec := doAs(h, http.MethodPost, "/v1/billing/contract-modifications", "", body, duty, nil); rec.Code != http.StatusOK || len(f.invoices) != before {
		t.Fatalf("retry: %d, %d documents", rec.Code, len(f.invoices))
	}
	if rec := doAs(h, http.MethodPost, "/v1/billing/contract-modifications", "", modificationBody("-100.00", `[{"productId":"`+uuid(81)+`","amount":"-300.00"},{"productId":"`+uuid(82)+`","amount":"100.00"}]`), duty, nil); rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("lines not adding up: %d", rec.Code)
	}
	if rec := doAs(h, http.MethodPost, "/v1/billing/contract-modifications", "", modificationBody("50.00", `[{"productId":"`+uuid(83)+`","amount":"50.00"}]`), duty, nil); rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("a product never invoiced: %d", rec.Code)
	}
	if rec := doAs(h, http.MethodPost, "/v1/billing/free-text-invoices", store.ModificationKeyPrefix+"x", `{}`, allDuties, nil); rec.Code != http.StatusBadRequest {
		t.Fatalf("a person's key with the modification prefix: %d", rec.Code)
	}
}
