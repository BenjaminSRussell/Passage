package domain

import (
	"strings"
	"testing"

	"github.com/shopspring/decimal"
)

func sampleInvoice(credit bool) UBLInvoice {
	return UBLInvoice{
		CreditNote:     credit,
		ID:             "INV-2026-000123",
		IssueDate:      "2026-10-01",
		DueDate:        "2026-10-31",
		Currency:       "EUR",
		BuyerReference: "PO-7788",
		Supplier: UBLParty{Name: "Demo Manufacturer & Co", EndpointScheme: "0088", EndpointID: "4006403333338",
			CountryCode: "DE", VATID: "DE123456789"},
		Customer: UBLParty{Name: "Demo Distributor", EndpointScheme: "0088", EndpointID: "4000001234561",
			CountryCode: "FR"},
		IBAN: "DE89370400440532013000",
		Lines: []UBLLine{
			{ID: "1", Name: "Widget", UnitCode: "C62", Quantity: d("10"), UnitPrice: d("10.00"), Category: "S", Percent: d("21")},
			{ID: "2", Name: "Bulk resin", UnitCode: "KGM", Quantity: d("50"), UnitPrice: d("1.00"), Category: "Z", Percent: d("0")},
		},
		// A 5.00 discount on the standard-rated goods: S taxable 95.00, tax 19.95.
		AllowanceCharges: []UBLAllowanceCharge{{Reason: "Discount", Amount: d("5.00"), Category: "S", Percent: d("21")}},
		ExemptionReasons: map[string]string{},
	}
}

func TestBuildUBL_RequiredElementsAndTotals(t *testing.T) {
	xmlOut, issues := BuildUBL(sampleInvoice(false))
	if len(issues) != 0 {
		t.Fatalf("issues = %+v", issues)
	}
	for _, want := range []string{
		"<cbc:CustomizationID>", peppolCustomization, peppolProfile, "<cbc:InvoiceTypeCode>", ">380<",
		"<cbc:DocumentCurrencyCode>EUR", `schemeID="0088"`, `<cbc:TaxAmount currencyID="EUR">19.95</cbc:TaxAmount>`,
		`<cbc:TaxableAmount currencyID="EUR">95.00</cbc:TaxableAmount>`,
		`<cbc:LineExtensionAmount currencyID="EUR">150.00</cbc:LineExtensionAmount>`,
		`<cbc:TaxExclusiveAmount currencyID="EUR">145.00</cbc:TaxExclusiveAmount>`,
		`<cbc:TaxInclusiveAmount currencyID="EUR">164.95</cbc:TaxInclusiveAmount>`,
		`<cbc:AllowanceTotalAmount currencyID="EUR">5.00</cbc:AllowanceTotalAmount>`,
		`<cbc:PayableAmount currencyID="EUR">164.95</cbc:PayableAmount>`,
		`<cac:AllowanceCharge><cbc:ChargeIndicator>false</cbc:ChargeIndicator><cbc:AllowanceChargeReason>Discount</cbc:AllowanceChargeReason>`,
		"<cbc:DueDate>2026-10-31</cbc:DueDate>",
		`unitCode="KGM">50</cbc:InvoicedQuantity>`, "<cbc:ID>VAT</cbc:ID>", "Demo Manufacturer &amp; Co",
		"<cac:InvoiceLine>",
	} {
		if !strings.Contains(xmlOut, want) {
			t.Errorf("missing %q", want)
		}
	}
	if strings.Contains(xmlOut, "CreditNote") {
		t.Error("invoice must not use the CreditNote root")
	}
}

