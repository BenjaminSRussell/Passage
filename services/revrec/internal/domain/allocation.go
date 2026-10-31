// Package domain holds the pure revenue-recognition rules of wave 29: standalone
// selling price (SSP) allocation of a transaction price (BR-29.1, BR-29.3) and
// the ratable schedule of an obligation. It performs no I/O.
package domain

import (
	"time"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/money"
)

// Ratable bases (§5.29.13 ratable_basis, FR-29.8 "RATABLE (daily or monthly)").
// DAILY weighs each calendar month by the share of its days the term covers;
// MONTHLY gives every calendar month the term touches the same share.
const (
	BasisDaily   = "DAILY"
	BasisMonthly = "MONTHLY"
)

// ValidRatableBasis reports whether b is a known ratable basis.
func ValidRatableBasis(b string) bool { return b == BasisDaily || b == BasisMonthly }

// Residual rules decide which obligation absorbs the rounding residual (BR-29.1).
const (
	ResidualLargest = "LARGEST"
	ResidualFirst   = "FIRST"
	ResidualLast    = "LAST"
)

// SSP sources (BR-29.2) and recognition methods (FR-29.8).
var (
	sspSources = map[string]bool{"EXPLICIT": true, "LIST": true, "PCT_OF_LIST": true, "RESIDUAL": true}
	methods    = map[string]bool{"POINT_IN_TIME": true, "RATABLE": true, "MILESTONE": true, "USAGE": true}
)

// Error is a rule violation with a stable Passage code (REV-xxxx).
type Error struct {
	Code   string
	Detail string
}

func (e *Error) Error() string { return e.Code + ": " + e.Detail }

// Obligation is a performance obligation with its standalone selling price.
type Obligation struct {
	ID        string
	SSP       decimal.Decimal
	SSPSource string
	Method    string
	Allocated decimal.Decimal
}

// ValidSSPSource reports whether s is a known SSP source.
func ValidSSPSource(s string) bool { return sspSources[s] }

// ValidMethod reports whether m is a known recognition method.
func ValidMethod(m string) bool { return methods[m] }

// ValidResidualRule reports whether r is a known residual rule.
func ValidResidualRule(r string) bool {
	return r == ResidualLargest || r == ResidualFirst || r == ResidualLast
}

// Allocate splits price across obligations in proportion to their SSP (BR-29.1).
// The SSP of a RESIDUAL obligation is derived: the transaction price less the
// other SSPs (BR-29.2); one that is not positive is refused (REV-0008).
// Each share is truncated to the currency's minor unit; the residual goes to the
// obligation chosen by rule (LARGEST means largest SSP, first index on ties).
// The returned obligations sum to price exactly.
func Allocate(price money.Amount, obs []Obligation, rule string) ([]Obligation, error) {
	if len(obs) == 0 {
		return nil, &Error{Code: "REV-0001", Detail: "a contract needs at least one obligation"}
	}
	if !ValidResidualRule(rule) {
		return nil, &Error{Code: "COM-0001", Detail: "residual rule must be LARGEST, FIRST or LAST"}
	}
	if price.Value.IsNegative() {
		return nil, &Error{Code: "REV-0001", Detail: "transaction price must not be negative"}
	}
	if err := CheckSSPSources(obs); err != nil {
		return nil, err
	}
	out := make([]Obligation, len(obs))
	copy(out, obs)
	total, residual := decimal.Zero, -1
	for i, o := range out {
		if o.SSPSource == SSPResidual {
			residual = i
			continue
		}
		if !o.SSP.IsPositive() {
			return nil, &Error{Code: "REV-0001", Detail: "every obligation needs a positive standalone selling price"}
		}
		total = total.Add(o.SSP)
	}
	if residual >= 0 {
		derived := price.Value.Sub(total)
		if !derived.IsPositive() {
			return nil, &Error{Code: "REV-0008", Detail: "the residual SSP (transaction price less the other SSPs) is not positive; the residual approach cannot be used"}
		}
		out[residual].SSP = derived
		total = price.Value
	}
	exp := money.MinorUnits(price.Currency)
	sum := decimal.Zero
	for i := range out {
		share := price.Value.Mul(out[i].SSP).Shift(exp).DivRound(total, 18).Truncate(0).Shift(-exp)
		out[i].Allocated = share
		sum = sum.Add(share)
	}
	idx := residualIndex(out, rule)
	out[idx].Allocated = out[idx].Allocated.Add(price.Value.Sub(sum))
	return out, nil
}

func residualIndex(obs []Obligation, rule string) int {
	switch rule {
	case ResidualFirst:
		return 0
	case ResidualLast:
		return len(obs) - 1
	}
	best := 0
	for i := range obs {
		if obs[i].SSP.GreaterThan(obs[best].SSP) {
			best = i
		}
	}
	return best
}

// Period is one line of a ratable schedule; Start and End are inclusive dates.
type Period struct {
	Start  time.Time
	End    time.Time
	Amount decimal.Decimal
}

// RatableSchedule spreads amount over the inclusive term [start, end] by calendar
// month (FR-29.8, BR-29.1). On the DAILY basis a month weighs the days it covers
// divided by the days in that month; on the MONTHLY basis every month the term
// touches weighs 1, so a partial first or last month gets a full share. Each
// period takes amount × weight / Σweights rounded half-even to the minor unit,
// and the last period absorbs the residual.
func RatableSchedule(amount money.Amount, start, end time.Time, basis string) ([]Period, error) {
	if !ValidRatableBasis(basis) {
		return nil, &Error{Code: "COM-0001", Detail: "ratable basis must be DAILY or MONTHLY"}
	}
	start, end = dateOnly(start), dateOnly(end)
	if end.Before(start) {
		return nil, &Error{Code: "REV-0002", Detail: "term end is before term start"}
	}
	var periods []Period
	var weights []decimal.Decimal
	for cur := start; !cur.After(end); {
		monthStart := time.Date(cur.Year(), cur.Month(), 1, 0, 0, 0, 0, time.UTC)
		monthEnd := monthStart.AddDate(0, 1, -1)
		segEnd := monthEnd
		if end.Before(monthEnd) {
			segEnd = end
		}
		days := int64(segEnd.Sub(cur)/(24*time.Hour)) + 1
		weight := decimal.NewFromInt(1)
		if basis == BasisDaily {
			weight = decimal.NewFromInt(days).Div(decimal.NewFromInt(int64(monthEnd.Day())))
		}
		weights = append(weights, weight)
		periods = append(periods, Period{Start: cur, End: segEnd})
		cur = segEnd.AddDate(0, 0, 1)
	}
	totalWeight := decimal.Zero
	for _, w := range weights {
		totalWeight = totalWeight.Add(w)
	}
	exp := money.MinorUnits(amount.Currency)
	sum := decimal.Zero
	last := len(periods) - 1
	for i := range periods {
		if i == last {
			break
		}
		periods[i].Amount = amount.Value.Mul(weights[i]).DivRound(totalWeight, 18).RoundBank(exp)
		sum = sum.Add(periods[i].Amount)
	}
	periods[last].Amount = amount.Value.Sub(sum)
	return periods, nil
}

func dateOnly(t time.Time) time.Time {
	y, m, d := t.Date()
	return time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
}
