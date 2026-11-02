package domain_test

import (
	"slices"
	"testing"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/services/receiving/internal/domain"
)

const ublInvoice = `<?xml version="1.0" encoding="UTF-8"?>
<Invoice xmlns="urn:oasis:names:specification:ubl:schema:xsd:Invoice-2" xmlns:cac="urn:oasis:names:specification:ubl:schema:xsd:CommonAggregateComponents-2" xmlns:cbc="urn:oasis:names:specification:ubl:schema:xsd:CommonBasicComponents-2">
  <cbc:ID>INV-2026-17</cbc:ID>
  <cbc:IssueDate>2026-10-20</cbc:IssueDate>
  <cbc:DocumentCurrencyCode>EUR</cbc:DocumentCurrencyCode>
  <cac:AccountingSupplierParty><cac:Party><cac:PartyTaxScheme><cbc:CompanyID>DE123456789</cbc:CompanyID></cac:PartyTaxScheme></cac:Party></cac:AccountingSupplierParty>
  <cac:LegalMonetaryTotal>
    <cbc:LineExtensionAmount currencyID="EUR">125.00</cbc:LineExtensionAmount>
    <cbc:TaxExclusiveAmount currencyID="EUR">125.00</cbc:TaxExclusiveAmount>
    <cbc:PayableAmount currencyID="EUR">148.75</cbc:PayableAmount>
  </cac:LegalMonetaryTotal>
  <cac:InvoiceLine><cbc:ID>1</cbc:ID><cbc:InvoicedQuantity unitCode="EA">10</cbc:InvoicedQuantity><cbc:LineExtensionAmount currencyID="EUR">25.00</cbc:LineExtensionAmount><cac:Item><cbc:Name>Bolt M8</cbc:Name></cac:Item><cac:Price><cbc:PriceAmount currencyID="EUR">2.50</cbc:PriceAmount></cac:Price></cac:InvoiceLine>
  <cac:InvoiceLine><cbc:ID>2</cbc:ID><cbc:InvoicedQuantity unitCode="KG">4</cbc:InvoicedQuantity><cbc:LineExtensionAmount currencyID="EUR">100.00</cbc:LineExtensionAmount><cac:Item><cbc:Name>Steel plate</cbc:Name></cac:Item><cac:Price><cbc:PriceAmount currencyID="EUR">25.00</cbc:PriceAmount></cac:Price></cac:InvoiceLine>
</Invoice>`

func TestParseUBL_ReadsHeaderLinesAndTaxExclusiveTotal(t *testing.T) {
	got, err := domain.ParseUBL([]byte(ublInvoice))
	if err != nil {
		t.Fatal(err)
	}
	if got.InvoiceNumber != "INV-2026-17" || got.Currency != "EUR" || got.VendorTaxID != "DE123456789" || got.InvoiceDate.Format("2006-01-02") != "2026-10-20" {
		t.Fatalf("header %+v", got)
	}
	if !got.Total.Equal(decimal.NewFromInt(125)) || !got.Confidence.Equal(decimal.NewFromInt(1)) || len(got.Lines) != 2 {
		t.Fatalf("totals %+v", got)
	}
	if l := got.Lines[1]; l.Description != "Steel plate" || l.Unit != "KG" || !l.Qty.Equal(decimal.NewFromInt(4)) || !l.UnitPrice.Equal(decimal.NewFromInt(25)) || !l.Amount.Equal(decimal.NewFromInt(100)) {
		t.Fatalf("line %+v", l)
	}
	if err := got.Validate(); err != nil {
		t.Fatal(err)
	}
	if reasons := got.ReviewReasons(); len(reasons) != 0 {
		t.Fatalf("clean document needs review: %v", reasons)
	}
}

func TestParseUBL_RefusesBadDocuments(t *testing.T) {
	for name, doc := range map[string]string{
		"doctype":  `<!DOCTYPE Invoice [<!ENTITY x "y">]><Invoice><ID>1</ID></Invoice>`,
		"not xml":  `not xml`,
		"wrong":    `<Order><ID>1</ID></Order>`,
		"bad date": `<Invoice><ID>1</ID><IssueDate>20.10.2026</IssueDate></Invoice>`,
		"no total": `<Invoice><ID>1</ID><IssueDate>2026-10-20</IssueDate></Invoice>`,
	} {
		_, err := domain.ParseUBL([]byte(doc))
		var de *domain.Error
		if err == nil || !asDomain(err, &de) || de.Code != domain.CodeInvalidCapture {
			t.Errorf("%s: err %v", name, err)
		}
	}
}

func asDomain(err error, target **domain.Error) bool {
	de, ok := err.(*domain.Error)
	*target = de
	return ok
}

func TestExtractedInvoice_ReviewReasons(t *testing.T) {
	base, err := domain.ParseUBL([]byte(ublInvoice))
	if err != nil {
		t.Fatal(err)
	}
	low := base
	low.Confidence = decimal.RequireFromString("0.79")
	mismatch := base
	mismatch.Total = decimal.RequireFromString("125.02")
	withinTolerance := base
	withinTolerance.Total = decimal.RequireFromString("125.01")
	noTax := base
	noTax.VendorTaxID = ""
	for name, tc := range map[string]struct {
		in   domain.ExtractedInvoice
		want []string
	}{
		"low confidence":    {low, []string{domain.ReviewLowConfidence}},
		"mismatch":          {mismatch, []string{domain.ReviewTotalMismatch}},
		"within tolerance":  {withinTolerance, []string{}},
		"no vendor tax id":  {noTax, []string{domain.ReviewNoVendorTaxID}},
		"exactly threshold": {func() domain.ExtractedInvoice { b := base; b.Confidence = domain.MinCaptureConfidence; return b }(), []string{}},
	} {
		if got := tc.in.ReviewReasons(); !slices.Equal(got, tc.want) {
			t.Errorf("%s: got %v want %v", name, got, tc.want)
		}
	}
}

func TestExtractedInvoice_ValidateRefusesUnusableDocuments(t *testing.T) {
	good, _ := domain.ParseUBL([]byte(ublInvoice))
	for name, mutate := range map[string]func(*domain.ExtractedInvoice){
		"no number":        func(e *domain.ExtractedInvoice) { e.InvoiceNumber = "" },
		"lower case ccy":   func(e *domain.ExtractedInvoice) { e.Currency = "eur" },
		"negative total":   func(e *domain.ExtractedInvoice) { e.Total = decimal.NewFromInt(-1) },
		"no lines":         func(e *domain.ExtractedInvoice) { e.Lines = nil },
		"zero qty":         func(e *domain.ExtractedInvoice) { e.Lines = []domain.CaptureLine{{Qty: decimal.Zero}} },
		"confidence above": func(e *domain.ExtractedInvoice) { e.Confidence = decimal.NewFromInt(2) },
	} {
		bad := good
		mutate(&bad)
		if err := bad.Validate(); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
}
