package domain

import (
	"errors"
	"os"
	"strings"
	"testing"
	"time"
)

func TestMapINVOIC_SelfBilled389ReadsLineKeysAndLineTax(t *testing.T) {
	raw, err := os.ReadFile("../../testdata/edifact/invoic-389-selfbilled.edi")
	if err != nil {
		t.Fatal(err)
	}
	ic, broken, err := ReadEdifact(string(raw))
	if err != nil || len(broken) != 0 || len(ic.Messages) != 1 {
		t.Fatalf("read: %v %v", err, broken)
	}
	inv, err := MapINVOIC(ic.Messages[0])
	if err != nil {
		t.Fatal(err)
	}
	if !inv.SelfBilled || inv.CreditNote || inv.Number != "SB-9001" || len(inv.Lines) != 1 {
		t.Fatalf("invoice: %+v", inv)
	}
	l := inv.Lines[0]
	if l.DeliveryNote != "SHP-0001" || l.POLine != "10" || l.Tax == nil || !l.Tax.Equal(dec("6.80")) || !l.Quantity.Equal(dec("10")) || !l.UnitPrice.Equal(dec("8")) {
		t.Fatalf("line: %+v", l)
	}
	// Tax stated only inside the lines becomes the header tax, so the total still equals lines plus tax.
	if len(inv.Taxes) != 1 || !inv.Taxes[0].Amount.Equal(dec("6.80")) || !inv.Taxes[0].Percent.Equal(dec("8.5")) {
		t.Fatalf("taxes: %+v", inv.Taxes)
	}
	// A 380 is not self-billed and keeps no line keys it did not state.
	plain, err := os.ReadFile("../../testdata/edifact/invoic-from-invoice.edi")
	if err != nil {
		t.Fatal(err)
	}
	ic, _, _ = ReadEdifact(string(plain))
	got, err := MapINVOIC(ic.Messages[0])
	if err != nil || got.SelfBilled || got.Lines[0].DeliveryNote != "" || got.Lines[0].Tax != nil {
		t.Fatalf("380: %+v %v", got, err)
	}
}

func TestSelfBillingLines_SumsShippedContainersPerPOLineAndItem(t *testing.T) {
	shipped := time.Date(2026, 10, 20, 14, 0, 0, 0, time.UTC)
	p := TradeProfile{LegalEntityID: "e", CustomerID: "c"}
	sh := SourceShipment{ID: "s1", ShipmentNumber: "SHP-7", LegalEntityID: "e", CustomerID: "c", Status: "SHIPPED", ActualShipDate: &shipped,
		Lines: []SourceShipLine{{ID: "sl1", OrderLineID: "ol1", ProductID: "prod-a"}, {ID: "sl2", OrderLineID: "ol2", ProductID: "prod-b"}},
		Containers: []SourceContainer{
			{SSCC: "1", Status: "SHIPPED", Lines: []SourceContainerLine{{ShipmentLineID: "sl1", Qty: dec("4")}, {ShipmentLineID: "sl2", Qty: dec("1")}}},
			{SSCC: "2", Status: "SHIPPED", Lines: []SourceContainerLine{{ShipmentLineID: "sl1", Qty: dec("6")}}},
			{SSCC: "3", Status: "CLOSED", Lines: []SourceContainerLine{{ShipmentLineID: "sl2", Qty: dec("99")}}},
		}}
	o := SourceOrder{ID: "o", LegalEntityID: "e", CustomerID: "c", ExternalRef: "PO",
		Lines: []SourceOrderLine{{ID: "ol1", LineNo: 10, UnitPrice: dec("8")}, {ID: "ol2", LineNo: 20, UnitPrice: dec("2.5")}}}
	got, err := SelfBillingLines(p, sh, o)
	if err != nil || len(got) != 2 {
		t.Fatalf("lines: %v %v", got, err)
	}
	if got[0].POLine != "10" || got[0].Item != "prod-a" || !got[0].Quantity.Equal(dec("10")) || !got[0].UnitPrice.Equal(dec("8")) || got[0].DeliveryNote != "SHP-7" {
		t.Fatalf("first: %+v", got[0])
	}
	if got[1].POLine != "20" || !got[1].Quantity.Equal(dec("1")) {
		t.Fatalf("an unshipped container adds nothing: %+v", got[1])
	}
	sh.Containers[0].Lines[0].ShipmentLineID = "missing"
	if _, err := SelfBillingLines(p, sh, o); !errors.Is(err, ErrOutbound) {
		t.Fatalf("a pack of an unknown line is refused: %v", err)
	}
}

