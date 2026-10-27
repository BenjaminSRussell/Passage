package domain

import (
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/money"
)

// LeaseTerms are the inputs to an IFRS 16 liability and ROU schedule. Payment is
// per payment; PaymentsPerYear is 1, 2, 4 or 12; TermMonths must be a whole number
// of payments; Advance false means arrears; AnnualRate is the IBR, e.g. 0.05.
type LeaseTerms struct {
	Payment         decimal.Decimal
	PaymentsPerYear int
	TermMonths      int
	Advance         bool
	AnnualRate      decimal.Decimal
	Currency        string
}

// LeaseLine is one period of the liability amortization and ROU depreciation.
type LeaseLine struct {
	PeriodNo        int
	Opening         decimal.Decimal
	Interest        decimal.Decimal
	Payment         decimal.Decimal
	Closing         decimal.Decimal
	ROUDepreciation decimal.Decimal
}

// LeaseSchedule is the recognized liability, the ROU asset and every line.
type LeaseSchedule struct {
	Liability decimal.Decimal
	ROUAsset  decimal.Decimal
	Lines     []LeaseLine
}

var maxIBR = decimal.RequireFromString("0.5")

// MaxLeaseTermMonths bounds a lease term (50 years).
const MaxLeaseTermMonths = 600

// ComputeLease builds the schedule under IFRS 16 with the nominal periodic rate
// (annual rate / payments per year). The final interest is the plug that clears
// the residual, so the last closing balance is exactly zero and the sum of
// interest equals the sum of payments minus the recognized liability.
func ComputeLease(t LeaseTerms) (LeaseSchedule, error) {
	if len(t.Currency) != 3 {
		return LeaseSchedule{}, fail(CodeInvalidLease, "currency must be an ISO 4217 code")
	}
	if t.AnnualRate.LessThanOrEqual(decimal.Zero) || t.AnnualRate.GreaterThan(maxIBR) {
		return LeaseSchedule{}, fail(CodeIBRRange, "lease IBR must be above 0 and at most 50%")
	}
	if t.Payment.LessThanOrEqual(decimal.Zero) {
		return LeaseSchedule{}, fail(CodeInvalidLease, "payment must be positive")
	}
	if t.PaymentsPerYear != 1 && t.PaymentsPerYear != 2 && t.PaymentsPerYear != 4 && t.PaymentsPerYear != 12 {
		return LeaseSchedule{}, fail(CodeInvalidLease, "payments per year must be 1, 2, 4 or 12")
	}
	if t.TermMonths < 1 || t.TermMonths > MaxLeaseTermMonths || (t.TermMonths*t.PaymentsPerYear)%12 != 0 {
		return LeaseSchedule{}, fail(CodeInvalidLease, "term must be a whole number of payments")
	}
	minor := money.MinorUnits(t.Currency)
	n := t.TermMonths * t.PaymentsPerYear / 12
	r := t.AnnualRate.Div(decimal.NewFromInt(int64(t.PaymentsPerYear)))
	liability := annuityPV(t.Payment, r, n, t.Advance).Round(minor)
	rou := liability

	lines := make([]LeaseLine, 0, n)
	open := liability
	depCum := decimal.Zero
	perPeriodDep := rou.Div(decimal.NewFromInt(int64(n))).Round(minor)
	for k := 1; k <= n; k++ {
		last := k == n
		l := LeaseLine{PeriodNo: k, Opening: open, Payment: t.Payment}
		if t.Advance {
			afterPay := open.Sub(t.Payment)
			l.Interest = afterPay.Mul(r).Round(minor)
			if last {
				l.Interest = afterPay.Neg() // residual plug to zero
			}
			l.Closing = afterPay.Add(l.Interest)
		} else {
			l.Interest = open.Mul(r).Round(minor)
			if last {
				l.Interest = t.Payment.Sub(open) // residual plug to zero
			}
			l.Closing = open.Add(l.Interest).Sub(t.Payment)
		}
		dep := perPeriodDep
		if last {
			dep = rou.Sub(depCum)
		}
		depCum = depCum.Add(dep)
		l.ROUDepreciation = dep
		lines = append(lines, l)
		open = l.Closing
	}
	return LeaseSchedule{Liability: liability, ROUAsset: rou, Lines: lines}, nil
}

// annuityPV is the present value of n payments at periodic rate r.
func annuityPV(payment, r decimal.Decimal, n int, advance bool) decimal.Decimal {
	growth := decimal.NewFromInt(1).Add(r)
	pow := decimal.NewFromInt(1)
	for i := 0; i < n; i++ {
		pow = pow.Mul(growth).Round(18)
	}
	factor := decimal.NewFromInt(1).Sub(decimal.NewFromInt(1).Div(pow)).Div(r)
	if advance {
		factor = factor.Mul(growth)
	}
	return payment.Mul(factor)
}
