package domain

import (
	"bytes"
	"fmt"
	"os"
	"strings"
	"testing"
)

func sampleDocument(docType string) DocumentRequest {
	return DocumentRequest{
		DocumentType: docType,
		Number:       "PO-000123",
		LegalEntity:  DocParty{Name: "Cedar Ridge Pumps, Inc.", Address: []string{"400 Industrial Way", "Cedar Rapids, IA 52404"}},
		Counterparty: &DocParty{Label: "Vendor", Name: "Ironbridge Foundry", Address: []string{"88 Foundry Rd", "Birmingham, AL"}},
		Fields:       []DocField{{Label: "Order date", Value: "2026-11-10"}, {Label: "Currency", Value: "USD"}},
		Columns:      []DocColumn{{Header: "Item"}, {Header: "Qty", Align: "R"}, {Header: "Unit price", Align: "R"}, {Header: "Amount", Align: "R"}},
		Lines: [][]string{
			{"Impeller casting (CST-100) (rev B)", "40", "112.50", "4,500.00"},
			{"Volute casting", "12", "98.00", "1,176.00"},
		},
		Totals: []DocField{{Label: "Total", Value: "5,676.00 USD"}},
		Notes:  "Deliver to dock 2.",
	}
}

func TestRenderDocument_MatchesGoldenAndIsDeterministic(t *testing.T) {
	first, err := RenderDocument(sampleDocument(DocPurchaseOrder))
	if err != nil {
		t.Fatal(err)
	}
	second, _ := RenderDocument(sampleDocument(DocPurchaseOrder))
	if !bytes.Equal(first, second) {
		t.Fatal("two renders of the same document differ")
	}
	if !bytes.HasPrefix(first, []byte("%PDF-")) || !bytes.HasSuffix(first, []byte("%%EOF\n")) {
		t.Fatalf("not a PDF: %q", first[:20])
	}
	const golden = "../../testdata/golden/purchase-order.pdf"
	if os.Getenv("UPDATE_GOLDEN") != "" {
		if err := os.WriteFile(golden, first, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(golden)
	if err != nil {
		t.Fatalf("read golden: %v (run with UPDATE_GOLDEN=1 to create it)", err)
	}
	if !bytes.Equal(first, want) {
		t.Fatal("purchase order differs from testdata/golden/purchase-order.pdf; if the layout change is intended, run with UPDATE_GOLDEN=1")
	}
}

func TestRenderDocument_ShowsEntityNumberAndTitle(t *testing.T) {
	for docType, title := range docTitles {
		pdf, err := RenderDocument(sampleDocument(docType))
		if err != nil {
			t.Fatal(err)
		}
		for _, want := range []string{"Cedar Ridge Pumps, Inc.", "400 Industrial Way", "PO-000123", title} {
			if !strings.Contains(string(pdf), "("+want+")") {
				t.Errorf("%s: PDF does not show %q", docType, want)
			}
		}
	}
}

func TestRenderDocument_EscapesAndPaginates(t *testing.T) {
	d := sampleDocument(DocCustomerInvoice)
	d.LegalEntity.Name = `Acme (East) \ Co`
	d.Lines = nil
	for i := 0; i < 90; i++ {
		d.Lines = append(d.Lines, []string{fmt.Sprintf("Line %d", i), "1", "1.00", "1.00"})
	}
	pdf, err := RenderDocument(d)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(pdf), `(Acme \(East\) \\ Co)`) {
		t.Error("parentheses and backslash are not escaped")
	}
	if got := strings.Count(string(pdf), "/Type /Page "); got != 3 {
		t.Errorf("pages = %d, want 3", got)
	}
}

func TestValidateDocument_NamesTheField(t *testing.T) {
	cases := map[string]func(d *DocumentRequest){
		"documentType":     func(d *DocumentRequest) { d.DocumentType = "RECEIPT" },
		"number":           func(d *DocumentRequest) { d.Number = " " },
		"legalEntity.name": func(d *DocumentRequest) { d.LegalEntity.Name = "" },
		"lines[0]":         func(d *DocumentRequest) { d.Lines[0] = []string{"only one"} },
		"lines[1][0]":      func(d *DocumentRequest) { d.Lines[1][0] = "Volute ✓" },
		"columns":          func(d *DocumentRequest) { d.Columns = nil },
		"notes":            func(d *DocumentRequest) { d.Notes = strings.Repeat("x", 201) },
	}
	for field, mutate := range cases {
		d := sampleDocument(DocPurchaseOrder)
		mutate(&d)
		_, err := RenderDocument(d)
		de, ok := err.(*Error)
		if !ok || de.Code != CodeDocument || !strings.Contains(de.Detail, field) {
			t.Errorf("%s: err = %v, want %s naming the field", field, err, CodeDocument)
		}
	}
}
