package domain

import (
	"time"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/money"
)

// InstallmentTerm is one installment of the payment terms (FR-29.18): a share of
// the gross and the days after the invoice date it falls due.
type InstallmentTerm struct {
	Percent    decimal.Decimal
	OffsetDays int
}

// Installment is one open item created at post (BR-29.11).
type Installment struct {
	No      int
	Amount  decimal.Decimal
	DueDate time.Time
}

// SplitInstallments splits gross into installments. Installment i is
// round_half_even(gross × pct_i) and the last one takes the residual, so the
// installments sum to gross exactly. The percentages must total 100 (BIL-0011).
func SplitInstallments(currency string, gross decimal.Decimal, invoiceDate time.Time, terms []InstallmentTerm) ([]Installment, error) {
	if len(terms) == 0 {
		return nil, &Error{Code: "BIL-0011", Detail: "installment terms need at least one installment"}
	}
	total := decimal.Zero
	for _, t := range terms {
		if !t.Percent.IsPositive() || t.OffsetDays < 0 {
			return nil, &Error{Code: "BIL-0011", Detail: "each installment needs a positive percentage and a non-negative offset"}
		}
		total = total.Add(t.Percent)
	}
	if !total.Equal(decimal.NewFromInt(100)) {
		return nil, &Error{Code: "BIL-0011", Detail: "installment percentages total " + total.String() + "%, expected 100%"}
	}
	exp := money.MinorUnits(currency)
	day := dateOnly(invoiceDate)
	out := make([]Installment, len(terms))
	sum := decimal.Zero
	last := len(terms) - 1
	for i, t := range terms {
		amount := gross.Mul(t.Percent).Div(decimal.NewFromInt(100)).RoundBank(exp)
		if i == last {
			amount = gross.Sub(sum)
		}
		sum = sum.Add(amount)
		out[i] = Installment{No: i + 1, Amount: amount, DueDate: day.AddDate(0, 0, t.OffsetDays)}
	}
	return out, nil
}

// Prorate returns amount × service days ÷ period days (BR-29.8), where service
// days are the inclusive overlap of [serviceFrom, serviceTo] with the inclusive
// billing period. Days are counted in calendar days, so February 2028 has 29.
// The result is rounded half-even to the minor unit; no overlap gives zero.
func Prorate(amount money.Amount, periodFrom, periodTo, serviceFrom, serviceTo time.Time) (money.Amount, error) {
	pf, pt := dateOnly(periodFrom), dateOnly(periodTo)
	sf, st := dateOnly(serviceFrom), dateOnly(serviceTo)
	if pt.Before(pf) || st.Before(sf) {
		return money.Amount{}, &Error{Code: "COM-0001", Detail: "period and service ranges must end on or after their start"}
	}
	from, to := maxDate(pf, sf), minDate(pt, st)
	if to.Before(from) {
		return money.Amount{Value: decimal.Zero, Currency: amount.Currency}, nil
	}
	serviceDays := decimal.NewFromInt(inclusiveDays(from, to))
	periodDays := decimal.NewFromInt(inclusiveDays(pf, pt))
	value := amount.Value.Mul(serviceDays).Div(periodDays).RoundBank(money.MinorUnits(amount.Currency))
	return money.Amount{Value: value, Currency: amount.Currency}, nil
}

func inclusiveDays(from, to time.Time) int64 {
	return int64(to.Sub(from)/(24*time.Hour)) + 1
}

func dateOnly(t time.Time) time.Time {
	y, m, d := t.Date()
	return time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
}

func maxDate(a, b time.Time) time.Time {
	if a.After(b) {
		return a
	}
	return b
}

func minDate(a, b time.Time) time.Time {
	if a.Before(b) {
		return a
	}
	return b
}
