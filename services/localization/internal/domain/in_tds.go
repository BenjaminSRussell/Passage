package domain

import (
	"strings"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/money"
)

// India TDS codes (FR-48.27).
const (
	CodeTDSSection = "LOC-0510" // unknown section
	CodeTDSAmount  = "LOC-0511" // amount, year-to-date figures or certificate rate out of range
	CodeTDSPayee   = "LOC-0512" // payee type not valid for the section
)

// Payee types read by 194C.
const (
	PayeeIndividualHUF = "INDIVIDUAL_HUF"
	PayeeOther         = "COMPANY"
)

// Withholding reasons reported with every result.
const (
	ReasonBelowThreshold = "BELOW_THRESHOLD"
	ReasonThreshold      = "THRESHOLD_CROSSED"
	ReasonNoPAN          = "PAN_MISSING_206AA"
	ReasonLowerDeduction = "LOWER_DEDUCTION_CERTIFICATE"
	ReasonExcessOnly     = "EXCESS_OVER_THRESHOLD"
)

const inr = "INR"

var (
	hundred = decimal.NewFromInt(100)
	two     = decimal.NewFromInt(2)
	// noPANFloor is the 206AA(1)(iii) rate of 20%.
	noPANFloor = decimal.NewFromInt(20)
)

// TDSSection is one withholding section with its rate and thresholds. Percentages
// are decimal numbers (10 means 10%). A zero threshold means none applies.
type TDSSection struct {
	Code            string          `json:"code"`
	Description     string          `json:"description"`
	RatePct         decimal.Decimal `json:"ratePct"`
	RatePctCompany  decimal.Decimal `json:"ratePctCompany"`
	SinglePayment   decimal.Decimal `json:"singlePaymentThreshold"`
	AnnualThreshold decimal.Decimal `json:"annualThreshold"`
	// NoPANFloorPct replaces the 20% of 206AA where the Act names another rate (5% for 194Q).
	NoPANFloorPct decimal.Decimal `json:"noPanFloorPct"`
	// ExcessOnly deducts only the part of the annual aggregate above the threshold (194Q).
	ExcessOnly bool `json:"excessOnly"`
}

// tdsSections holds the FR-48.27 sections under their Income-tax Act 1961 numbers,
// as the PRD names them. Thresholds are INR at the rates and limits in force for
// FY 2025-26 (Finance Act 2025; 194H at 2% since 1 October 2024). They are ASSUMED
// until tax counsel signs them off, and the Income-tax Act 2025 renumbering is a
// later pack version.
var tdsSections = map[string]TDSSection{
	// 194C contractor: 1% individual/HUF, 2% others; single payment over 30,000 or
	// annual aggregate over 1,00,000.
	"194C": {Code: "194C", Description: "Payments to contractors", RatePct: num("1"), RatePctCompany: num("2"),
		SinglePayment: num("30000"), AnnualThreshold: num("100000"), NoPANFloorPct: noPANFloor},
	// 194J professional fees: 10%; annual aggregate over 50,000. Technical services
	// at 2% and directors' fees (no threshold) are not modelled.
	"194J": {Code: "194J", Description: "Professional fees", RatePct: num("10"), RatePctCompany: num("10"),
		AnnualThreshold: num("50000"), NoPANFloorPct: noPANFloor},
	// 194I rent of land, building or furniture: 10%; rent over 50,000 for a month,
	// so the single-payment threshold applies to one month's rent. Plant and
	// machinery at 2% is not modelled.
	"194I": {Code: "194I", Description: "Rent of land, building or furniture", RatePct: num("10"), RatePctCompany: num("10"),
		SinglePayment: num("50000"), NoPANFloorPct: noPANFloor},
	// 194H commission or brokerage: 2%; annual aggregate over 20,000.
	"194H": {Code: "194H", Description: "Commission or brokerage", RatePct: num("2"), RatePctCompany: num("2"),
		AnnualThreshold: num("20000"), NoPANFloorPct: noPANFloor},
	// 194Q purchase of goods: 0.1% on the excess over 50 lakh in the year; 5% without PAN.
	"194Q": {Code: "194Q", Description: "Purchase of goods", RatePct: num("0.1"), RatePctCompany: num("0.1"),
		AnnualThreshold: num("5000000"), NoPANFloorPct: num("5"), ExcessOnly: true},
}

func num(s string) decimal.Decimal { return decimal.RequireFromString(s) }

// TDSSections returns a copy of the section table.
func TDSSections() map[string]TDSSection {
	out := make(map[string]TDSSection, len(tdsSections))
	for k, v := range tdsSections {
		out[k] = v
	}
	return out
}

// WithholdInput is one payment or credit to a deductee under one section.
type WithholdInput struct {
	Section string `json:"section"`
	// PayeeType is INDIVIDUAL_HUF or COMPANY (any other payee). Only 194C reads it.
	PayeeType string          `json:"payeeType"`
	Amount    decimal.Decimal `json:"amount"`
	// YTDPaid is the aggregate already credited to this deductee under the section this FY.
	YTDPaid decimal.Decimal `json:"ytdPaid"`
	// YTDTaxed is the part of YTDPaid that already bore TDS. Once the annual
	// threshold is crossed, the untaxed rest is deducted with this payment.
	YTDTaxed   decimal.Decimal `json:"ytdTaxed"`
	PANPresent bool            `json:"panPresent"`
	// LowerRatePct is the rate on a section 197 lower-deduction certificate, if any.
	LowerRatePct *decimal.Decimal `json:"lowerRatePct,omitempty"`
}

