package domain

import (
	"github.com/shopspring/decimal"
)

// RetroPeriod is one finalized period that a backdated change touches: its
// original earning lines and the earning lines recalculated under the rule-set
// versions the original run was bound to (BR-43.4).
type RetroPeriod struct {
	Prior        []Line
	Recalculated []Line
}

// RetroDelta books the earning differences of one or more finalized periods as
// RETRO_<code> earning lines for the current period (BR-43.4, FR-43.8): deltas
// are summed per earning code across periods, and unchanged codes produce
// nothing. Only EARNING lines are compared. Taxes and deductions on the deltas
// are not carried back; they follow the current-period method when the
// RETRO_* lines are calculated as earnings of the current run (AC-43.4).
// Order follows the first appearance of each code.
func RetroDelta(periods ...RetroPeriod) []Line {
	var order []string
	sums := map[string]decimal.Decimal{}
	src := map[string]Line{}
	add := func(l Line, sign decimal.Decimal) {
		if l.Kind != KindEarning {
			return
		}
		if _, seen := sums[l.Code]; !seen {
			order = append(order, l.Code)
			sums[l.Code] = decimal.Zero
			src[l.Code] = l
		}
		sums[l.Code] = sums[l.Code].Add(l.Amount.Mul(sign))
	}
	minus := decimal.NewFromInt(-1)
	for _, p := range periods {
		for _, l := range p.Recalculated {
			add(l, decimal.NewFromInt(1))
			src[l.Code] = l // explain with the recalculated basis and version
		}
		for _, l := range p.Prior {
			add(l, minus)
		}
	}

	var out []Line
	for _, code := range order {
		delta := sums[code]
		if delta.IsZero() {
			continue
		}
		s := src[code]
		out = append(out, Line{
			Code: "RETRO_" + code, Kind: KindEarning, Amount: delta,
			BasisAmount: s.BasisAmount, RuleVersion: s.RuleVersion,
		})
	}
	return out
}

// RetroEarnings turns retro delta lines into earnings for the current period's
// calculation, so taxes recompute on the new gross (BR-43.4).
func RetroEarnings(lines []Line) []Earning {
	out := make([]Earning, 0, len(lines))
	for _, l := range lines {
		out = append(out, Earning{Code: l.Code, Amount: l.Amount})
	}
	return out
}

// RetroSum is the total of retro delta lines; used to check that deltas tie to
// the change that produced them.
func RetroSum(lines []Line) decimal.Decimal {
	total := decimal.Zero
	for _, l := range lines {
		total = total.Add(l.Amount)
	}
	return total
}
