package domain

import "github.com/shopspring/decimal"

// Match outcomes for a freight bill against a carrier invoice (BR-34.4).
const (
	MatchMatched    = "MATCHED"
	MatchVariance   = "VARIANCE"
	MatchUnexpected = "UNEXPECTED_INVOICE"
)

// Line is one charge: a charge code and an amount in the document currency.
type Line struct {
	ChargeCode string          `json:"chargeCode"`
	Amount     decimal.Decimal `json:"amount"`
}

// Tolerance is the freight invoice tolerance: an absolute amount and a percent
// of the expected charge (match_tolerance_abs / match_tolerance_pct).
type Tolerance struct {
	Abs decimal.Decimal
	Pct decimal.Decimal
}

// DefaultTolerance is $5.00 or 2 % (TMS_MATCH_TOL_ABS / TMS_MATCH_TOL_PCT).
var DefaultTolerance = Tolerance{Abs: decimal.NewFromInt(5), Pct: decimal.NewFromInt(2)}

// LineResult compares one charge code between expected and invoiced amounts.
type LineResult struct {
	ChargeCode string          `json:"chargeCode"`
	Expected   decimal.Decimal `json:"expected"`
	Invoiced   decimal.Decimal `json:"invoiced"`
	Variance   decimal.Decimal `json:"variance"`
	Allowed    decimal.Decimal `json:"allowed"`
	Within     bool            `json:"within"`
}

// MatchResult is the line-by-line outcome. Status is MATCHED only when every
// charge is within tolerance; otherwise VARIANCE. Nothing is auto-approved.
type MatchResult struct {
	Status        string          `json:"status"`
	Lines         []LineResult    `json:"lines"`
	VarianceTotal decimal.Decimal `json:"varianceTotal"`
}

// MatchInvoice compares invoiced charges with expected charges per charge code.
// The allowed variance for a charge is the smaller of the absolute tolerance and
// the percent tolerance of the expected amount, so a charge is within tolerance
// only when it passes both limits. Charges present on one side only compare
// against zero. Codes appear in expected order, then new invoice codes in order.
func MatchInvoice(expected, invoiced []Line, tol Tolerance) MatchResult {
	codes := []string{}
	seen := map[string]bool{}
	for _, l := range append(append([]Line{}, expected...), invoiced...) {
		if !seen[l.ChargeCode] {
			seen[l.ChargeCode] = true
			codes = append(codes, l.ChargeCode)
		}
	}
	res := MatchResult{Status: MatchMatched, Lines: make([]LineResult, 0, len(codes)), VarianceTotal: decimal.Zero}
	for _, code := range codes {
		exp := sumCode(expected, code)
		inv := sumCode(invoiced, code)
		variance := inv.Sub(exp)
		allowed := tol.Abs
		// The percent limit is exact (2 % of 123.45 is 2.469): rounding it to cents
		// first would let a 2.47 overage, 2.0008 %, through.
		if pct := exp.Mul(tol.Pct).Shift(-2); pct.LessThan(allowed) {
			allowed = pct
		}
		within := variance.Abs().LessThanOrEqual(allowed)
		if !within {
			res.Status = MatchVariance
		}
		res.VarianceTotal = res.VarianceTotal.Add(variance)
		res.Lines = append(res.Lines, LineResult{ChargeCode: code, Expected: exp, Invoiced: inv, Variance: variance, Allowed: allowed, Within: within})
	}
	return res
}

func sumCode(lines []Line, code string) decimal.Decimal {
	total := decimal.Zero
	for _, l := range lines {
		if l.ChargeCode == code {
			total = total.Add(l.Amount)
		}
	}
	return total
}
