package domain_test

import (
	"bytes"
	"testing"

	"github.com/BenjaminSRussell/passage/services/receiving/internal/domain"
)

func TestSelfBill_RenderPDF(t *testing.T) {
	pdf := domain.SelfBill{
		InvoiceID: "inv-1", InvoiceNumber: "ERS-RCV-00000001", LegalEntityID: "le-1", VendorID: "v-1",
		InvoiceDate: "2026-11-03", Currency: "EUR", TaxCode: "VAT19",
		Net: dec("500"), Tax: dec("95"), Total: dec("595"),
		Lines: []domain.SelfBillLine{{LineNo: 1, Qty: dec("50"), UnitPrice: dec("10"), Amount: dec("500")}},
	}.RenderPDF()
	if !bytes.HasPrefix(pdf, []byte("%PDF-1.4")) || !bytes.Contains(pdf, []byte("%%EOF")) {
		t.Fatalf("not a PDF: %s", pdf[:min(80, len(pdf))])
	}
	if !bytes.Contains(pdf, []byte("ERS-RCV-00000001")) || !bytes.Contains(pdf, []byte("VAT19")) || !bytes.Contains(pdf, []byte("595.00")) {
		t.Fatalf("missing invoice fields: %s", pdf)
	}
}
