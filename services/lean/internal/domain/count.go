package domain

import "github.com/shopspring/decimal"

// CountDeviationLimitPct is the deviation, in percent, above which a fixed
// rule's number of kanbans is reported against the BR-24.7 suggestion (PRD
// 4.24.16 fixed-kanban count suggestion: "Deviations > 25% reported").
const CountDeviationLimitPct = 25

// CountCheck compares a fixed rule's configured number of kanbans with the
// BR-24.7 suggestion for a demand. It is advisory: nothing is applied.
type CountCheck struct {
	Configured int
	Suggested  int64
	// DeviationPct is |suggested − configured| ÷ configured × 100, to two
	// places; 100 when nothing is configured and a card is suggested.
	DeviationPct decimal.Decimal
	// Reportable is set when the exact deviation is above the limit.
	Reportable bool
}

// CheckFixedCount computes the BR-24.7 suggestion for a fixed rule from its
// lead time in hours (the formula takes days) and compares it with the
// configured number of kanbans.
func CheckFixedCount(configured int, dailyDemand decimal.Decimal, leadTimeH int, safetyPct, fixedQty decimal.Decimal) (CountCheck, error) {
	if configured < 0 || leadTimeH < 0 {
		return CountCheck{}, errorf(CodeInvalid, "number of kanbans and lead time must not be negative")
	}
	leadDays := decimal.NewFromInt(int64(leadTimeH)).DivRound(decimal.NewFromInt(24), 24)
	suggested, err := FixedKanbanCount(dailyDemand, leadDays, safetyPct, fixedQty)
	if err != nil {
		return CountCheck{}, err
	}
	out := CountCheck{Configured: configured, Suggested: suggested, DeviationPct: decimal.Zero}
	diff := suggested - int64(configured)
	if diff < 0 {
		diff = -diff
	}
	switch {
	case configured == 0 && suggested > 0:
		out.DeviationPct = decimal.NewFromInt(100)
		out.Reportable = true
	case configured > 0:
		out.DeviationPct = decimal.NewFromInt(diff*100).DivRound(decimal.NewFromInt(int64(configured)), 2)
		// Compared in integers so rounding never hides a deviation just above the limit.
		out.Reportable = diff*100 > CountDeviationLimitPct*int64(configured)
	}
	return out, nil
}
