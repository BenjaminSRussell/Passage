package domain

import (
	"bytes"
	"encoding/xml"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/shopspring/decimal"
)

// CodeRequired is the EINV code for a missing or malformed BR-35.4 element. It
// shares the EINV-011x family with the schematron and arithmetic codes.
const CodeRequired = "EINV-0110"

// Peppol BIS Billing 3.0 identifiers (BR-35.4).
const (
	peppolCustomization = "urn:cen.eu:en16931:2017#compliant#urn:fdc:peppol.eu:2017:poacc:billing:3.0"
	peppolProfile       = "urn:fdc:peppol.eu:2017:poacc:billing:01:1.0"
	nsInvoice           = "urn:oasis:names:specification:ubl:schema:xsd:Invoice-2"
	nsCreditNote        = "urn:oasis:names:specification:ubl:schema:xsd:CreditNote-2"
	nsCAC               = "urn:oasis:names:specification:ubl:schema:xsd:CommonAggregateComponents-2"
	nsCBC               = "urn:oasis:names:specification:ubl:schema:xsd:CommonBasicComponents-2"
	ublCodeInvoice      = "380"
	ublCodeCreditNote   = "381"
)

// UBLParty is one AccountingSupplierParty or AccountingCustomerParty.
type UBLParty struct {
	Name           string
	LegalName      string
	EndpointScheme string // 0088 (GLN) or 0192 (Norwegian org number)
	EndpointID     string
	CountryCode    string // ISO 3166-1 alpha-2
	VATID          string // empty when no VAT applies to the party
}

// UBLLine is one InvoiceLine (or CreditNoteLine).
type UBLLine struct {
	ID        string
	Name      string
	UnitCode  string // UN/ECE Recommendation 20, e.g. C62 or KGM
	Quantity  decimal.Decimal
	UnitPrice decimal.Decimal // net price per unit; more than two decimals is allowed
	Category  string          // VAT category S, Z, E, AE, K, G, O, L or M
	Percent   decimal.Decimal
}

// UBLAllowanceCharge is one document-level AllowanceCharge. Its VAT category and
// rate decide which VAT breakdown it lowers (allowance) or raises (charge).
type UBLAllowanceCharge struct {
	Charge   bool
	Reason   string
	Amount   decimal.Decimal
	Category string
	Percent  decimal.Decimal
}

// UBLInvoice is the input to the Peppol BIS Billing 3.0 builder. Amounts are
// derived from the lines and the document allowances and charges, so the
// document is arithmetically consistent by construction; ValidateUBL still runs
// CheckArithmetic as the contract check.
type UBLInvoice struct {
	CreditNote       bool
	ID               string
	IssueDate        string // YYYY-MM-DD in the supplier's legal date
	DueDate          string // YYYY-MM-DD
	Currency         string // ISO 4217
	BuyerReference   string
	OrderReference   string
	Supplier         UBLParty
	Customer         UBLParty
	PaymentMeansCode string // 30 credit transfer or 58 SEPA; defaults to 30
	IBAN             string
	AllowanceCharges []UBLAllowanceCharge
	Prepaid          decimal.Decimal
	// ExemptionReasons holds the TaxExemptionReason per VAT category. Categories
	// E, AE, K, G and O need one (BR-E-10, BR-AE-10, BR-IC-10, BR-G-10, BR-O-10).
	ExemptionReasons map[string]string
	Lines            []UBLLine
}

var hundred = decimal.NewFromInt(100)

// ublCategories are the EN 16931 VAT category codes (UNCL5305 subset).
var ublCategories = map[string]bool{"S": true, "Z": true, "E": true, "AE": true, "K": true, "G": true, "O": true, "L": true, "M": true}

// zeroRated lists the categories whose rate must be 0 (BR-Z-05, BR-E-05,
// BR-AE-05, BR-IC-05, BR-G-05); category O carries no rate at all (BR-O-05).
var zeroRated = map[string]bool{"Z": true, "E": true, "AE": true, "K": true, "G": true, "O": true}

type rateKey struct {
	category string
	percent  string
}