func TestMatchSelfBilling_NoTaxLineIsNotComparedOnTax(t *testing.T) {
	ship := SBShipment{ID: "s", DeliveryNote: "D", POLine: "1", Item: "i", Quantity: dec("1"), UnitPrice: dec("100"), TaxRatePct: dec("19"), ShippedOn: time.Now()}
	line := SBLine{Line: "1", DeliveryNote: "D", POLine: "1", Item: "i", Quantity: dec("1"), UnitPrice: dec("100")}
	res, err := MatchSelfBilling([]SBLine{line}, []SBShipment{ship}, DefaultSBTolerance(), time.Now(), 30)
	if err != nil || res.Lines[0].Outcome != SelfBillDifference {
		t.Fatalf("a stated zero tax differs from 19 percent: %+v %v", res, err)
	}
	line.NoTax = true
	res, _ = MatchSelfBilling([]SBLine{line}, []SBShipment{ship}, DefaultSBTolerance(), time.Now(), 30)
	if res.Lines[0].Outcome != SelfBillMatched {
		t.Fatalf("an unstated tax is not compared: %+v", res)
	}
}

func TestMapCXMLDocument_RequiredElements(t *testing.T) {
	read := func(name string) ([]byte, CXMLEnvelope) {
		raw, err := os.ReadFile("../../testdata/cxml/" + name)
		if err != nil {
			t.Fatal(err)
		}
		env, err := ParseCXML(raw)
		if err != nil {
			t.Fatal(err)
		}
		return raw, env
	}
	raw, env := read("confirmation.xml")
	c, err := MapCXMLDocument(raw, env)
	if err != nil || c.Number != "CONF-9" || c.Purpose != "detail" || len(c.OrderIDs) != 1 || c.Lines[0].Status != "accept" || c.Lines[0].Quantity != "10" {
		t.Fatalf("confirmation: %+v %v", c, err)
	}
	raw, env = read("ship-notice.xml")
	c, err = MapCXMLDocument(raw, env)
	if err != nil || c.Number != "DIST-ASN-55" || c.Lines[0].Item != "prod-a" || !strings.HasPrefix(c.Date, "2026-11-04") {
		t.Fatalf("ship notice: %+v %v", c, err)
	}
	for _, tc := range []struct{ name, from, to string }{
		{"ship-notice.xml", `shipmentID="DIST-ASN-55"`, `shipmentID=""`},
		{"confirmation.xml", `<OrderReference orderID="PO-1001" orderDate="2026-10-15T00:00:00-05:00"/>`, ``},
		{"invoice-detail.xml", `invoiceDate="2026-11-05T00:00:00-05:00"`, `invoiceDate=""`},
	} {
		raw, env := read(tc.name)
		_, err := MapCXMLDocument([]byte(strings.Replace(string(raw), tc.from, tc.to, 1)), env)
		if !errors.Is(err, ErrCXMLDocument) {
			t.Errorf("%s without %s: %v", tc.name, tc.from, err)
		}
	}
	if _, err := MapCXMLDocument([]byte("<cXML payloadID=\"p\"><Request><X/></Request></cXML"), CXMLEnvelope{DocType: "X"}); err == nil {
		t.Error("a document that is not well-formed must be refused")
	}
}

func TestParsePunchOutSetupResponse(t *testing.T) {
	ok := `<cXML><Response><Status code="200" text="OK"/><PunchOutSetupResponse><StartPage><URL>https://s.example/start?x=1</URL></StartPage></PunchOutSetupResponse></Response></cXML>`
	if u, err := ParsePunchOutSetupResponse([]byte(ok)); err != nil || u != "https://s.example/start?x=1" {
		t.Fatalf("ok: %q %v", u, err)
	}
	for name, doc := range map[string]string{
		"refused":   `<cXML><Response><Status code="401" text="bad secret"/></Response></cXML>`,
		"no start":  `<cXML><Response><Status code="200" text="OK"/></Response></cXML>`,
		"bad url":   strings.Replace(ok, "https://s.example/start?x=1", "javascript:alert(1)", 1),
		"not xml":   `nope`,
		"no number": `<cXML><Response><Status code="ok"/></Response></cXML>`,
	} {
		if _, err := ParsePunchOutSetupResponse([]byte(doc)); !errors.Is(err, ErrPunchoutSetup) {
			t.Errorf("%s: %v", name, err)
		}
	}
	req := BuildPunchOutSetupRequest("p<1>", "2026-11-05T00:00:00Z", "B&Co", "S", "sec", "cookie", "https://as2/return?t=a&b=c")
	for _, want := range []string{`payloadID="p&lt;1&gt;"`, "<Identity>B&amp;Co</Identity>", "<SharedSecret>sec</SharedSecret>", "<BuyerCookie>cookie</BuyerCookie>", "t=a&amp;b=c"} {
		if !strings.Contains(req, want) {
			t.Errorf("setup request lacks %q: %s", want, req)
		}
	}
	if strings.Contains(BuildPunchOutSetupRequest("p", "t", "B", "S", "", "c", "https://r"), "SharedSecret") {
		t.Error("no shared secret element without a secret")
	}
	if c, err := PunchOutBuyerCookie([]byte(`<cXML><Message><PunchOutOrderMessage><BuyerCookie> abc </BuyerCookie></PunchOutOrderMessage></Message></cXML>`)); err != nil || c != "abc" {
		t.Errorf("cookie: %q %v", c, err)
	}
	if u, err := OCIStartURL("https://s.example/oci?a=1", "https://as2/return?t=x"); err != nil || !strings.Contains(u, "HOOK_URL=https%3A%2F%2Fas2%2Freturn%3Ft%3Dx") || !strings.Contains(u, "a=1") {
		t.Errorf("oci: %q %v", u, err)
	}
}

