package domain

import (
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/shopspring/decimal"
)

const backorderDayLayout = "2006-01-02"

// Backorder processing strategies (FR-27.17).
const (
	StrategyWin          = "WIN"
	StrategyGain         = "GAIN"
	StrategyRedistribute = "REDISTRIBUTE"
	StrategyFill         = "FILL"
	StrategyLose         = "LOSE"
)

// BackorderLine is one open order line competing for the same supply.
type BackorderLine struct {
	ID            string
	Priority      int             // priority class, 1 is highest
	RequestedDate string          // yyyy-mm-dd
	OrderValue    decimal.Decimal // used as the last tie-breaker, larger first
	Demand        decimal.Decimal // open quantity to confirm
	Confirmed     decimal.Decimal // quantity currently confirmed, 0 <= x <= Demand
}

// PlannedLine is the preview result for one line.
type PlannedLine struct {
	ID          string
	Before      decimal.Decimal
	After       decimal.Decimal
	Changed     bool
	Deconfirmed bool
}

// BackorderPlan is the preview of one run. Lines are in processing order.
type BackorderPlan struct {
	Strategy         string
	Lines            []PlannedLine
	Allocated        decimal.Decimal
	DeconfirmedCount int
}

// PlanBackorder computes the allocation of available supply across lines under
// strategy. It is a pure preview; nothing is committed (BR-27.11).
//
//   - WIN never reduces a confirmed quantity: every confirmed quantity is kept,
//     then open demand is served in the base order.
//   - GAIN also keeps every confirmed quantity and serves the rest smallest open
//     demand first, so the most lines improve to a full confirmation.
//   - LOSE keeps the confirmed quantity of lines with a priority class below
//     loseThreshold; lines at or above the threshold may be deconfirmed.
//   - FILL serves by requested date and value, ignoring priority (first come).
//   - REDISTRIBUTE serves by priority, then requested date, then value, and may
//     move confirmed quantity from a lower to a higher priority line.
//
// The base order is (priority class asc, requested date asc, order value desc).
// A run that cannot keep a protected confirmation because supply is short is
// refused rather than returned partially.
func PlanBackorder(strategy string, available decimal.Decimal, loseThreshold int, lines []BackorderLine) (BackorderPlan, error) {
	switch strategy {
	case StrategyWin, StrategyGain, StrategyRedistribute, StrategyFill, StrategyLose:
	default:
		return BackorderPlan{}, fmt.Errorf("unknown strategy %q", strategy)
	}
	if available.IsNegative() {
		return BackorderPlan{}, errors.New("available supply must not be negative")
	}
	if loseThreshold < 1 || loseThreshold > 9 {
		return BackorderPlan{}, errors.New("lose threshold must be 1 to 9")
	}
	seen := map[string]bool{}
	for _, l := range lines {
		if err := validateBackorderLine(l); err != nil {
			return BackorderPlan{}, fmt.Errorf("line %s: %w", l.ID, err)
		}
		if seen[l.ID] {
			return BackorderPlan{}, fmt.Errorf("line %s appears twice", l.ID)
		}
		seen[l.ID] = true
	}

	order := append([]BackorderLine(nil), lines...)
	sort.SliceStable(order, func(i, j int) bool { return lessForStrategy(strategy, order[i], order[j]) })

	after := make(map[string]decimal.Decimal, len(order))
	remaining := available
	give := func(l BackorderLine, need decimal.Decimal) {
		if !need.IsPositive() || !remaining.IsPositive() {
			return
		}
		q := decimal.Min(need, remaining)
		after[l.ID] = after[l.ID].Add(q)
		remaining = remaining.Sub(q)
	}
	for _, l := range order {
		if protects(strategy, loseThreshold, l) {
			give(l, l.Confirmed)
		}
	}
	for _, l := range order {
		if protects(strategy, loseThreshold, l) && after[l.ID].LessThan(l.Confirmed) {
			return BackorderPlan{}, fmt.Errorf("%s cannot keep the %s confirmed on line %s; supply is short", strategy, l.Confirmed, l.ID)
		}
	}
	for _, l := range order {
		give(l, l.Demand.Sub(after[l.ID]))
	}

	plan := BackorderPlan{Strategy: strategy, Allocated: decimal.Zero, Lines: make([]PlannedLine, 0, len(order))}
	for _, l := range order {
		a := after[l.ID]
		pl := PlannedLine{ID: l.ID, Before: l.Confirmed, After: a, Changed: !a.Equal(l.Confirmed), Deconfirmed: a.LessThan(l.Confirmed)}
		if pl.Deconfirmed {
			plan.DeconfirmedCount++
		}
		plan.Allocated = plan.Allocated.Add(a)
		plan.Lines = append(plan.Lines, pl)
	}
	return plan, nil
}

// protects reports whether strategy must keep l's confirmed quantity.
func protects(strategy string, loseThreshold int, l BackorderLine) bool {
	switch strategy {
	case StrategyWin, StrategyGain:
		return true
	case StrategyLose:
		return l.Priority < loseThreshold
	}
	return false
}

func validateBackorderLine(l BackorderLine) error {
	switch {
	case l.ID == "":
		return errors.New("id is required")
	case l.Priority < 1 || l.Priority > 9:
		return errors.New("priority class must be 1 to 9")
	case l.OrderValue.IsNegative():
		return errors.New("order value must not be negative")
	case !l.Demand.IsPositive():
		return errors.New("demand must be positive")
	case l.Confirmed.IsNegative() || l.Confirmed.GreaterThan(l.Demand):
		return errors.New("confirmed quantity must be between 0 and demand")
	}
	if _, err := time.Parse(backorderDayLayout, l.RequestedDate); err != nil {
		return errors.New("requested date must be yyyy-mm-dd")
	}
	return nil
}

// lessForStrategy orders lines for processing. The base order is priority class
// ascending, requested date ascending, order value descending, then id.
func lessForStrategy(strategy string, a, b BackorderLine) bool {
	switch strategy {
	case StrategyFill:
		if a.RequestedDate != b.RequestedDate {
			return a.RequestedDate < b.RequestedDate
		}
		if !a.OrderValue.Equal(b.OrderValue) {
			return a.OrderValue.GreaterThan(b.OrderValue)
		}
		return a.ID < b.ID
	case StrategyGain:
		if openA, openB := a.Demand.Sub(a.Confirmed), b.Demand.Sub(b.Confirmed); !openA.Equal(openB) {
			return openA.LessThan(openB)
		}
	}
	return baseLess(a, b)
}

func baseLess(a, b BackorderLine) bool {
	if a.Priority != b.Priority {
		return a.Priority < b.Priority
	}
	if a.RequestedDate != b.RequestedDate {
		return a.RequestedDate < b.RequestedDate
	}
	if !a.OrderValue.Equal(b.OrderValue) {
		return a.OrderValue.GreaterThan(b.OrderValue)
	}
	return a.ID < b.ID
}
