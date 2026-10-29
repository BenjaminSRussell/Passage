package domain

import (
	"fmt"
	"regexp"

	"github.com/shopspring/decimal"
)

// CodeSchema is the catalog code for a statutory output that fails its schema (ERP-0801).
const CodeSchema = "ERP-0801"

// VATReturn is the body of HMRC's MTD VAT "submit VAT return for period"
// call (VAT (MTD) API 1.0), boxes 1 to 9 under HMRC's field names. Amounts
// are decimals parsed from the JSON number text, never floats.
type VATReturn struct {
	PeriodKey                    string           `json:"periodKey"`
	VATDueSales                  *decimal.Decimal `json:"vatDueSales"`                  // box 1
	VATDueAcquisitions           *decimal.Decimal `json:"vatDueAcquisitions"`           // box 2
	TotalVATDue                  *decimal.Decimal `json:"totalVatDue"`                  // box 3
	VATReclaimedCurrPeriod       *decimal.Decimal `json:"vatReclaimedCurrPeriod"`       // box 4
	NetVATDue                    *decimal.Decimal `json:"netVatDue"`                    // box 5
	TotalValueSalesExVAT         *decimal.Decimal `json:"totalValueSalesExVAT"`         // box 6
	TotalValuePurchasesExVAT     *decimal.Decimal `json:"totalValuePurchasesExVAT"`     // box 7
	TotalValueGoodsSuppliedExVAT *decimal.Decimal `json:"totalValueGoodsSuppliedExVAT"` // box 8
	TotalAcquisitionsExVAT       *decimal.Decimal `json:"totalAcquisitionsExVAT"`       // box 9
	Finalised                    *bool            `json:"finalised"`
}

// FieldError points at one failed check of a statutory output.
type FieldError struct {
	Field  string `json:"field"`
	Detail string `json:"detail"`
}

// HMRC period keys are four characters, letters and digits, occasionally with '#'.
var periodKeyPattern = regexp.MustCompile(`^[0-9A-Za-z#]{4}$`)

type mtdBox struct {
	field string
	value *decimal.Decimal
	min   decimal.Decimal
	max   decimal.Decimal
	scale int32
}

// ValidateMTDVAT checks a return against HMRC's request schema and the two
// business rules HMRC enforces on submission: every box present within its
// range and scale (boxes 1 to 4 signed with pence, box 5 non-negative with
// pence, boxes 6 to 9 signed whole pounds), finalised = true, box 3 = box 1 +
// box 2 (VAT_TOTAL_VALUE) and box 5 = |box 3 - box 4| (VAT_NET_VALUE). It
// returns every failure, not just the first.
func ValidateMTDVAT(r VATReturn) []FieldError {
	var errs []FieldError
	add := func(field, format string, args ...any) {
		errs = append(errs, FieldError{Field: field, Detail: fmt.Sprintf(format, args...)})
	}
	if !periodKeyPattern.MatchString(r.PeriodKey) {
		add("periodKey", "%q is not an HMRC period key (four characters, e.g. 26A1)", r.PeriodKey)
	}
	pence := decimal.RequireFromString("9999999999999.99")
	pounds := decimal.RequireFromString("9999999999999")
	boxes := []mtdBox{
		{"vatDueSales", r.VATDueSales, pence.Neg(), pence, 2},
		{"vatDueAcquisitions", r.VATDueAcquisitions, pence.Neg(), pence, 2},
		{"totalVatDue", r.TotalVATDue, pence.Neg(), pence, 2},
		{"vatReclaimedCurrPeriod", r.VATReclaimedCurrPeriod, pence.Neg(), pence, 2},
		{"netVatDue", r.NetVATDue, decimal.Zero, decimal.RequireFromString("99999999999.99"), 2},
		{"totalValueSalesExVAT", r.TotalValueSalesExVAT, pounds.Neg(), pounds, 0},
		{"totalValuePurchasesExVAT", r.TotalValuePurchasesExVAT, pounds.Neg(), pounds, 0},
		{"totalValueGoodsSuppliedExVAT", r.TotalValueGoodsSuppliedExVAT, pounds.Neg(), pounds, 0},
		{"totalAcquisitionsExVAT", r.TotalAcquisitionsExVAT, pounds.Neg(), pounds, 0},
	}
	complete := true
	for _, b := range boxes {
		switch {
		case b.value == nil:
			add(b.field, "is required")
			complete = false
		case !b.value.Equal(b.value.Truncate(b.scale)):
			if b.scale == 0 {
				add(b.field, "whole pounds only")
			} else {
				add(b.field, "at most two decimal places")
			}
		case b.value.LessThan(b.min) || b.value.GreaterThan(b.max):
			add(b.field, "must be between %s and %s", b.min, b.max)
		}
	}
	if r.Finalised == nil || !*r.Finalised {
		add("finalised", "must be true: HMRC accepts only a finalised return")
	}
	if !complete {
		return errs
	}
	if !r.TotalVATDue.Equal(r.VATDueSales.Add(*r.VATDueAcquisitions)) {
		add("totalVatDue", "must equal vatDueSales + vatDueAcquisitions (box 3 = box 1 + box 2)")
	}
	if !r.NetVATDue.Equal(r.TotalVATDue.Sub(*r.VATReclaimedCurrPeriod).Abs()) {
		add("netVatDue", "must equal |totalVatDue - vatReclaimedCurrPeriod| (box 5 = |box 3 - box 4|)")
	}
	return errs
}
