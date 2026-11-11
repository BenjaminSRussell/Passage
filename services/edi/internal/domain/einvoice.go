package domain

import (
	"fmt"
	"strconv"
	"time"

	"github.com/shopspring/decimal"
)

// The e-invoice mandate framework (#35, 35-2). A mandate is a country or network rule set that turns one invoice into the legal
// document its authority or access point takes. Peppol BIS Billing 3.0 (UBL 2.1) is built here; the Italian SDI (FatturaPA) and
// Mexican CFDI mandates are hooks: they are registered with a generator supplied by a country pack, and without one they refuse
// to build (EINV-0150) instead of producing a document the authority would reject.
const (
	MandatePeppol = "PEPPOL"
	MandateITSDI  = "IT_SDI"
	MandateMXCFDI = "MX_CFDI"

	// CodeMandateHook is the EINV code for a mandate whose country generator is not installed.
	CodeMandateHook = "EINV-0150"
	// CodeBillingTotal is the EINV code for an e-invoice whose payable differs from the billing invoice's gross.
	CodeBillingTotal = "EINV-0112"
)

// Mandate renders an invoice for one mandate. Build returns the document, or the blocking issues and no document.
type Mandate interface {
	Code() string
	Format() string
	Build(inv UBLInvoice) (string, []Issue)
}

type peppolMandate struct{}

func (peppolMandate) Code() string   { return MandatePeppol }
func (peppolMandate) Format() string { return "UBL21" }
func (peppolMandate) Build(inv UBLInvoice) (string, []Issue) {
	doc, issues := BuildUBL(inv)
	if len(issues) > 0 {
		return doc, issues
	}
	// The rule subset runs on the document as it will be stored and sent (CheckPeppolRules).
	if rules := CheckPeppolRules(doc); len(rules) > 0 {
		return "", rules
	}
	return doc, nil
}

// HookMandate is a mandate whose document is produced by a generator a country pack installs (FatturaPA for IT_SDI, CFDI 4.0 for
// MX_CFDI). The generator receives the same canonical invoice the Peppol builder does.
type HookMandate struct {
	MandateCode   string
	MandateFormat string
	Generate      func(UBLInvoice) (string, []Issue)
}

func (h HookMandate) Code() string   { return h.MandateCode }
func (h HookMandate) Format() string { return h.MandateFormat }
func (h HookMandate) Build(inv UBLInvoice) (string, []Issue) {
	if h.Generate == nil {
		return "", []Issue{{Code: CodeMandateHook, Reject: true,
			Detail: "No generator is installed for " + h.MandateCode + "; the mandate is a hook until its country pack is added."}}
	}
	return h.Generate(inv)
}

// Mandates is the registry the service builds with.
type Mandates map[string]Mandate

// DefaultMandates has Peppol built and the Italian and Mexican mandates as hooks without a generator. Pass the generators a country
// pack provides to replace the hooks.
func DefaultMandates(extra ...Mandate) Mandates {
	m := Mandates{
		MandatePeppol: peppolMandate{},
		MandateITSDI:  HookMandate{MandateCode: MandateITSDI, MandateFormat: "FATTURAPA"},
		MandateMXCFDI: HookMandate{MandateCode: MandateMXCFDI, MandateFormat: "CFDI40"},
	}
	for _, e := range extra {
		m[e.Code()] = e
	}
	return m
}

