package domain

import (
	"fmt"

	"github.com/shopspring/decimal"
)

// CodeArithmetic is the EINV code for a failed BR-35.4 arithmetic rule.
const CodeArithmetic = "EINV-0111"

// TaxedLine is one invoice line's extension amount in its VAT category and rate
// (Item/ClassifiedTaxCategory ID and Percent).
type TaxedLine struct {
	Category string
	Percent  decimal.Decimal
	Amount   decimal.Decimal
}

// TaxSubtotal is one TaxTotal/TaxSubtotal of a UBL invoice.
type TaxSubtotal struct {
	Category string
	Percent  decimal.Decimal
	Taxable  decimal.Decimal
	Tax      decimal.Decimal
}

// UBLTotals carries the amounts of a UBL 2.1 invoice that BR-35.4 checks before
// the schematron runs.
type UBLTotals struct {
	Lines         []TaxedLine
	LineExtension decimal.Decimal
	Allowances    decimal.Decimal
	Charges       decimal.Decimal
	TaxExclusive  decimal.Decimal
	TaxAmount     decimal.Decimal
	TaxInclusive  decimal.Decimal
	Prepaid       decimal.Decimal
	Rounding      decimal.Decimal
	Payable       decimal.Decimal
	TaxSubtotals  []TaxSubtotal
	// DocAllowances and DocCharges are the document-level AllowanceCharge
	// amounts in their VAT category and rate. They must sum to Allowances and
	// Charges, and they lower or raise the taxable amount of their category.
	DocAllowances []TaxedLine
	DocCharges    []TaxedLine
}

// CheckArithmetic returns one issue per broken rule, in a stable order: amounts
// carry at most two decimals; the line sum equals LineExtensionAmount;
// TaxExclusiveAmount equals LineExtension minus allowances plus charges;
// TaxInclusiveAmount equals exclusive plus tax; PayableAmount equals inclusive
// minus prepaid plus rounding; each VAT breakdown (category and rate) has a
// taxable amount equal to the sum of its lines and a tax equal to the rate
// rounded to the minor unit; every line's category and rate has a breakdown;
// the breakdowns sum to TaxAmount. Document-level allowances and charges sum to
// AllowanceTotalAmount and ChargeTotalAmount (BR-CO-11, BR-CO-12) and enter the
// taxable amount of their category and rate (BR-S-08 and its siblings).
func CheckArithmetic(t UBLTotals) []Issue {
	var issues []Issue
	fail := func(format string, args ...any) {
		issues = append(issues, Issue{Code: CodeArithmetic, Detail: fmt.Sprintf(format, args...)})
	}
	amounts := []struct {
		name  string
		value decimal.Decimal
	}{
		{"LineExtensionAmount", t.LineExtension}, {"AllowanceTotalAmount", t.Allowances}, {"ChargeTotalAmount", t.Charges},
		{"TaxExclusiveAmount", t.TaxExclusive}, {"TaxAmount", t.TaxAmount}, {"TaxInclusiveAmount", t.TaxInclusive},
		{"PrepaidAmount", t.Prepaid}, {"PayableRoundingAmount", t.Rounding}, {"PayableAmount", t.Payable},
	}
	for _, a := range amounts {
		if !a.value.Equal(a.value.Round(2)) {
			fail("%s has more than two decimals", a.name)
		}
	}
	lineSum := decimal.Zero
	for _, l := range t.Lines {
		lineSum = lineSum.Add(l.Amount)
	}
	if !lineSum.Equal(t.LineExtension) {
		fail("LineExtensionAmount %s differs from the line sum %s", t.LineExtension, lineSum)
	}
	if want := t.LineExtension.Sub(t.Allowances).Add(t.Charges); !t.TaxExclusive.Equal(want) {
		fail("TaxExclusiveAmount %s differs from %s", t.TaxExclusive, want)
	}
	if want := t.TaxExclusive.Add(t.TaxAmount); !t.TaxInclusive.Equal(want) {
		fail("TaxInclusiveAmount %s differs from %s", t.TaxInclusive, want)
	}
	if want := t.TaxInclusive.Sub(t.Prepaid).Add(t.Rounding); !t.Payable.Equal(want) {
		fail("PayableAmount %s differs from %s", t.Payable, want)
	}
	sum := func(parts []TaxedLine) decimal.Decimal {
		total := decimal.Zero
		for _, p := range parts {
			total = total.Add(p.Amount)
		}
		return total
	}
	if got := sum(t.DocAllowances); !got.Equal(t.Allowances) {
		fail("AllowanceTotalAmount %s differs from the document allowances %s", t.Allowances, got)
	}
	if got := sum(t.DocCharges); !got.Equal(t.Charges) {
		fail("ChargeTotalAmount %s differs from the document charges %s", t.Charges, got)
	}
	sameRate := func(category string, percent decimal.Decimal, st TaxSubtotal) bool {
		return category == st.Category && percent.Equal(st.Percent)
	}
	rateSum := func(parts []TaxedLine, st TaxSubtotal) decimal.Decimal {
		total := decimal.Zero
		for _, p := range parts {
			if sameRate(p.Category, p.Percent, st) {
				total = total.Add(p.Amount)
			}
		}
		return total
	}
	subtotalTax := decimal.Zero
	for _, st := range t.TaxSubtotals {
		subtotalTax = subtotalTax.Add(st.Tax)
		catSum := rateSum(t.Lines, st).Sub(rateSum(t.DocAllowances, st)).Add(rateSum(t.DocCharges, st))
		if !catSum.Equal(st.Taxable) {
			fail("category %s at %s%% taxable amount %s differs from its lines, allowances and charges %s", st.Category, st.Percent, st.Taxable, catSum)
		}
		if want := st.Taxable.Mul(st.Percent).Div(decimal.NewFromInt(100)).Round(2); !st.Tax.Equal(want) {
			fail("category %s at %s%% tax %s differs from %s", st.Category, st.Percent, st.Tax, want)
		}
	}
	covered := func(category string, percent decimal.Decimal) bool {
		for _, st := range t.TaxSubtotals {
			if sameRate(category, percent, st) {
				return true
			}
		}
		return false
	}
	for i, l := range t.Lines {
		if !covered(l.Category, l.Percent) {
			fail("line %d category %s at %s%% has no VAT breakdown", i+1, l.Category, l.Percent)
		}
	}
	for _, group := range []struct {
		name  string
		parts []TaxedLine
	}{{"allowance", t.DocAllowances}, {"charge", t.DocCharges}} {
		for i, p := range group.parts {
			if !covered(p.Category, p.Percent) {
				fail("document %s %d category %s at %s%% has no VAT breakdown", group.name, i+1, p.Category, p.Percent)
			}
		}
	}
	if !subtotalTax.Equal(t.TaxAmount) {
		fail("TaxAmount %s differs from the subtotal sum %s", t.TaxAmount, subtotalTax)
	}
	return issues
}
