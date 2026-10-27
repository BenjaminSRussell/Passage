package domain

import "github.com/shopspring/decimal"

// FixedKanbanCount is the BR-24.7 suggestion for a fixed rule:
// ⌈daily demand × lead time (days) × (1 + safety%) / fixed qty⌉.
// The suggestion is never applied automatically.
func FixedKanbanCount(dailyDemand, leadTimeDays, safetyPct, fixedQty decimal.Decimal) (int64, error) {
	if !fixedQty.IsPositive() {
		return 0, errorf(CodeInvalid, "fixed quantity must be positive")
	}
	if dailyDemand.IsNegative() || leadTimeDays.IsNegative() || safetyPct.IsNegative() {
		return 0, errorf(CodeInvalid, "demand, lead time and safety must not be negative")
	}
	factor := decimal.NewFromInt(1).Add(safetyPct.DivRound(decimal.NewFromInt(100), 24))
	need := dailyDemand.Mul(leadTimeDays).Mul(factor).DivRound(fixedQty, 24).Ceil()
	return need.IntPart(), nil
}

// EventKanbanQuantities implements BR-24.8: demand D creates ⌈D / fixedQty⌉
// cards. Each card holds fixedQty, so the last card is full size (overproduction
// allowed) unless exact is set, in which case the last card holds the remainder.
func EventKanbanQuantities(demand, fixedQty decimal.Decimal, exact bool) ([]decimal.Decimal, error) {
	if !demand.IsPositive() || !fixedQty.IsPositive() {
		return nil, errorf(CodeInvalid, "demand and fixed quantity must be positive")
	}
	n := demand.DivRound(fixedQty, 24).Ceil().IntPart()
	if n > MaxKanbansPerGeneration {
		return nil, errorf(CodeInvalid, "generation would create %d kanbans (max %d)", n, MaxKanbansPerGeneration)
	}
	out := make([]decimal.Decimal, n)
	for i := range out {
		out[i] = fixedQty
	}
	if exact {
		out[n-1] = demand.Sub(fixedQty.Mul(decimal.NewFromInt(n - 1)))
	}
	return out, nil
}

// CheckCapacity enforces BR-24.7 for a fixed rule: active non-emptied cards plus
// the new ones may not exceed the rule's number of kanbans.
func CheckCapacity(rule string, active, adding, max int) error {
	if active+adding > max {
		return errorf(CodeCapacity, "rule %s already has %d active kanbans (max %d)", rule, active, max)
	}
	return nil
}

// RuleType is the FR-24.9 kanban rule type.
type RuleType string

const (
	RuleManufacturing RuleType = "manufacturing"
	RuleWithdrawal    RuleType = "withdrawal"
)

// MaxKanbansPerRule bounds a fixed rule's number of kanbans.
const MaxKanbansPerRule = 1000

// Rule is the part of a KanbanRule (FR-24.9) the rules here need.
type Rule struct {
	Type            RuleType
	Replenishment   Replenishment
	FixedQty        decimal.Decimal
	NumberOfKanbans int
	EventSource     string
}

// ValidateRule checks a kanban rule (FR-24.9): a known type and replenishment,
// a positive fixed quantity, a fixed rule with 1 to MaxKanbansPerRule kanbans,
// and an event rule with an event source (sales, bom, kanban or min-stock).
func ValidateRule(r Rule) error {
	if r.Type != RuleManufacturing && r.Type != RuleWithdrawal {
		return errorf(CodeInvalid, "rule type must be manufacturing or withdrawal")
	}
	if !r.FixedQty.IsPositive() {
		return errorf(CodeInvalid, "fixed quantity must be positive")
	}
	switch r.Replenishment {
	case ReplenishFixed:
		if r.NumberOfKanbans < 1 || r.NumberOfKanbans > MaxKanbansPerRule {
			return errorf(CodeInvalid, "a fixed rule needs 1 to %d kanbans", MaxKanbansPerRule)
		}
	case ReplenishEvent:
		switch r.EventSource {
		case "sales", "bom", "kanban", "min-stock":
		default:
			return errorf(CodeInvalid, "an event rule needs an event source: sales, bom, kanban or min-stock")
		}
	case ReplenishScheduled:
	default:
		return errorf(CodeInvalid, "replenishment must be fixed, scheduled or event")
	}
	return nil
}

// FixedCards returns the cards a fixed rule may still create (BR-24.7): count
// cards of fixedQty each, Not planned, provided active + count stays within the
// rule's number of kanbans (LEAN-0002 otherwise).
func FixedCards(ruleID string, r Rule, active, count int, newID func() string) ([]Kanban, error) {
	if count < 1 {
		return nil, errorf(CodeInvalid, "count must be at least 1")
	}
	if err := CheckCapacity(ruleID, active, count, r.NumberOfKanbans); err != nil {
		return nil, err
	}
	out := make([]Kanban, count)
	for i := range out {
		out[i] = Kanban{ID: newID(), RuleID: ruleID, Qty: r.FixedQty, State: NotPlanned}
	}
	return out, nil
}