// WithholdResult is the TDS decision for one payment, rounded to the paisa once.
type WithholdResult struct {
	Section       string          `json:"section"`
	RatePct       decimal.Decimal `json:"ratePct"`
	TaxableAmount decimal.Decimal `json:"taxableAmount"`
	TDSAmount     decimal.Decimal `json:"tdsAmount"`
	Currency      string          `json:"currency"`
	Reason        string          `json:"reason"`
	ReturnForm    string          `json:"returnForm"`
	ChallanForm   string          `json:"challanForm"`
}

// Withhold computes TDS for one payment to a resident deductee. Rules:
//   - a payment is taxable when it exceeds the single-payment threshold or takes the
//     annual aggregate over the annual threshold;
//   - when the annual threshold is crossed the base is this payment plus any earlier
//     credit of the year that did not bear TDS (YTDPaid - YTDTaxed); a payment taxable
//     on the single-payment threshold alone is its own base;
//   - 194Q deducts only on the part of the aggregate above 50 lakh not yet taxed;
//   - the rate is the individual/HUF or other rate for 194C, else the section rate;
//   - with a PAN a section 197 certificate may lower the rate; without a PAN the rate is
//     the highest of the section rate, twice that rate and 20% (5% for 194Q) under 206AA,
//     and a certificate is ignored.
func Withhold(in WithholdInput) (WithholdResult, error) {
	sec, ok := tdsSections[strings.ToUpper(strings.TrimSpace(in.Section))]
	if !ok {
		return WithholdResult{}, fail(CodeTDSSection, "section %q is not in the India TDS table", in.Section)
	}
	if !in.Amount.IsPositive() || in.YTDPaid.IsNegative() || in.YTDTaxed.IsNegative() {
		return WithholdResult{}, fail(CodeTDSAmount, "amount must be positive; year-to-date paid and taxed must not be negative")
	}
	if in.YTDTaxed.GreaterThan(in.YTDPaid) {
		return WithholdResult{}, fail(CodeTDSAmount, "year-to-date taxed %s exceeds year-to-date paid %s", in.YTDTaxed, in.YTDPaid)
	}
	if in.LowerRatePct != nil && (in.LowerRatePct.IsNegative() || in.LowerRatePct.GreaterThan(hundred)) {
		return WithholdResult{}, fail(CodeTDSAmount, "lower-deduction rate %s%% is outside 0..100", in.LowerRatePct)
	}
	rate := sec.RatePct
	if sec.Code == "194C" {
		switch in.PayeeType {
		case PayeeIndividualHUF:
		case PayeeOther:
			rate = sec.RatePctCompany
		default:
			return WithholdResult{}, fail(CodeTDSPayee, "section 194C needs payeeType %s or %s, got %q", PayeeIndividualHUF, PayeeOther, in.PayeeType)
		}
	}

	base, reason := taxableBase(sec, in)
	res := WithholdResult{Section: sec.Code, TaxableAmount: base, Currency: inr, Reason: reason,
		ReturnForm: "26Q", ChallanForm: "ITNS 281"}
	switch {
	case !in.PANPresent:
		rate = decimal.Max(rate, rate.Mul(two), sec.NoPANFloorPct)
		res.Reason = ReasonNoPAN
	case in.LowerRatePct != nil && in.LowerRatePct.LessThan(rate):
		rate = *in.LowerRatePct
		res.Reason = ReasonLowerDeduction
	}
	if base.IsZero() {
		res.Reason = ReasonBelowThreshold
	}
	res.RatePct = rate
	res.TDSAmount = money.Amount{Value: base.Mul(rate).Div(hundred), Currency: inr}.Round().Value
	return res, nil
}

// taxableBase returns the amount subject to TDS and why it is or is not taxable.
func taxableBase(sec TDSSection, in WithholdInput) (decimal.Decimal, string) {
	total := in.YTDPaid.Add(in.Amount)
	annualHit := sec.AnnualThreshold.IsPositive() && total.GreaterThan(sec.AnnualThreshold)
	if sec.ExcessOnly {
		if !annualHit {
			return decimal.Zero, ReasonBelowThreshold
		}
		// The excess already taxed this year is YTDTaxed; deduct on the rest.
		return decimal.Max(decimal.Zero, total.Sub(sec.AnnualThreshold).Sub(in.YTDTaxed)), ReasonExcessOnly
	}
	if annualHit {
		return in.Amount.Add(in.YTDPaid.Sub(in.YTDTaxed)), ReasonThreshold
	}
	if sec.SinglePayment.IsPositive() && in.Amount.GreaterThan(sec.SinglePayment) {
		return in.Amount, ReasonThreshold
	}
	return decimal.Zero, ReasonBelowThreshold
}