// ComputeUBLTotals derives every BR-35.4 amount. Line amounts are quantity times
// unit price rounded to the minor unit. There is one VAT breakdown per category
// and rate, in first-seen order; its taxable amount is its lines less its
// allowances plus its charges, and its tax is rounded once per breakdown.
func ComputeUBLTotals(inv UBLInvoice) UBLTotals {
	t := UBLTotals{Prepaid: inv.Prepaid.Round(2), Rounding: decimal.Zero}
	index := map[rateKey]int{}
	add := func(category string, percent, amount decimal.Decimal) {
		k := rateKey{category, percent.String()}
		i, ok := index[k]
		if !ok {
			i = len(t.TaxSubtotals)
			index[k] = i
			t.TaxSubtotals = append(t.TaxSubtotals, TaxSubtotal{Category: category, Percent: percent, Taxable: decimal.Zero, Tax: decimal.Zero})
		}
		t.TaxSubtotals[i].Taxable = t.TaxSubtotals[i].Taxable.Add(amount)
	}
	for _, l := range inv.Lines {
		amount := l.Quantity.Mul(l.UnitPrice).Round(2)
		t.Lines = append(t.Lines, TaxedLine{Category: l.Category, Percent: l.Percent, Amount: amount})
		t.LineExtension = t.LineExtension.Add(amount)
		add(l.Category, l.Percent, amount)
	}
	t.Allowances, t.Charges = decimal.Zero, decimal.Zero
	for _, ac := range inv.AllowanceCharges {
		amount := ac.Amount.Round(2)
		part := TaxedLine{Category: ac.Category, Percent: ac.Percent, Amount: amount}
		if ac.Charge {
			t.DocCharges = append(t.DocCharges, part)
			t.Charges = t.Charges.Add(amount)
			add(ac.Category, ac.Percent, amount)
		} else {
			t.DocAllowances = append(t.DocAllowances, part)
			t.Allowances = t.Allowances.Add(amount)
			add(ac.Category, ac.Percent, amount.Neg())
		}
	}
	for i := range t.TaxSubtotals {
		st := &t.TaxSubtotals[i]
		st.Tax = st.Taxable.Mul(st.Percent).Div(hundred).Round(2)
		t.TaxAmount = t.TaxAmount.Add(st.Tax)
	}
	t.TaxExclusive = t.LineExtension.Sub(t.Allowances).Add(t.Charges)
	t.TaxInclusive = t.TaxExclusive.Add(t.TaxAmount)
	t.Payable = t.TaxInclusive.Sub(t.Prepaid).Add(t.Rounding)
	return t
}

// ValidateUBL checks the BR-35.4 required elements, the VAT category rules that
// can be decided without schematron, and the arithmetic rules. An empty result
// means the invoice may be built and sent to schematron.
func ValidateUBL(inv UBLInvoice) []Issue {
	var issues []Issue
	fail := func(format string, args ...any) {
		issues = append(issues, Issue{Code: CodeRequired, Detail: fmt.Sprintf(format, args...), Reject: true})
	}
	if inv.ID == "" {
		fail("ID is required")
	}
	issue, issueErr := time.Parse("2006-01-02", inv.IssueDate)
	if issueErr != nil {
		fail("IssueDate must be YYYY-MM-DD")
	}
	if inv.DueDate == "" {
		fail("DueDate is required (PaymentTerms is not modelled)")
	} else if due, err := time.Parse("2006-01-02", inv.DueDate); err != nil {
		fail("DueDate must be YYYY-MM-DD")
	} else if issueErr == nil && due.Before(issue) {
		fail("DueDate must not be before IssueDate")
	}
	if len(inv.Currency) != 3 || strings.ToUpper(inv.Currency) != inv.Currency {
		fail("DocumentCurrencyCode must be an ISO 4217 code")
	}
	if inv.BuyerReference == "" && inv.OrderReference == "" {
		fail("BuyerReference or OrderReference is required")
	}
	checkParty := func(role string, p UBLParty) {
		if p.Name == "" {
			fail("%s name is required", role)
		}
		if p.EndpointScheme == "" || p.EndpointID == "" {
			fail("%s EndpointID with schemeID is required", role)
		}
		if len(p.CountryCode) != 2 || strings.ToUpper(p.CountryCode) != p.CountryCode {
			fail("%s country must be an ISO 3166-1 alpha-2 code", role)
		}
	}
	checkParty("AccountingSupplierParty", inv.Supplier)
	checkParty("AccountingCustomerParty", inv.Customer)
	if inv.IBAN == "" {
		fail("PayeeFinancialAccount IBAN is required")
	}
	if pm := paymentCode(inv); pm != "30" && pm != "58" {
		fail("PaymentMeansCode must be 30 or 58")
	}
	if inv.Prepaid.IsNegative() {
		fail("PrepaidAmount must not be negative")
	}
	if len(inv.Lines) == 0 {
		fail("at least one InvoiceLine is required")
	}
	used := map[string]bool{}
	checkRate := func(where, category string, percent decimal.Decimal) {
		if !ublCategories[category] {
			fail("%s VAT category %q is not one of S, Z, E, AE, K, G, O, L, M", where, category)
			return
		}
		used[category] = true
		switch {
		case zeroRated[category] && !percent.IsZero():
			fail("%s VAT category %s must have rate 0", where, category)
		case category == "S" && !percent.IsPositive():
			fail("%s VAT category S needs a rate above 0", where)
		case percent.IsNegative() || percent.GreaterThan(hundred):
			fail("%s VAT rate must be between 0 and 100", where)
		}
	}
	for i, l := range inv.Lines {
		key := fmt.Sprintf("line %d", i+1)
		if l.ID == "" || l.Name == "" || l.UnitCode == "" {
			fail("%s needs ID, Name and UnitCode", key)
		}
		if !l.Quantity.IsPositive() {
			fail("%s quantity must be positive", key)
		}
		if l.UnitPrice.IsNegative() {
			fail("%s unit price must not be negative", key)
		}
		checkRate(key, l.Category, l.Percent)
	}
	for i, ac := range inv.AllowanceCharges {
		key := fmt.Sprintf("allowance %d", i+1)
		if ac.Charge {
			key = fmt.Sprintf("charge %d", i+1)
		}
		if ac.Reason == "" {
			fail("%s needs a reason (BR-33, BR-38)", key)
		}
		if !ac.Amount.IsPositive() {
			fail("%s amount must be positive", key)
		}
		checkRate(key, ac.Category, ac.Percent)
	}
	for _, category := range []string{"E", "AE", "K", "G", "O"} {
		if used[category] && strings.TrimSpace(inv.ExemptionReasons[category]) == "" {
			fail("VAT category %s needs an exemption reason", category)
		}
	}
	if used["S"] && inv.Supplier.VATID == "" {
		fail("AccountingSupplierParty VAT identifier is required with VAT category S (BR-S-02)")
	}
	if (used["AE"] || used["K"]) && (inv.Supplier.VATID == "" || inv.Customer.VATID == "") {
		fail("reverse charge and intra-community supply need the supplier and customer VAT identifiers (BR-AE-02, BR-IC-02)")
	}
	// Every finding blocks the build, arithmetic ones included.
	for _, i := range CheckArithmetic(ComputeUBLTotals(inv)) {
		i.Reject = true
		issues = append(issues, i)
	}
	return issues
}

