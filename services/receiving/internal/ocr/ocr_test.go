package ocr

import (
	"bytes"
	"compress/zlib"
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/BenjaminSRussell/passage/services/receiving/internal/domain"
)

// textPDF writes a one-page PDF whose content stream shows each line with Tj.
func textPDF(t *testing.T, compress bool, lines ...string) []byte {
	t.Helper()
	var cs bytes.Buffer
	cs.WriteString("BT /F1 10 Tf 72 760 Td\n")
	for _, l := range lines {
		esc := strings.NewReplacer(`\`, `\\`, "(", `\(`, ")", `\)`).Replace(l)
		fmt.Fprintf(&cs, "(%s) Tj 0 -14 Td\n", esc)
	}
	cs.WriteString("ET\n")
	data, filter := cs.Bytes(), ""
	if compress {
		var z bytes.Buffer
		w := zlib.NewWriter(&z)
		_, _ = w.Write(data)
		_ = w.Close()
		data, filter = z.Bytes(), " /Filter /FlateDecode"
	}
	var b bytes.Buffer
	b.WriteString("%PDF-1.7\n1 0 obj << /Type /Catalog /Pages 2 0 R >> endobj\n")
	b.WriteString("2 0 obj << /Type /Pages /Kids [3 0 R] /Count 1 >> endobj\n")
	b.WriteString("3 0 obj << /Type /Page /Parent 2 0 R /Contents 4 0 R >> endobj\n")
	fmt.Fprintf(&b, "4 0 obj << /Length %d%s >>\nstream\n", len(data), filter)
	b.Write(data)
	b.WriteString("\nendstream\nendobj\ntrailer << /Root 1 0 R >>\n%%EOF\n")
	return b.Bytes()
}

var invoiceText = []string{
	"ACME Services GmbH",
	"Invoice Number: INV-2026-0042",
	"invoice date: 2026-11-02",
	"Currency: eur",
	"Vendor Tax ID: DE123456789",
	"Line: Consulting (November) | 10 | HUR | 120.00 | 1,200.00",
	"Line: Travel | 1 | EA | 85.50 | 85.50",
	"Total: 1,285.50",
}

func TestLocalReadsTheTextLayerDeterministically(t *testing.T) {
	for _, compress := range []bool{false, true} {
		pdf := textPDF(t, compress, invoiceText...)
		got, err := Local{}.Extract(context.Background(), pdf, "application/pdf")
		if err != nil {
			t.Fatalf("compress=%v: %v", compress, err)
		}
		if got.InvoiceNumber != "INV-2026-0042" || got.InvoiceDate.Format("2006-01-02") != "2026-11-02" || got.Currency != "EUR" ||
			got.VendorTaxID != "DE123456789" || got.Total.String() != "1285.5" || !got.Confidence.Equal(LocalConfidence) || len(got.Lines) != 2 {
			t.Fatalf("compress=%v: %+v", compress, got)
		}
		l := got.Lines[0]
		if l.Description != "Consulting (November)" || l.Qty.String() != "10" || l.Unit != "HUR" || l.UnitPrice.String() != "120" || l.Amount.String() != "1200" {
			t.Fatalf("line %+v", l)
		}
		if err := got.Validate(); err != nil || len(got.ReviewReasons()) != 0 {
			t.Fatalf("validate %v reasons %v", err, got.ReviewReasons())
		}
		again, _ := Local{}.Extract(context.Background(), pdf, "application/pdf")
		if fmt.Sprint(again) != fmt.Sprint(got) {
			t.Fatal("the same document gave a different result")
		}
	}
}

func TestLocalJoinsTJArrays(t *testing.T) {
	pdf := []byte("%PDF-1.4\n1 0 obj << /Length 0 >>\nstream\nBT [(Invoice ) -30 (Number: A-1)] TJ (Invoice Date: 2026-01-31) ' [(Line: x | 1 | EA | 2 | 2)] TJ (Total: 2) Tj ET\nendstream\nendobj\n")
	got, err := Local{}.Extract(context.Background(), pdf, "application/pdf")
	if err != nil || got.InvoiceNumber != "A-1" || len(got.Lines) != 1 || got.Total.String() != "2" {
		t.Fatalf("%+v %v", got, err)
	}
	if reasons := got.ReviewReasons(); len(reasons) != 1 || reasons[0] != domain.ReviewNoVendorTaxID {
		t.Fatalf("reasons %v", reasons)
	}
}

func TestLocalFailsClosed(t *testing.T) {
	without := func(drop string) []string {
		var out []string
		for _, l := range invoiceText {
			if !strings.HasPrefix(strings.ToLower(l), drop) {
				out = append(out, l)
			}
		}
		return out
	}
	cases := map[string]struct {
		content   []byte
		mediaType string
	}{
		"image":           {[]byte("\x89PNG\r\n"), "image/png"},
		"not a pdf":       {[]byte("hello"), "application/pdf"},
		"no text layer":   {[]byte("%PDF-1.7 scan"), "application/pdf"},
		"no number":       {textPDF(t, false, without("invoice number")...), "application/pdf"},
		"no date":         {textPDF(t, false, without("invoice date")...), "application/pdf"},
		"no total":        {textPDF(t, false, without("total")...), "application/pdf"},
		"no lines":        {textPDF(t, false, without("line")...), "application/pdf"},
		"bad date":        {textPDF(t, false, append(without("invoice date"), "Invoice Date: 02.11.2026")...), "application/pdf"},
		"bad line":        {textPDF(t, false, append(invoiceText, "Line: a | b")...), "application/pdf"},
		"bad amount":      {textPDF(t, false, append(without("total"), "Total: about 5")...), "application/pdf"},
		"duplicate label": {textPDF(t, false, append(invoiceText, "Total: 9")...), "application/pdf"},
	}
	for name, tc := range cases {
		if _, err := (Local{}).Extract(context.Background(), tc.content, tc.mediaType); !errors.Is(err, domain.ErrExtraction) {
			t.Errorf("%s: got %v, want ErrExtraction", name, err)
		}
	}
}

func TestNew(t *testing.T) {
	if ex, err := New(""); ex != nil || err != nil {
		t.Fatalf("empty: %v %v", ex, err)
	}
	if ex, err := New(" Local "); err != nil {
		t.Fatal(err)
	} else if _, ok := ex.(Local); !ok {
		t.Fatalf("got %T", ex)
	}
	if _, err := New("textract"); err == nil || !strings.Contains(err.Error(), "OCR_PROVIDER") {
		t.Fatalf("unknown provider: %v", err)
	}
}
