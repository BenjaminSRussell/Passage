package domain

import (
	"math"

	"github.com/shopspring/decimal"
)

// Risk weights (FR-30.10). The five components sum to 100 at their maxima:
// overdue ratio 40, average days late 20, dunning level 20, open disputes 15,
// broken promises 5.
const (
	riskOverdueWeight  = 40.0
	riskDaysWeight     = 20.0
	riskDaysCap        = 60.0
	riskDunningWeight  = 5.0 // per level, levels 0..4
	riskMaxDunning     = 4
	riskDisputeWeight  = 5.0 // per open dispute, capped at 3
	riskMaxDisputes    = 3
	riskPromiseWeight  = 2.5 // per broken promise, capped at 2
	riskMaxBrokenPromi = 2
)

// RiskInputs are the customer facts the score is computed from.
type RiskInputs struct {
	// OverdueAmount and TotalOpen are in one currency (entity currency).
	OverdueAmount decimal.Decimal
	TotalOpen     decimal.Decimal
	// AvgDaysLate is the open-item-weighted average days past due, never negative.
	AvgDaysLate    int
	DunningLevel   int
	OpenDisputes   int
	BrokenPromises int
}

// RiskScore returns a 0-100 score (higher is riskier). Inputs outside their
// documented ranges are clamped, so the score is always within bounds.
func RiskScore(in RiskInputs) int {
	ratio := 0.0
	if in.TotalOpen.IsPositive() {
		r, _ := in.OverdueAmount.Div(in.TotalOpen).Float64()
		ratio = clamp(r, 0, 1)
	}
	days := clamp(float64(in.AvgDaysLate)/riskDaysCap, 0, 1)
	score := riskOverdueWeight*ratio +
		riskDaysWeight*days +
		riskDunningWeight*clamp(float64(in.DunningLevel), 0, riskMaxDunning) +
		riskDisputeWeight*clamp(float64(in.OpenDisputes), 0, riskMaxDisputes) +
		riskPromiseWeight*clamp(float64(in.BrokenPromises), 0, riskMaxBrokenPromi)
	return int(math.Round(clamp(score, 0, 100)))
}

func clamp(v, lo, hi float64) float64 {
	return math.Max(lo, math.Min(hi, v))
}