func paymentCode(inv UBLInvoice) string {
	if inv.PaymentMeansCode == "" {
		return "30"
	}
	return inv.PaymentMeansCode
}

// BuildUBL returns the UBL 2.1 XML for an invoice or credit note, with the
// elements in schema order. It returns no XML when ValidateUBL reports issues.
// A UBL 2.1 CreditNote has no DueDate element, so a credit note carries the due
// date as PaymentMeans/PaymentDueDate (Peppol BT-9 mapping).
func BuildUBL(inv UBLInvoice) (string, []Issue) {
	if issues := ValidateUBL(inv); len(issues) > 0 {
		return "", issues
	}
	t := ComputeUBLTotals(inv)
	cur := inv.Currency

	root, ns, typeCode, lineTag, qtyTag := "Invoice", nsInvoice, ublCodeInvoice, "InvoiceLine", "InvoicedQuantity"
	typeTag := "InvoiceTypeCode"
	if inv.CreditNote {
		root, ns, typeCode, lineTag, qtyTag = "CreditNote", nsCreditNote, ublCodeCreditNote, "CreditNoteLine", "CreditedQuantity"
		typeTag = "CreditNoteTypeCode"
	}

	var b strings.Builder
	b.WriteString(`<?xml version="1.0" encoding="UTF-8"?>` + "\n")
	fmt.Fprintf(&b, `<%s xmlns="%s" xmlns:cac="%s" xmlns:cbc="%s">`, root, ns, nsCAC, nsCBC)
	b.WriteString(text("CustomizationID", peppolCustomization))
	b.WriteString(text("ProfileID", peppolProfile))
	b.WriteString(text("ID", inv.ID))
	b.WriteString(text("IssueDate", inv.IssueDate))
	if !inv.CreditNote {
		b.WriteString(text("DueDate", inv.DueDate))
	}
	b.WriteString(text(typeTag, typeCode))
	b.WriteString(text("DocumentCurrencyCode", cur))
	if inv.BuyerReference != "" {
		b.WriteString(text("BuyerReference", inv.BuyerReference))
	}
	if inv.OrderReference != "" {
		b.WriteString(cac("OrderReference", text("ID", inv.OrderReference)))
	}
	b.WriteString(cac("AccountingSupplierParty", cac("Party", partyBody(inv.Supplier))))
	b.WriteString(cac("AccountingCustomerParty", cac("Party", partyBody(inv.Customer))))
	means := text("PaymentMeansCode", paymentCode(inv))
	if inv.CreditNote {
		means += text("PaymentDueDate", inv.DueDate)
	}
	b.WriteString(cac("PaymentMeans", means+cac("PayeeFinancialAccount", text("ID", inv.IBAN))))

	for _, ac := range inv.AllowanceCharges {
		b.WriteString(cac("AllowanceCharge", text("ChargeIndicator", strconv.FormatBool(ac.Charge))+
			text("AllowanceChargeReason", ac.Reason)+amount("Amount", cur, ac.Amount.Round(2))+
			cac("TaxCategory", rate(ac.Category, ac.Percent)+taxScheme())))
	}

	taxTotal := amount("TaxAmount", cur, t.TaxAmount)
	for _, st := range t.TaxSubtotals {
		category := rate(st.Category, st.Percent)
		if reason := strings.TrimSpace(inv.ExemptionReasons[st.Category]); reason != "" && st.Category != "S" {
			category += text("TaxExemptionReason", reason)
		}
		taxTotal += cac("TaxSubtotal", amount("TaxableAmount", cur, st.Taxable)+amount("TaxAmount", cur, st.Tax)+
			cac("TaxCategory", category+taxScheme()))
	}
	b.WriteString(cac("TaxTotal", taxTotal))

	monetary := amount("LineExtensionAmount", cur, t.LineExtension) + amount("TaxExclusiveAmount", cur, t.TaxExclusive) +
		amount("TaxInclusiveAmount", cur, t.TaxInclusive)
	if !t.Allowances.IsZero() {
		monetary += amount("AllowanceTotalAmount", cur, t.Allowances)
	}
	if !t.Charges.IsZero() {
		monetary += amount("ChargeTotalAmount", cur, t.Charges)
	}
	if !t.Prepaid.IsZero() {
		monetary += amount("PrepaidAmount", cur, t.Prepaid)
	}
	monetary += amount("PayableAmount", cur, t.Payable)
	b.WriteString(cac("LegalMonetaryTotal", monetary))

	for i, l := range inv.Lines {
		body := text("ID", l.ID) +
			fmt.Sprintf(`<cbc:%s unitCode="%s">%s</cbc:%s>`, qtyTag, esc(l.UnitCode), l.Quantity.String(), qtyTag) +
			amount("LineExtensionAmount", cur, t.Lines[i].Amount) +
			cac("Item", text("Name", l.Name)+cac("ClassifiedTaxCategory", rate(l.Category, l.Percent)+taxScheme())) +
			cac("Price", priceAmount(cur, l.UnitPrice))
		b.WriteString(cac(lineTag, body))
	}
	fmt.Fprintf(&b, "</%s>\n", root)
	return b.String(), nil
}