func TestEInvoiceFromBilling_DiscountChargesAndMixedRates(t *testing.T) {
	seller := UBLParty{Name: "Seller", EndpointScheme: "0088", EndpointID: "0614141000012", CountryCode: "US", VATID: "US1", LegalID: "L1", StreetName: "s", CityName: "c", PostalZone: "1"}
	buyer := UBLParty{Name: "Buyer", EndpointScheme: "0088", EndpointID: "0614141000029", CountryCode: "US", LegalID: "L2", StreetName: "s", CityName: "c", PostalZone: "2"}
	inv := SourceInvoice{ID: "i", Number: "INV-9", Type: "SHIPMENT", Status: "POSTED", Currency: "USD", InvoiceDate: "2026-11-01", PaymentTermDays: 30,
		Totals: SourceInvoiceTotals{Net: dec("90"), Charges: dec("10"), Tax: dec("10"), Gross: dec("110")},
		Lines:  []SourceInvoiceLine{{LineNo: 1, Qty: dec("10"), UnitPrice: dec("10"), Net: dec("90"), Tax: dec("9")}}}
	doc, issues := EInvoiceFromBilling(inv, "PO-1", "", "DE89370400440532013000", "58", seller, buyer)
	if len(issues) != 0 {
		t.Fatalf("issues: %+v", issues)
	}
	// 100 extension, 10 discount, 10 charge, 10 percent: due date from the terms, one allowance and one charge, payable 110.
	if doc.DueDate != "2026-12-01" || len(doc.AllowanceCharges) != 2 || doc.AllowanceCharges[0].Charge || !doc.AllowanceCharges[1].Charge || doc.Lines[0].Name != "Invoice line 1" {
		t.Fatalf("document: %+v", doc)
	}
	if got := ComputeUBLTotals(doc).Payable; !got.Equal(dec("110")) {
		t.Fatalf("payable %s", got)
	}
	// A credit note carries the flag; an unposted invoice is refused.
	credit := inv
	credit.Type = "CREDIT_NOTE"
	if d, _ := EInvoiceFromBilling(credit, "", "REF", "", "", seller, buyer); !d.CreditNote {
		t.Fatal("credit note flag lost")
	}
	draft := inv
	draft.Status = "PROPOSAL"
	if _, is := EInvoiceFromBilling(draft, "PO-1", "", "", "", seller, buyer); len(is) != 1 || is[0].Code != CodeRequired {
		t.Fatalf("draft: %+v", is)
	}
	// Two lines implying 10 and 0 percent cannot be one rate.
	mixed := inv
	mixed.Lines = append([]SourceInvoiceLine{}, inv.Lines...)
	mixed.Lines = append(mixed.Lines, SourceInvoiceLine{LineNo: 2, Qty: dec("1"), UnitPrice: dec("10"), Net: dec("10"), Tax: dec("0")})
	if _, is := EInvoiceFromBilling(mixed, "PO-1", "", "", "", seller, buyer); len(is) == 0 || is[0].Code != CodeMixedRates {
		t.Fatalf("mixed: %+v", is)
	}
	// A payable that is not billing's gross is refused (EINV-0112).
	off := inv
	off.Totals.Gross = dec("200")
	if _, is := EInvoiceFromBilling(off, "PO-1", "", "", "", seller, buyer); len(is) != 1 || is[0].Code != CodeBillingTotal {
		t.Fatalf("gross: %+v", is)
	}
	// A hook mandate without a generator refuses to build.
	if _, is := DefaultMandates()[MandateMXCFDI].Build(doc); len(is) != 1 || is[0].Code != CodeMandateHook {
		t.Fatalf("hook: %+v", is)
	}
}
