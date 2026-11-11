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
	// CodeMixedRates is the EINV code for an invoice that cannot be mapped to one VAT rate.
	CodeMixedRates = "EINV-0113"
)

// Mandate renders an invoice for one mandate. Build returns the document, or the blocking issues and no document.
type Mandate interface {
	Code() string
	Format() string
	Build(inv UBLInvoice) (string, []Issue)
}

type peppolMandate struct{}

func (peppolMandate) Code() string                           { return MandatePeppol }
func (peppolMandate) Format() string                         { return "UBL21" }
func (peppolMandate) Build(inv UBLInvoice) (string, []Issue) { return BuildUBL(inv) }

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
// tax amounts per line, not rates, so the VAT rate is derived: tax over net, rounded to two decimals, one rate for the whole
// invoice (an invoice whose lines imply different rates is refused with EINV-0113). Line discounts become one document allowance
// and header charges one document charge, both at that rate, so quantity times unit price stays the line extension. Lines are named
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
	for _, l := range inv.Lines {
		netTotal = netTotal.Add(l.Net)
	}
	base := netTotal.Add(inv.Totals.Charges)
	percent := decimal.Zero
	if base.IsPositive() {
		percent = inv.Totals.Tax.Div(base).Mul(hundred).Round(2)
	}
	category := "S"
	if percent.IsZero() {
		category = "Z"
	}
	for _, l := range inv.Lines {
		if l.Net.IsPositive() {
			lineRate := l.Tax.Div(l.Net).Mul(hundred).Round(2)
			if lineRate.Sub(percent).Abs().GreaterThan(decimal.RequireFromString("0.1")) {
				reject(CodeMixedRates, "line %d implies VAT %s%% but the invoice %s%%; billing carries no rate per line, so mixed rates cannot be mapped", l.LineNo, lineRate, percent)
			}
		}
	}
	if len(issues) > 0 {
		return UBLInvoice{}, issues
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
	extension := decimal.Zero
	for _, l := range inv.Lines {
		name := l.Description
		if name == "" {
			name = "Invoice line " + strconv.Itoa(l.LineNo)
		}
		out.Lines = append(out.Lines, UBLLine{ID: strconv.Itoa(l.LineNo), Name: name, UnitCode: "C62", Quantity: l.Qty, UnitPrice: l.UnitPrice,
			Category: category, Percent: percent})
		extension = extension.Add(l.Qty.Mul(l.UnitPrice).Round(2))
	}
	if discount := extension.Sub(netTotal); discount.IsPositive() {
		out.AllowanceCharges = append(out.AllowanceCharges, UBLAllowanceCharge{Reason: "Discount", Amount: discount, Category: category, Percent: percent})
	}
	if inv.Totals.Charges.IsPositive() {
		out.AllowanceCharges = append(out.AllowanceCharges, UBLAllowanceCharge{Charge: true, Reason: "Charges", Amount: inv.Totals.Charges, Category: category, Percent: percent})
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