func TestBuildUBL_CreditNoteUsesCreditNoteElements(t *testing.T) {
	xmlOut, issues := BuildUBL(sampleInvoice(true))
	if len(issues) != 0 {
		t.Fatalf("issues = %+v", issues)
	}
	for _, want := range []string{"<CreditNote ", "<cbc:CreditNoteTypeCode>", ">381<", "<cac:CreditNoteLine>", "<cbc:CreditedQuantity",
		"<cbc:PaymentMeansCode>30</cbc:PaymentMeansCode><cbc:PaymentDueDate>2026-10-31</cbc:PaymentDueDate>"} {
		if !strings.Contains(xmlOut, want) {
			t.Errorf("missing %q", want)
		}
	}
	// A UBL 2.1 CreditNote has no DueDate element.
	if strings.Contains(xmlOut, "<cbc:DueDate>") {
		t.Error("credit note must carry the due date in PaymentMeans, not cbc:DueDate")
	}
}

// Germany has two standard rates; each needs its own S breakdown (BR-S-08).
func TestBuildUBL_TwoStandardRatesGetTwoBreakdowns(t *testing.T) {
	inv := sampleInvoice(false)
	inv.AllowanceCharges = nil
	inv.Lines[1] = UBLLine{ID: "2", Name: "Manual", UnitCode: "C62", Quantity: d("1"), UnitPrice: d("50.00"), Category: "S", Percent: d("7")}
	xmlOut, issues := BuildUBL(inv)
	if len(issues) != 0 {
		t.Fatalf("issues = %+v", issues)
	}
	tot := ComputeUBLTotals(inv)
	if len(tot.TaxSubtotals) != 2 || !tot.TaxAmount.Equal(d("24.50")) {
		t.Fatalf("subtotals %+v tax %s, want two and 24.50", tot.TaxSubtotals, tot.TaxAmount)
	}
	if !strings.Contains(xmlOut, "<cbc:Percent>7</cbc:Percent>") || !strings.Contains(xmlOut, "<cbc:Percent>21</cbc:Percent>") {
		t.Error("both rates must appear")
	}
}

// oInvoice is a not-subject-to-VAT invoice: category O alone, no VAT
// identifier on either party, the supplier identified by its legal
// registration (BR-O-02, BR-O-11, BR-CO-26).
func oInvoice() UBLInvoice {
	inv := sampleInvoice(false)
	inv.Supplier.VATID, inv.Supplier.LegalID = "", "HRB 12345"
	for i := range inv.Lines {
		inv.Lines[i].Category, inv.Lines[i].Percent = "O", d("0")
	}
	inv.AllowanceCharges[0].Category, inv.AllowanceCharges[0].Percent = "O", d("0")
	inv.ExemptionReasons = map[string]string{"O": "Not subject to VAT"}
	return inv
}

func TestBuildUBL_ExemptCategoryCarriesReasonAndOHasNoRate(t *testing.T) {
	xmlOut, issues := BuildUBL(oInvoice())
	if len(issues) != 0 {
		t.Fatalf("issues = %+v", issues)
	}
	if !strings.Contains(xmlOut, "<cbc:ID>O</cbc:ID><cbc:TaxExemptionReason>Not subject to VAT</cbc:TaxExemptionReason>") {
		t.Errorf("O breakdown must carry the reason and no Percent: %s", xmlOut)
	}
	if !strings.Contains(xmlOut, "<cbc:RegistrationName>Demo Manufacturer &amp; Co</cbc:RegistrationName><cbc:CompanyID>HRB 12345</cbc:CompanyID>") {
		t.Errorf("the supplier's legal registration must be written as PartyLegalEntity/CompanyID: %s", xmlOut)
	}
}