// rate writes a TaxCategory ID and Percent. Category O carries no rate (BR-O-05).
func rate(category string, percent decimal.Decimal) string {
	if category == "O" {
		return text("ID", category)
	}
	return text("ID", category) + text("Percent", percent.String())
}

// priceAmount keeps the unit price's own precision (at least two decimals), so
// that quantity times PriceAmount reproduces the line amount.
func priceAmount(currency string, v decimal.Decimal) string {
	places := int32(2)
	if exp := -v.Exponent(); exp > places {
		places = exp
	}
	return fmt.Sprintf(`<cbc:PriceAmount currencyID="%s">%s</cbc:PriceAmount>`, esc(currency), v.StringFixed(places))
}

func partyBody(p UBLParty) string {
	var b strings.Builder
	fmt.Fprintf(&b, `<cbc:EndpointID schemeID="%s">%s</cbc:EndpointID>`, esc(p.EndpointScheme), esc(p.EndpointID))
	b.WriteString(cac("PartyName", text("Name", p.Name)))
	b.WriteString(cac("PostalAddress", cac("Country", text("IdentificationCode", p.CountryCode))))
	if p.VATID != "" {
		b.WriteString(cac("PartyTaxScheme", text("CompanyID", p.VATID)+cac("TaxScheme", text("ID", "VAT"))))
	}
	legal := p.LegalName
	if legal == "" {
		legal = p.Name
	}
	b.WriteString(cac("PartyLegalEntity", text("RegistrationName", legal)))
	return b.String()
}

func taxScheme() string { return cac("TaxScheme", text("ID", "VAT")) }

func text(name, value string) string {
	return "<cbc:" + name + ">" + esc(value) + "</cbc:" + name + ">"
}

func amount(name, currency string, v decimal.Decimal) string {
	return fmt.Sprintf(`<cbc:%s currencyID="%s">%s</cbc:%s>`, name, esc(currency), v.StringFixed(2), name)
}

func cac(name, body string) string {
	return "<cac:" + name + ">" + body + "</cac:" + name + ">"
}

func esc(s string) string {
	var buf bytes.Buffer
	_ = xml.EscapeText(&buf, []byte(s))
	return buf.String()
}