// EInvoiceFromBilling maps a posted billing invoice or credit note to the canonical UBL invoice. Billing carries net amounts and
// tax amounts per line, not rates, so the VAT rate is derived: tax over net per line (several rates per invoice are carried as one VAT
// breakdown per rate). Line discounts become one document allowance per rate and header charges one document charge at the rate
// billing's tax total implies, so quantity times unit price stays the line extension. Lines are named
// by their billing description and carry unit C62 (billing has no unit of measure per line). orderRef is the customer's PO from the
// sales order, when there is one; the buyer's own reference (buyerRef) is the fallback Peppol requires. The seller's IBAN and payment means code come from its profile.
func EInvoiceFromBilling(inv SourceInvoice, orderRef, buyerRef, iban, paymentMeans string, seller, buyer UBLParty) (UBLInvoice, []Issue) {
	var issues []Issue
	reject := func(code, format string, args ...any) {
		issues = append(issues, Issue{Code: code, Reject: true, Detail: fmt.Sprintf(format, args...)})
	}
	if _, err := ValidateInvoiceStatus(inv); err != nil {
		reject(CodeRequired, "%s", err.Error())
		return UBLInvoice{}, issues
	}
	if len(inv.Lines) == 0 {
		reject(CodeRequired, "invoice %s has no lines", inv.Number)
		return UBLInvoice{}, issues
	}
	netTotal := decimal.Zero
	lineTax := decimal.Zero
	rates := make([]decimal.Decimal, len(inv.Lines))
	for i, l := range inv.Lines {
		netTotal = netTotal.Add(l.Net)
		lineTax = lineTax.Add(l.Tax)
		rates[i] = lineRate(l.Net, l.Tax)
	}
	// The VAT on header charges is what billing's tax total holds beyond its lines' tax.
	chargeRate := decimal.Zero
	if inv.Totals.Charges.IsPositive() {
		chargeRate = lineRate(inv.Totals.Charges, inv.Totals.Tax.Sub(lineTax))
	}
	category := func(r decimal.Decimal) string {
		if r.IsZero() {
			return "Z"
		}
		return "S"
	}
	out := UBLInvoice{
		CreditNote: inv.Type == "CREDIT_NOTE", ID: inv.Number, IssueDate: inv.InvoiceDate, DueDate: inv.DueDate, Currency: inv.Currency,
		OrderReference: orderRef, BuyerReference: buyerRef, IBAN: iban, PaymentMeansCode: paymentMeans, Supplier: seller, Customer: buyer,
	}
	if out.DueDate == "" {
		if d, err := time.Parse("2006-01-02", inv.InvoiceDate); err == nil {
			out.DueDate = d.AddDate(0, 0, inv.PaymentTermDays).Format("2006-01-02")
		}
	}
	// A line discount is lowered at the line's own rate, so each rate has its own document allowance.
	discounts := map[string]decimal.Decimal{}
	var order []decimal.Decimal
	for i, l := range inv.Lines {
		name := l.Description
		if name == "" {
			name = "Invoice line " + strconv.Itoa(l.LineNo)
		}
		out.Lines = append(out.Lines, UBLLine{ID: strconv.Itoa(l.LineNo), Name: name, UnitCode: "C62", Quantity: l.Qty, UnitPrice: l.UnitPrice,
			Category: category(rates[i]), Percent: rates[i]})
		if d := l.Qty.Mul(l.UnitPrice).Round(2).Sub(l.Net); d.IsPositive() {
			k := rates[i].String()
			if _, seen := discounts[k]; !seen {
				order = append(order, rates[i])
			}
			discounts[k] = discounts[k].Add(d)
		}
	}
	for _, r := range order {
		out.AllowanceCharges = append(out.AllowanceCharges, UBLAllowanceCharge{Reason: "Discount", Amount: discounts[r.String()], Category: category(r), Percent: r})
	}
	if inv.Totals.Charges.IsPositive() {
		out.AllowanceCharges = append(out.AllowanceCharges, UBLAllowanceCharge{Charge: true, Reason: "Charges", Amount: inv.Totals.Charges, Category: category(chargeRate), Percent: chargeRate})
	}
	if t := ComputeUBLTotals(out); t.Payable.Sub(inv.Totals.Gross).Abs().GreaterThan(decimal.RequireFromString("0.02")) {
		reject(CodeBillingTotal, "the e-invoice payable %s differs from billing's gross %s for %s", t.Payable.StringFixed(2), inv.Totals.Gross.StringFixed(2), inv.Number)
		return UBLInvoice{}, issues
	}
	return out, nil
}

// ValidateInvoiceStatus says whether billing's invoice is final: posted, or settled in part or in full.
func ValidateInvoiceStatus(inv SourceInvoice) (bool, error) {
	switch inv.Status {
	case "POSTED", "PARTIALLY_SETTLED", "SETTLED":
		return true, nil
	}
	return false, fmt.Errorf("%w: invoice %s is %s, not posted", ErrSourceNotReady, inv.Number, inv.Status)
}

// lineRate derives the VAT rate of an amount from its tax: the tax over the net, snapped to the nearest half percent when that
// reproduces the tax within a cent or two (so 3.33 net with 0.63 tax is 19%, not 18.92%), else rounded to two decimals.
func lineRate(net, tax decimal.Decimal) decimal.Decimal {
	if !net.IsPositive() || !tax.IsPositive() {
		return decimal.Zero
	}
	raw := tax.Div(net).Mul(hundred)
	snapped := raw.Mul(decimal.NewFromInt(2)).Round(0).Div(decimal.NewFromInt(2))
	if net.Mul(snapped).Div(hundred).Sub(tax).Abs().LessThanOrEqual(decimal.RequireFromString("0.02")) {
		return snapped
	}
	return raw.Round(2)
}