// Each case is an input the builder used to accept although the Peppol BIS
// 3.0.21 schematron rejects the XML it produced (rule ids from running
// testdata/peppol/validate.py on that XML). The builder must now refuse it.
func TestValidateUBL_RefusesWhatPeppolSchematronRejects(t *testing.T) {
	de := func(u *UBLInvoice) {
		u.Customer.CountryCode, u.Customer.VATID = "DE", "DE987654321"
	}
	tests := []struct {
		rule string
		edit func(*UBLInvoice)
	}{
		{"PEPPOL-COMMON-R040 GLN check digit", func(u *UBLInvoice) { u.Supplier.EndpointID = "4006403333333" }},
		{"PEPPOL-COMMON-R040 GLN length", func(u *UBLInvoice) { u.Customer.EndpointID = "400000123456" }},
		{"PEPPOL-COMMON-R041 org number check digit", func(u *UBLInvoice) {
			u.Customer.EndpointScheme, u.Customer.EndpointID = "0192", "974760674"
		}},
		{"BR-O-11 O with another category", func(u *UBLInvoice) {
			*u = oInvoice()
			u.Lines[1].Category = "Z"
		}},
		{"BR-O-02 O with a customer VAT id", func(u *UBLInvoice) {
			*u = oInvoice()
			u.Customer.VATID = "FR12345678901"
		}},
		{"BR-CO-26 supplier without any identifier", func(u *UBLInvoice) {
			*u = oInvoice()
			u.Supplier.LegalID = ""
		}},
		{"BR-Z-02 Z without supplier VAT id", func(u *UBLInvoice) {
			u.AllowanceCharges, u.Lines = nil, u.Lines[1:]
			u.Supplier.VATID, u.Supplier.LegalID = "", "HRB 12345"
		}},
		{"BR-E-02 E without supplier VAT id", func(u *UBLInvoice) {
			u.AllowanceCharges, u.Lines = nil, u.Lines[1:]
			u.Lines[0].Category = "E"
			u.ExemptionReasons = map[string]string{"E": "Exempt"}
			u.Supplier.VATID, u.Supplier.LegalID = "", "HRB 12345"
		}},
		{"BR-IC-11 K without delivery date", func(u *UBLInvoice) {
			u.Lines[1].Category = "K"
			u.Customer.VATID = "FR12345678901"
			u.ExemptionReasons = map[string]string{"K": "Intra-community supply"}
			u.DeliveryCountry = "FR"
		}},
		{"BR-IC-12 K without deliver-to country", func(u *UBLInvoice) {
			u.Lines[1].Category = "K"
			u.Customer.VATID = "FR12345678901"
			u.ExemptionReasons = map[string]string{"K": "Intra-community supply"}
			u.DeliveryDate = "2026-09-30"
		}},
		{"DE-R-002 domestic German without seller contact", func(u *UBLInvoice) {
			de(u)
			u.Supplier.CityName, u.Supplier.PostalZone, u.Customer.CityName, u.Customer.PostalZone = "Köln", "50667", "Bonn", "53111"
		}},
		{"DE-R-003 domestic German without seller city", func(u *UBLInvoice) {
			de(u)
			u.Supplier.ContactName, u.Supplier.ContactPhone, u.Supplier.ContactEmail = "Billing", "+49 221 123456", "billing@example.de"
			u.Supplier.PostalZone, u.Customer.CityName, u.Customer.PostalZone = "50667", "Bonn", "53111"
		}},
		{"DE-R-015 domestic German without buyer reference", func(u *UBLInvoice) {
			de(u)
			u.Supplier.ContactName, u.Supplier.ContactPhone, u.Supplier.ContactEmail = "Billing", "+49 221 123456", "billing@example.de"
			u.Supplier.CityName, u.Supplier.PostalZone, u.Customer.CityName, u.Customer.PostalZone = "Köln", "50667", "Bonn", "53111"
			u.BuyerReference, u.OrderReference = "", "PO-7788"
		}},
	}
	for _, tc := range tests {
		t.Run(tc.rule, func(t *testing.T) {
			inv := sampleInvoice(false)
			tc.edit(&inv)
			xmlOut, issues := BuildUBL(inv)
			if len(issues) == 0 || xmlOut != "" {
				t.Fatalf("accepted; Peppol rejects this (%s)", tc.rule)
			}
		})
	}
	// The same shapes with the missing data supplied are built.
	k := sampleInvoice(false)
	k.Lines[1].Category = "K"
	k.Customer.VATID = "FR12345678901"
	k.ExemptionReasons = map[string]string{"K": "Intra-community supply"}
	k.DeliveryDate, k.DeliveryCountry = "2026-09-30", "FR"
	xmlOut, issues := BuildUBL(k)
	if len(issues) != 0 || !strings.Contains(xmlOut, "<cac:Delivery><cbc:ActualDeliveryDate>2026-09-30</cbc:ActualDeliveryDate>"+
		"<cac:DeliveryLocation><cac:Address><cac:Country><cbc:IdentificationCode>FR</cbc:IdentificationCode></cac:Country></cac:Address></cac:DeliveryLocation></cac:Delivery>") {
		t.Fatalf("K with delivery: %+v %s", issues, xmlOut)
	}
	dom := sampleInvoice(false)
	de(&dom)
	dom.Supplier.ContactName, dom.Supplier.ContactPhone, dom.Supplier.ContactEmail = "Billing", "+49 221 123456", "billing@example.de"
	dom.Supplier.StreetName, dom.Supplier.CityName, dom.Supplier.PostalZone = "Hauptstr. 1", "Köln", "50667"
	dom.Customer.CityName, dom.Customer.PostalZone = "Bonn", "53111"
	xmlOut, issues = BuildUBL(dom)
	if len(issues) != 0 || !strings.Contains(xmlOut, "<cac:PostalAddress><cbc:StreetName>Hauptstr. 1</cbc:StreetName><cbc:CityName>Köln</cbc:CityName><cbc:PostalZone>50667</cbc:PostalZone><cac:Country>") ||
		!strings.Contains(xmlOut, "<cac:Contact><cbc:Name>Billing</cbc:Name><cbc:Telephone>+49 221 123456</cbc:Telephone><cbc:ElectronicMail>billing@example.de</cbc:ElectronicMail></cac:Contact></cac:Party></cac:AccountingSupplierParty>") {
		t.Fatalf("domestic German: %+v %s", issues, xmlOut)
	}
}

