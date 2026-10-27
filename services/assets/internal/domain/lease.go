package domain

import (
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/money"
)

// Periodic rate methods (lease.periodicRateMethod, PRD 7.37.13). Nominal is
// annual rate / payments per year (the default); Effective is
// (1 + annual rate)^(1 / payments per year) - 1.
const (
	RateNominal   = "Nominal"
	RateEffective = "Effective"
)

// LeaseTerms are the inputs to an IFRS 16 liability and ROU schedule. Payment is
// per payment; PaymentsPerYear is 1, 2, 4 or 12; TermMonths must be a whole number
// of payments; Advance false means arrears; AnnualRate is the IBR, e.g. 0.05.
// RateMethod is RateNominal (also when empty) or RateEffective.
type LeaseTerms struct {
	Payment         decimal.Decimal
	PaymentsPerYear int
	TermMonths      int
	Advance         bool
	AnnualRate      decimal.Decimal
	Currency        string
	RateMethod      string
}

// LeaseLine is one period of the liability amortization and ROU depreciation.
// Remeasurement is the modification adjustment added to Opening before the
// period's payment and interest (BR-37.8); it is zero except on the first
// period after a modification, so Closing(n) = Opening(n+1) always holds.
type LeaseLine struct {
	PeriodNo        int
	Opening         decimal.Decimal
	Remeasurement   decimal.Decimal
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

// ComputeLease builds the schedule under IFRS 16. The final interest is the
// plug that clears the residual, so the last closing balance is exactly zero
// and the sum of interest equals the sum of payments minus the recognized
// liability.
func ComputeLease(t LeaseTerms) (LeaseSchedule, error) {
	n, err := validateLease(t)
	if err != nil {
		return LeaseSchedule{}, err
	}
	minor := money.MinorUnits(t.Currency)
	r := periodicRate(t)
	liability := annuityPV(t.Payment, r, n, t.Advance).Round(minor)
	lines := amortize(liability, liability, t.Payment, r, 1, n, t.Advance, minor, liability)
	return LeaseSchedule{Liability: liability, ROUAsset: liability, Lines: lines}, nil
}

// validateLease checks the terms and returns the number of payments.
func validateLease(t LeaseTerms) (int, error) {
	if len(t.Currency) != 3 {
		return 0, fail(CodeInvalidLease, "currency must be an ISO 4217 code")
	}
	if t.AnnualRate.LessThanOrEqual(decimal.Zero) || t.AnnualRate.GreaterThan(maxIBR) {
		return 0, fail(CodeIBRRange, "lease IBR must be above 0 and at most 50%")
	}
	if t.Payment.LessThanOrEqual(decimal.Zero) {
		return 0, fail(CodeInvalidLease, "payment must be positive")
	}
	if t.PaymentsPerYear != 1 && t.PaymentsPerYear != 2 && t.PaymentsPerYear != 4 && t.PaymentsPerYear != 12 {
		return 0, fail(CodeInvalidLease, "payments per year must be 1, 2, 4 or 12")
	}
	if t.RateMethod != "" && t.RateMethod != RateNominal && t.RateMethod != RateEffective {
		return 0, fail(CodeInvalidLease, "rate method must be Nominal or Effective")
	}
	if t.TermMonths < 1 || t.TermMonths > MaxLeaseTermMonths || (t.TermMonths*t.PaymentsPerYear)%12 != 0 {
		return 0, fail(CodeInvalidLease, "term must be a whole number of payments")
	}
	return t.TermMonths * t.PaymentsPerYear / 12, nil
}

// periodicRate is the rate per payment period under the lease's rate method.
func periodicRate(t LeaseTerms) decimal.Decimal {
	ppy := decimal.NewFromInt(int64(t.PaymentsPerYear))
	if t.RateMethod == RateEffective && t.PaymentsPerYear > 1 {
		return nthRoot(decimal.NewFromInt(1).Add(t.AnnualRate), t.PaymentsPerYear).Sub(decimal.NewFromInt(1))
	}
	return t.AnnualRate.Div(ppy)
}

// nthRoot returns x^(1/k) for x > 0 and k >= 1 by Newton iteration at a fixed
// scale, so the same inputs always give the same schedule.
func nthRoot(x decimal.Decimal, k int) decimal.Decimal {
	const scale = 24
	one := decimal.NewFromInt(1)
	kd := decimal.NewFromInt(int64(k))
	km1 := decimal.NewFromInt(int64(k - 1))
	y := one.Add(x.Sub(one).DivRound(kd, scale))
	for i := 0; i < 100; i++ {
		pow := one
		for j := 0; j < k-1; j++ {
			pow = pow.Mul(y).Round(scale)
		}
		next := km1.Mul(y).Add(x.DivRound(pow, scale)).DivRound(kd, scale)
		if next.Equal(y) {
			break
		}
		y = next
	}
	return y
}

// amortize runs the liability amortization for n payments numbered from
// first. opening is the balance at the start of the first period; start is
// the balance the remaining payments amortize (opening plus any remeasurement,
// recorded on the first line). rou is spread straight-line over the n
// periods; the last period absorbs the rounding residual and its interest is
// the plug that closes the liability at exactly zero.
func amortize(opening, start, payment, r decimal.Decimal, first, n int, advance bool, minor int32, rou decimal.Decimal) []LeaseLine {
	lines := make([]LeaseLine, 0, n)
	depCum := decimal.Zero
	perPeriodDep := rou.Div(decimal.NewFromInt(int64(n))).Round(minor)
	open, bal := opening, start
	for k := 1; k <= n; k++ {
		last := k == n
		l := LeaseLine{PeriodNo: first + k - 1, Opening: open, Remeasurement: bal.Sub(open), Payment: payment}
		if advance {
			afterPay := bal.Sub(payment)
			l.Interest = afterPay.Mul(r).Round(minor)
			if last {
				l.Interest = afterPay.Neg() // residual plug to zero
			}
			l.Closing = afterPay.Add(l.Interest)
		} else {
			l.Interest = bal.Mul(r).Round(minor)
			if last {
				l.Interest = payment.Sub(bal) // residual plug to zero
			}
			l.Closing = bal.Add(l.Interest).Sub(payment)
		}
		dep := perPeriodDep
		if last {
			dep = rou.Sub(depCum)
		}
		depCum = depCum.Add(dep)
		l.ROUDepreciation = dep
		lines = append(lines, l)
		open, bal = l.Closing, l.Closing
	}
	return lines
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
