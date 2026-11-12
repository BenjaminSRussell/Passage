package api

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
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

func (f *fakeStore) CreateCreditNotes(ctx context.Context, cns []store.Invoice) ([]store.Invoice, error) {
	out := make([]store.Invoice, 0, len(cns))
	for _, cn := range cns {
		made, err := f.CreateCreditNote(ctx, cn)
		if err != nil {
			return nil, err
		}
		out = append(out, made)
	}
	return out, nil
}

func (f *fakeStore) FindByIdempotencyPrefix(_ context.Context, prefix string) ([]store.Invoice, error) {
	var out []store.Invoice
	for _, inv := range f.invoices {
		if strings.HasPrefix(inv.IdempotencyKey, prefix) {
			out = append(out, inv)
		}
	}
	return out, nil
}

// newerSupportInvoice is a later posted invoice of order uuid(70) that billed 200.00 more support (product uuid(81)).
func newerSupportInvoice(f *fakeStore) {
	d := decimal.RequireFromString
	inv := store.Invoice{ID: uuid(61), LegalEntityID: leID, CustomerID: custID, InvoiceAccountID: acctID, Type: domain.TypeShipment, Status: domain.StatusPosted,
		Currency: "EUR", Number: "INV-2027-0000002", InvoiceDate: time.Date(2027, 2, 5, 0, 0, 0, 0, time.UTC), PaymentTermDays: 30, IdempotencyKey: "orig-2",
		Totals: domain.Totals{Net: d("200.00"), Charges: decimal.Zero, Tax: d("38.00"), Gross: d("238.00")}}
	inv.Lines = []store.Line{{Line: domain.Line{No: 1, Description: "Support plan", Qty: d("2"), UnitPrice: d("100"), DiscountPct: decimal.Zero, RevenueAccount: "4000", TaxCode: "S19"},
		Net: d("200.00"), Tax: d("38.00"), Source: domain.Source{OrderID: uuid(70), OrderLineID: uuid(71), ProductID: uuid(81)}}}
	f.invoices[inv.ID] = inv
}

func creditNotesOf(f *fakeStore) []store.Invoice {
	var out []store.Invoice
	for _, d := range f.invoices {
		if d.Type == domain.TypeCreditNote {
			out = append(out, d)
		}
	}
	return out
}

// A retry after a newer invoice of the order posted returns the credit note already made instead of crediting the decrease again on
// the newer invoice.
func TestBillModification_RetryAfterANewerInvoiceDoesNotCreditTwice(t *testing.T) {
	f := newFakeStore()
	postedOrderInvoice(f)
	h := setup(t, f, &fakeTax{perLine: "19.00"})
	body := modificationBody("-100.00", `[{"productId":"`+uuid(81)+`","amount":"-100.00"}]`)
	if rec := doAs(h, http.MethodPost, "/v1/billing/contract-modifications", "", body, "billing.contract-modification.bill", nil); rec.Code != http.StatusCreated {
		t.Fatalf("first: %d %s", rec.Code, rec.Body.String())
	}
	newerSupportInvoice(f)
	if rec := doAs(h, http.MethodPost, "/v1/billing/contract-modifications", "", body, "billing.contract-modification.bill", nil); rec.Code != http.StatusOK {
		t.Fatalf("retry: %d %s", rec.Code, rec.Body.String())
	}
	if cns := creditNotesOf(f); len(cns) != 1 || cns[0].OriginalInvoiceID != uuid(60) {
		t.Fatalf("credit notes %+v", cns)
	}
}

// A decrease above what the newest invoice line has left is spread over the order's lines, newest first: 200.00 on the newer invoice
// and the other 100.00 on the first.
func TestBillModification_SpreadsADecreaseOverTheOrdersLinesNewestFirst(t *testing.T) {
	f := newFakeStore()
	postedOrderInvoice(f)
	newerSupportInvoice(f)
	h := setup(t, f, &fakeTax{perLine: "19.00"})
	body := modificationBody("-300.00", `[{"productId":"`+uuid(81)+`","amount":"-300.00"}]`)
	if rec := doAs(h, http.MethodPost, "/v1/billing/contract-modifications", "", body, "billing.contract-modification.bill", nil); rec.Code != http.StatusCreated {
		t.Fatalf("bill: %d %s", rec.Code, rec.Body.String())
	}
	got := map[string]string{}
	for _, cn := range creditNotesOf(f) {
		got[cn.OriginalInvoiceID] = cn.Totals.Net.StringFixed(2)
	}
	if len(got) != 2 || got[uuid(61)] != "200.00" || got[uuid(60)] != "100.00" {
		t.Fatalf("credits %v", got)
	}
	tooMuch := modificationBody("-1500.00", `[{"productId":"`+uuid(81)+`","amount":"-1500.00"}]`)
	if rec := doAs(h, http.MethodPost, "/v1/billing/contract-modifications", "", strings.Replace(tooMuch, uuid(90), uuid(92), 1), "billing.contract-modification.bill", nil); rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("a decrease above every line: %d", rec.Code)
	}
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
