package domain

import (
	"fmt"

	"github.com/shopspring/decimal"
)

// Investment program rules (FR-38.21, BR-38.15). Pure functions: the store locks
// the program row, so every distribution of one program is serialized, then
// applies the result.

// MaxProgramLevels bounds the position hierarchy (the Capex program type allows 6 levels).
const MaxProgramLevels = 6

// Budget categories a program position may carry (7.38.13 `program.types`).
const (
	CategoryCapex       = "Capex"
	CategoryOpexRelated = "Opex-related"
	CategoryContingency = "Contingency"
)

// Objects that can be assigned to a program position (FR-38.21).
const (
	ObjectCapexRequest           = "CapexRequest"
	ObjectAssetUnderConstruction = "AssetUnderConstruction"
	ObjectProject                = "Project"
)

// Error codes raised by investment program rules.
const (
	CodeDistributionExceeds  = "CO-0641"  // children would exceed the parent budget (BR-38.15)
	CodeLevelExceeded        = "BUD-0020" // a new position would sit below level 6
	CodeCarryForwardRepeated = "BUD-0021" // every position was already carried forward
	CodeBelowDistributed     = "BUD-0023" // a position cannot drop below what its own children hold
	CodePositionNotFound     = "BUD-0024"
)

// ValidCategory reports whether c is a budget category of the program type.
func ValidCategory(c string) bool {
	switch c {
	case CategoryCapex, CategoryOpexRelated, CategoryContingency:
		return true
	}
	return false
}

// ValidObjectType reports whether t is an object type that can be assigned to a position.
func ValidObjectType(t string) bool {
	switch t {
	case ObjectCapexRequest, ObjectAssetUnderConstruction, ObjectProject:
		return true
	}
	return false
}

// ValidateLevel accepts levels 1 to MaxProgramLevels.
func ValidateLevel(level int) error {
	if level < 1 || level > MaxProgramLevels {
		return &Error{Code: CodeLevelExceeded, Detail: fmt.Sprintf("program positions run from level 1 to %d", MaxProgramLevels)}
	}
	return nil
}

// Child is a position under one parent (or under the program root). Distributed is
// the sum of the position's own children's budgets.
type Child struct {
	ID          string
	Budget      decimal.Decimal
	Distributed decimal.Decimal
}

// Distribute sets the budget of child targetID to newBudget under a parent holding
// parentBudget. It returns a new slice; the input is not modified. Rules: the
// position is under this parent (BUD-0024); its budget is not below what it already
// distributes to its own children (BUD-0023, BR-38.15 "reducing a parent below Σ
// children is rejected"); and the children's total stays within the parent budget
// (BR-38.15, CO-0641 with the excess). A negative budget is a validation error the
// API rejects before this point.
func Distribute(parentBudget decimal.Decimal, children []Child, targetID string, newBudget decimal.Decimal) ([]Child, error) {
	out := make([]Child, len(children))
	copy(out, children)
	idx := -1
	for i := range out {
		if out[i].ID == targetID {
			idx = i
			break
		}
	}
	if idx < 0 {
		return nil, &Error{Code: CodePositionNotFound, Detail: "position not found under this parent"}
	}
	if newBudget.LessThan(out[idx].Distributed) {
		return nil, &Error{Code: CodeBelowDistributed, Detail: fmt.Sprintf("budget %s is below the %s the position already distributes to its children",
			newBudget.StringFixed(2), out[idx].Distributed.StringFixed(2))}
	}
	out[idx].Budget = newBudget
	if total := Total(out); total.GreaterThan(parentBudget) {
		return nil, &Error{Code: CodeDistributionExceeds, Detail: fmt.Sprintf("distribution exceeds parent position budget by %s", total.Sub(parentBudget).StringFixed(2))}
	}
	return out, nil
}

// Total is the sum of the children's budgets.
func Total(children []Child) decimal.Decimal {
	sum := decimal.Zero
	for _, c := range children {
		sum = sum.Add(c.Budget)
	}
	return sum
}

// Unused is the amount a position carries forward: its own budget slice (budget −
// what it distributed to children) less the actuals and commitments of the objects
// assigned to it directly, never below zero. Summing Unused over every position
// counts each unit of program budget once, so a parent and its children are never
// both carried for the same money (FR-38.21 "budget − actual − commitments per
// position").
func Unused(budget, distributed, actual, commitment decimal.Decimal) decimal.Decimal {
	u := budget.Sub(distributed).Sub(actual).Sub(commitment)
	if u.Sign() < 0 {
		return decimal.Zero
	}
	return u
}

// Amounts are the plan, actual and commitment values of a position or subtree.
type Amounts struct {
	Plan       decimal.Decimal
	Actual     decimal.Decimal
	Commitment decimal.Decimal
}

// Add returns a + b.
func (a Amounts) Add(b Amounts) Amounts {
	return Amounts{Plan: a.Plan.Add(b.Plan), Actual: a.Actual.Add(b.Actual), Commitment: a.Commitment.Add(b.Commitment)}
}

// Node is one position in the rollup: its parent ("" at the root) and the amounts
// of the objects assigned to it directly.
type Node struct {
	ID       string
	ParentID string
	Own      Amounts
}

// Rollup returns, per position, its own amounts plus those of every descendant
// (FR-38.21 "roll up plan / budget / actual"). Positions whose parent is not in
// nodes are treated as roots. The hierarchy is at most MaxProgramLevels deep.
func Rollup(nodes []Node) map[string]Amounts {
	parent := make(map[string]string, len(nodes))
	out := make(map[string]Amounts, len(nodes))
	for _, n := range nodes {
		parent[n.ID] = n.ParentID
		if _, ok := out[n.ID]; !ok {
			out[n.ID] = Amounts{Plan: decimal.Zero, Actual: decimal.Zero, Commitment: decimal.Zero}
		}
	}
	for _, n := range nodes {
		// Walk up from the node; each ancestor (and the node itself) receives its amounts.
		for id, depth := n.ID, 0; id != "" && depth <= len(nodes); depth++ {
			if _, ok := out[id]; !ok {
				break
			}
			out[id] = out[id].Add(n.Own)
			id = parent[id]
		}
	}
	return out
}
