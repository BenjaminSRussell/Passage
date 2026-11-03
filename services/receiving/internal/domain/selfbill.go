package domain

import (
	"bytes"
	"fmt"
	"strings"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/money"
)

// SelfBill is the evaluated-receipt (self-billing) tax document issued to the vendor.
type SelfBill struct {
	InvoiceID     string
	InvoiceNumber string
	LegalEntityID string
	VendorID      string
	InvoiceDate   string
	Currency      string
	TaxCode       string
	Net           decimal.Decimal
	Tax           decimal.Decimal
	Total         decimal.Decimal
	Lines         []SelfBillLine
}

type SelfBillLine struct {
	LineNo    int
	Qty       decimal.Decimal
	UnitPrice decimal.Decimal
	Amount    decimal.Decimal
}

func (d SelfBill) RenderPDF() []byte {
	var b strings.Builder
	b.WriteString("Self-billing invoice " + d.InvoiceNumber + "\n")
	b.WriteString("Vendor " + d.VendorID + "\n")
	b.WriteString("Legal entity " + d.LegalEntityID + "\n")
	b.WriteString("Date " + d.InvoiceDate + "\n")
	b.WriteString("Currency " + d.Currency + "\n")
	scale := money.MinorUnits(d.Currency)
	for _, l := range d.Lines {
		fmt.Fprintf(&b, "Line %d  qty %s  unit %s  amount %s\n", l.LineNo, l.Qty.String(), l.UnitPrice.String(), l.Amount.StringFixed(scale))
	}
	if d.TaxCode != "" {
		b.WriteString("Tax code " + d.TaxCode + "\n")
	}
	fmt.Fprintf(&b, "Net %s\nTax %s\nTotal %s\n", d.Net.StringFixed(scale), d.Tax.StringFixed(scale), d.Total.StringFixed(scale))
	return pdfFromText("Self-billing invoice "+d.InvoiceNumber, b.String())
}

func pdfFromText(title, body string) []byte {
	escaped := pdfEscape(title + "\n\n" + body)
	var content bytes.Buffer
	content.WriteString("BT /F1 11 Tf 50 780 Td 14 TL (" + escaped + ") Tj ET")
	stream := content.Bytes()
	objects := []string{
		"<< /Type /Catalog /Pages 2 0 R >>",
		"<< /Type /Pages /Kids [3 0 R] /Count 1 >>",
		"<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] /Contents 4 0 R /Resources << /Font << /F1 5 0 R >> >> >>",
		fmt.Sprintf("<< /Length %d >>\nstream\n%s\nendstream", len(stream), stream),
		"<< /Type /Font /Subtype /Type1 /BaseFont /Courier >>",
	}
	var out bytes.Buffer
	out.WriteString("%PDF-1.4\n")
	offsets := make([]int, len(objects))
	for i, obj := range objects {
		offsets[i] = out.Len()
		fmt.Fprintf(&out, "%d 0 obj\n%s\nendobj\n", i+1, obj)
	}
	startxref := out.Len()
	fmt.Fprintf(&out, "xref\n0 %d\n0000000000 65535 f \n", len(objects)+1)
	for _, off := range offsets {
		fmt.Fprintf(&out, "%010d 00000 n \n", off)
	}
	fmt.Fprintf(&out, "trailer << /Size %d /Root 1 0 R >>\nstartxref\n%d\n%%%%EOF\n", len(objects)+1, startxref)
	return out.Bytes()
}

func pdfEscape(s string) string {
	s = strings.ReplaceAll(s, "\\", "\\\\")
	s = strings.ReplaceAll(s, "(", "\\(")
	s = strings.ReplaceAll(s, ")", "\\)")
	s = strings.ReplaceAll(s, "\r", " ")
	s = strings.ReplaceAll(s, "\n", ") Tj T* (")
	return s
}
