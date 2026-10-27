package domain

import (
	"time"

	"github.com/shopspring/decimal"
)

// MaxAnalysisDays bounds the period of one kanban rule analysis.
const MaxAnalysisDays = 366

// Cycle is one replenishment cycle of a kanban card: the card was planned
// (released for replenishment), and completed. A reused fixed-rule card has
// one cycle per completion. PlannedAt is nil when the planning time was not
// recorded (cards planned before it was kept); Due is nil when the card had
// no due time.
type Cycle struct {
	PlannedAt   *time.Time
	Due         *time.Time
	CompletedAt time.Time
}

// RuleKPI is the kanban rule analysis of PRD 4.24.14 over a period: cycles,
// average lead time and the share of cycles completed after their due time.
// Averages and shares are nil when nothing was measured, never zero.
type RuleKPI struct {
	// Cycles counts the cards completed in the period.
	Cycles int
	// TimedCycles counts the cycles with a recorded planning time; the
	// average lead time is over these.
	TimedCycles int
	// AvgLeadTimeH is the mean of completion minus planning, in hours to two
	// places.
	AvgLeadTimeH *decimal.Decimal
	// DueCycles counts the cycles with a due time.
	DueCycles int
	// LateCycles counts the due cycles completed after their due time.
	LateCycles int
	// LatePct is LateCycles ÷ DueCycles × 100 to two places.
	LatePct *decimal.Decimal
}

// AnalyzeCycles computes the rule KPI from the cycles of a period. Lead time is
// measured from planning, when the rule's lead time starts to run and the due
// time is set, to completion; a cycle completed exactly at its due time is on
// time.
func AnalyzeCycles(cycles []Cycle) RuleKPI {
	out := RuleKPI{Cycles: len(cycles)}
	lead := decimal.Zero
	for _, c := range cycles {
		if c.PlannedAt != nil && !c.CompletedAt.Before(*c.PlannedAt) {
			out.TimedCycles++
			lead = lead.Add(decimal.NewFromInt(c.CompletedAt.Sub(*c.PlannedAt).Milliseconds()))
		}
		if c.Due != nil {
			out.DueCycles++
			if c.CompletedAt.After(*c.Due) {
				out.LateCycles++
			}
		}
	}
	if out.TimedCycles > 0 {
		avg := lead.Div(decimal.NewFromInt(int64(out.TimedCycles) * 3_600_000)).Round(2)
		out.AvgLeadTimeH = &avg
	}
	if out.DueCycles > 0 {
		pct := decimal.NewFromInt(int64(out.LateCycles) * 100).Div(decimal.NewFromInt(int64(out.DueCycles))).Round(2)
		out.LatePct = &pct
	}
	return out
}
