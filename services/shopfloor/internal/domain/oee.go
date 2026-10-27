package domain

import (
	"errors"
	"fmt"

	"github.com/shopspring/decimal"
)

// ErrOEEInput reports a bucket that breaks an OEE invariant.
var ErrOEEInput = errors.New("oee: invalid bucket")

// OEEInput is one resource bucket (BR-25.5). Planned minutes are the scheduled
// open minutes less planned downtime; unplanned downtime comes out of run time.
type OEEInput struct {
	ScheduledMinutes     decimal.Decimal
	PlannedDownMinutes   decimal.Decimal
	UnplannedDownMinutes decimal.Decimal
	IdealCycleSeconds    decimal.Decimal
	TotalCount           int64
	GoodCount            int64
}

// OEE holds the ISO 22400 aligned factors. Each factor is rounded to 4 places;
// OEE is the product of the unrounded factors, rounded to 4 places.
type OEE struct {
	PlannedMinutes decimal.Decimal
	RunMinutes     decimal.Decimal
	Availability   decimal.Decimal
	Performance    decimal.Decimal
	Quality        decimal.Decimal
	OEE            decimal.Decimal
	// OverRate is set when the performance ratio exceeded 1.0 and was capped.
	OverRate bool
}

var (
	oneDec     = decimal.NewFromInt(1)
	sixtyDec   = decimal.NewFromInt(60)
	ratioPlace = int32(10)
)

// Compute applies BR-25.5:
//
//	A = run / planned
//	P = (ideal cycle s x total count / 60) / run, capped at 1.0
//	Q = good / total (1.0 when total is 0)
//	OEE = A x P x Q
func Compute(in OEEInput) (OEE, error) {
	planned := in.ScheduledMinutes.Sub(in.PlannedDownMinutes)
	run := planned.Sub(in.UnplannedDownMinutes)
	switch {
	case in.TotalCount < 0 || in.GoodCount < 0 || in.GoodCount > in.TotalCount:
		return OEE{}, fmt.Errorf("%w: counts good=%d total=%d", ErrOEEInput, in.GoodCount, in.TotalCount)
	case planned.LessThanOrEqual(decimal.Zero):
		return OEE{}, fmt.Errorf("%w: no planned time", ErrOEEInput)
	case run.IsNegative():
		return OEE{}, fmt.Errorf("%w: downtime exceeds planned time", ErrOEEInput)
	}
	out := OEE{PlannedMinutes: planned, RunMinutes: run}
	a := run.DivRound(planned, ratioPlace)

	p := decimal.Zero
	if run.IsPositive() {
		idealMinutes := in.IdealCycleSeconds.Mul(decimal.NewFromInt(in.TotalCount)).Div(sixtyDec)
		p = idealMinutes.DivRound(run, ratioPlace)
		if p.GreaterThan(oneDec) {
			p = oneDec
			out.OverRate = true
		}
	}

	q := oneDec
	if in.TotalCount > 0 {
		q = decimal.NewFromInt(in.GoodCount).DivRound(decimal.NewFromInt(in.TotalCount), ratioPlace)
	}

	out.Availability = a.Round(4)
	out.Performance = p.Round(4)
	out.Quality = q.Round(4)
	out.OEE = a.Mul(p).Mul(q).Round(4)
	return out, nil
}