func TestBuildUBL_PriceKeepsItsPrecision(t *testing.T) {
	inv := sampleInvoice(false)
	inv.Lines[0].UnitPrice = d("0.125")
	inv.Lines[0].Quantity = d("8")
	xmlOut, issues := BuildUBL(inv)
	if len(issues) != 0 {
		t.Fatalf("issues = %+v", issues)
	}
	if !strings.Contains(xmlOut, `<cbc:PriceAmount currencyID="EUR">0.125</cbc:PriceAmount>`) {
		t.Error("PriceAmount must not be rounded to two decimals")
	}
}

func TestValidateUBL_RequiredElements(t *testing.T) {
	tests := []struct {
		name string
		edit func(*UBLInvoice)
	}{
		{"missing ID", func(u *UBLInvoice) { u.ID = "" }},
		{"bad issue date", func(u *UBLInvoice) { u.IssueDate = "01/10/2026" }},
		{"missing due date", func(u *UBLInvoice) { u.DueDate = "" }},
		{"lowercase currency", func(u *UBLInvoice) { u.Currency = "eur" }},
		{"no buyer or order reference", func(u *UBLInvoice) { u.BuyerReference = "" }},
		{"customer endpoint missing", func(u *UBLInvoice) { u.Customer.EndpointID = "" }},
		{"supplier country", func(u *UBLInvoice) { u.Supplier.CountryCode = "Germany" }},
		{"no IBAN", func(u *UBLInvoice) { u.IBAN = "" }},
		{"payment code 10", func(u *UBLInvoice) { u.PaymentMeansCode = "10" }},
		{"no lines", func(u *UBLInvoice) { u.Lines = nil }},
		{"zero quantity", func(u *UBLInvoice) { u.Lines[0].Quantity = d("0") }},
		{"negative price", func(u *UBLInvoice) { u.Lines[0].UnitPrice = d("-1") }},
		{"due before issue", func(u *UBLInvoice) { u.DueDate = "2026-09-30" }},
		{"unknown VAT category", func(u *UBLInvoice) { u.Lines[0].Category = "X" }},
		{"zero-rated with a rate", func(u *UBLInvoice) { u.Lines[1].Percent = d("5") }},
		{"standard rate of zero", func(u *UBLInvoice) { u.Lines[0].Percent = d("0") }},
		{"exempt without reason", func(u *UBLInvoice) { u.Lines[1].Category = "E" }},
		{"reverse charge without customer VAT", func(u *UBLInvoice) {
			u.Lines[1].Category = "AE"
			u.ExemptionReasons = map[string]string{"AE": "Reverse charge"}
		}},
		{"S without supplier VAT", func(u *UBLInvoice) { u.Supplier.VATID = "" }},
		{"allowance without reason", func(u *UBLInvoice) { u.AllowanceCharges[0].Reason = "" }},
		{"allowance of zero", func(u *UBLInvoice) { u.AllowanceCharges[0].Amount = d("0") }},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			inv := sampleInvoice(false)
			tc.edit(&inv)
			xmlOut, issues := BuildUBL(inv)
			if len(issues) == 0 {
				t.Fatal("expected issues")
			}
			if xmlOut != "" {
				t.Error("no XML may be produced when validation fails")
			}
			for _, i := range issues {
				if i.Code != CodeRequired {
					t.Errorf("code = %s", i.Code)
				}
			}
		})
	}
}

