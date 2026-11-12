package domain

import (
	"bytes"
	"fmt"
	"strings"
	"testing"
	"time"
)

func facturxInvoice() UBLInvoice {
	seller := UBLParty{Name: "Demo Manufacturer GmbH", EndpointScheme: "0088", EndpointID: "4006403333338", CountryCode: "DE", VATID: "DE123456789", LegalID: "HRB 12345",
		StreetName: "Industriestrasse 7", CityName: "Koeln", PostalZone: "50667", ContactName: "AR", ContactPhone: "+49 221 5550100", ContactEmail: "ar@demo.example"}
	buyer := UBLParty{Name: "Mueller & Soehne KG", EndpointScheme: "0088", EndpointID: "0614141000029", CountryCode: "DE", VATID: "DE811907980", CityName: "Duesseldorf", PostalZone: "40213"}
	return UBLInvoice{ID: "INV-77", IssueDate: "2026-11-02", DueDate: "2026-12-02", Currency: "EUR", BuyerReference: "BR-1", OrderReference: "PO-9", IBAN: "DE89370400440532013000",
		PaymentMeansCode: "58", Supplier: seller, Customer: buyer,
		Lines: []UBLLine{
			{ID: "1", Name: "Polymer granulate", UnitCode: "KGM", Quantity: dec("10"), UnitPrice: dec("2.5"), Category: "S", Percent: dec("19")},
			{ID: "2", Name: "Sealing ring set", UnitCode: "C62", Quantity: dec("2"), UnitPrice: dec("10"), Category: "S", Percent: dec("7")}},
		AllowanceCharges: []UBLAllowanceCharge{{Reason: "Discount", Amount: dec("5"), Category: "S", Percent: dec("19")}}}
}

func TestBuildCII_CarriesTheSameFiguresAsTheUBL(t *testing.T) {
	inv := facturxInvoice()
	doc, issues := BuildCII(inv)
	if len(issues) != 0 {
		t.Fatalf("issues: %+v", issues)
	}
	root, err := parseXML(doc)
	if err != nil {
		t.Fatalf("not well-formed: %v", err)
	}
	totals := ComputeUBLTotals(inv)
	sum := root.first("SupplyChainTradeTransaction", "ApplicableHeaderTradeSettlement", "SpecifiedTradeSettlementHeaderMonetarySummation")
	if got := sum.val("DuePayableAmount"); got != totals.Payable.StringFixed(2) {
		t.Fatalf("payable %s, want %s", got, totals.Payable.StringFixed(2))
	}
	if got := sum.val("TaxTotalAmount"); got != totals.TaxAmount.StringFixed(2) || len(totals.TaxSubtotals) != 2 {
		t.Fatalf("tax %s %+v", got, totals.TaxSubtotals)
	}
	for _, want := range []string{facturxGuideline, "<ram:TypeCode>380</ram:TypeCode>", `<udt:DateTimeString format="102">20261102</udt:DateTimeString>`,
		"Mueller &amp; Soehne KG", `schemeID="VA">DE123456789`, "<ram:IBANID>DE89370400440532013000</ram:IBANID>", "<ram:ChargeIndicator><udt:Indicator>false</udt:Indicator></ram:ChargeIndicator>"} {
		if !strings.Contains(doc, want) {
			t.Errorf("CII lacks %q", want)
		}
	}
	if n := strings.Count(doc, "<ram:IncludedSupplyChainTradeLineItem>"); n != 2 {
		t.Errorf("%d line items", n)
	}
	// A credit note is type 381; an invalid invoice yields the blocking issues, no document.
	inv.CreditNote = true
	if d, _ := BuildCII(inv); !strings.Contains(d, "<ram:TypeCode>381</ram:TypeCode>") {
		t.Error("credit note type code")
	}
	inv.IBAN = ""
	if d, is := BuildCII(inv); d != "" || len(is) == 0 {
		t.Errorf("invalid: %q %+v", d, is)
	}
	if DefaultMandates()[MandateFacturX].Format() != "FACTURX_EN16931" {
		t.Error("the Factur-X mandate is registered")
	}
}

func TestFacturXPDF_EmbedsTheXMLAndNamesTheProfile(t *testing.T) {
	doc, _ := BuildCII(facturxInvoice())
	pdf, err := FacturXPDF(doc, time.Date(2026, 11, 2, 10, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"%PDF-1.7", "/AFRelationship /Data", "/Type /EmbeddedFile", "<pdfaid:part>3</pdfaid:part>", "<fx:ConformanceLevel>EN 16931</fx:ConformanceLevel>",
		"Invoice INV-77", "%%EOF"} {
		if !bytes.Contains(pdf, []byte(want)) {
			t.Errorf("the PDF lacks %q", want)
		}
	}
	// The embedded file is the XML byte for byte.
	if !bytes.Contains(pdf, []byte("stream\n"+doc+"\nendstream")) {
		t.Error("the embedded XML differs from the CII document")
	}
	// The cross-reference table points at the objects.
	idx := bytes.LastIndex(pdf, []byte("startxref\n"))
	var xref int
	if _, err := fmtSscan(string(pdf[idx+len("startxref\n"):]), &xref); err != nil || !bytes.HasPrefix(pdf[xref:], []byte("xref\n")) {
		t.Errorf("startxref %d does not point at the table (%v)", xref, err)
	}
	if _, err := FacturXPDF("<nope/>", time.Now()); err == nil {
		t.Error("a non-CII document is refused")
	}
}

func fmtSscan(s string, v *int) (int, error) { return fmt.Sscanf(s, "%d", v) }

func TestFacturXPDF_RunsOverToMorePagesInsteadOfDroppingLines(t *testing.T) {
	inv := facturxInvoice()
	inv.Lines = nil
	for i := 1; i <= 120; i++ {
		inv.Lines = append(inv.Lines, UBLLine{ID: fmt.Sprint(i), Name: fmt.Sprintf("Part number %03d", i), UnitCode: "C62", Quantity: dec("1"), UnitPrice: dec("1"), Category: "S", Percent: dec("19")})
	}
	inv.AllowanceCharges = nil
	doc, issues := BuildCII(inv)
	if len(issues) != 0 {
		t.Fatalf("issues: %+v", issues)
	}
	pdf, err := FacturXPDF(doc, time.Date(2026, 11, 2, 10, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(pdf, []byte("Part number 001")) || !bytes.Contains(pdf, []byte("Part number 120")) || !bytes.Contains(pdf, []byte("Amount due")) {
		t.Fatal("a line or the totals were dropped")
	}
	if n := bytes.Count(pdf, []byte("/Type /Page ")); n < 3 {
		t.Fatalf("120 lines need more than %d pages", n)
	}
	if !bytes.Contains(pdf, []byte(fmt.Sprintf("/Count %d", bytes.Count(pdf, []byte("/Type /Page "))))) {
		t.Fatal("the page count does not match the pages")
	}
	var xref int
	idx := bytes.LastIndex(pdf, []byte("startxref\n"))
	if _, err := fmtSscan(string(pdf[idx+len("startxref\n"):]), &xref); err != nil || !bytes.HasPrefix(pdf[xref:], []byte("xref\n")) {
		t.Fatalf("startxref: %v", err)
	}
}
