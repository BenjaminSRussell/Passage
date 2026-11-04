package domain_test

import (
	"bytes"
	"testing"

	"github.com/BenjaminSRussell/passage/services/receiving/internal/domain"
)

func TestSelfBill_RenderPDF(t *testing.T) {
	pdf := domain.SelfBill{
		Number: "SB-00000007", InvoiceID: "inv-1", InvoiceNumber: "ERS-RCV-00000001", LegalEntityID: "le-1", VendorID: "v-1",
		InvoiceDate: "2026-11-03", SupplyDate: "2026-10-30", Currency: "EUR", TaxCode: "VAT19", TaxRatePct: dec("19"),
		Parties: domain.SelfBillParties{
			Supplier: domain.SelfBillParty{Name: "Acme Supplies GmbH", Address: "Hauptstr 1, 10115 Berlin", RegistrationNumber: "HRB 123"},
			Customer: domain.SelfBillParty{Name: "Passage Werke AG", Address: "Ring 5, 80331 Muenchen", VatID: "DE123456789"},
		},
		Net: dec("500"), Tax: dec("95"), Total: dec("595"),
		Lines: []domain.SelfBillLine{{LineNo: 1, Qty: dec("50"), UnitPrice: dec("10"), Amount: dec("500")}},
	}.RenderPDF()
	if !bytes.HasPrefix(pdf, []byte("%PDF-1.4")) || !bytes.Contains(pdf, []byte("%%EOF")) {
		t.Fatalf("not a PDF: %s", pdf[:min(80, len(pdf))])
	}
	for _, want := range []string{"SB-00000007", "ERS-RCV-00000001", "VAT19", "rate 19%", "595.00", "Acme Supplies GmbH", "Hauptstr 1", "Passage Werke AG", "DE123456789", "Date of supply 2026-10-30"} {
		if !bytes.Contains(pdf, []byte(want)) {
			t.Fatalf("missing %q: %s", want, pdf)
		}
	}
}