func TestComputeUBLTotals_Allowances(t *testing.T) {
	tot := ComputeUBLTotals(sampleInvoice(false))
	if !tot.Payable.Equal(d("164.95")) {
		t.Fatalf("payable = %s", tot.Payable)
	}
	if len(tot.TaxSubtotals) != 2 {
		t.Fatalf("subtotals = %d", len(tot.TaxSubtotals))
	}
	if issues := CheckArithmetic(tot); len(issues) != 0 {
		t.Fatalf("arithmetic issues %+v", issues)
	}
}

// Property: any positive line set with document allowances and charges built by
// the function passes the arithmetic contract, and the payable amount equals
// the lines less allowances plus charges plus tax.
func TestComputeUBLTotals_PropertyArithmeticHolds(t *testing.T) {
	cats := []struct {
		code    string
		percent decimal.Decimal
	}{{"S", d("21")}, {"S", d("21")}, {"Z", d("0")}, {"AE", d("0")}}
	for seed := 0; seed < 200; seed++ {
		var lines []UBLLine
		n := 1 + seed%7
		for i := 0; i < n; i++ {
			c := cats[(seed+i)%len(cats)]
			qty := decimal.NewFromInt(int64(1 + (seed*31+i*7)%50))
			price := decimal.New(int64(1+(seed*13+i*101)%99999), -2)
			lines = append(lines, UBLLine{ID: "x", Name: "n", UnitCode: "C62", Quantity: qty, UnitPrice: price,
				Category: c.code, Percent: c.percent})
		}
		inv := sampleInvoice(false)
		inv.Lines = lines
		c := cats[seed%len(cats)]
		inv.AllowanceCharges = []UBLAllowanceCharge{
			{Reason: "Discount", Amount: decimal.New(int64(seed%3), -2), Category: c.code, Percent: c.percent},
			{Charge: true, Reason: "Freight", Amount: decimal.New(int64(seed%5), 0), Category: c.code, Percent: c.percent},
		}
		tot := ComputeUBLTotals(inv)
		if issues := CheckArithmetic(tot); len(issues) != 0 {
			t.Fatalf("seed %d: %+v", seed, issues)
		}
		want := tot.LineExtension.Sub(tot.Allowances).Add(tot.Charges).Add(tot.TaxAmount)
		if !tot.Payable.Equal(want) {
			t.Fatalf("seed %d: payable %s want %s", seed, tot.Payable, want)
		}
	}
}
