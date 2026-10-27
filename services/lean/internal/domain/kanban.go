package domain

import "github.com/shopspring/decimal"

// State is a kanban card state (BR-24.6).
type State string

const (
	NotPlanned State = "not-planned"
	Planned    State = "planned"
	Prepared   State = "prepared"
	InProgress State = "in-progress"
	Completed  State = "completed"
	Emptied    State = "emptied"
)

// states lists the states in their forward order.
func states() []State {
	return []State{NotPlanned, Planned, Prepared, InProgress, Completed, Emptied}
}

// ValidState reports whether s is one of the six card states.
func ValidState(s State) bool {
	for _, x := range states() {
		if x == s {
			return true
		}
	}
	return false
}

func (s State) index() int {
	for i, x := range states() {
		if x == s {
			return i
		}
	}
	return -1
}

// CheckMove validates one state change (BR-24.6). Moves are one step forward
// through the sequence. The only backward moves are Planned → Not planned (the
// date is cleared) and Emptied → Not planned (card reuse). Anything else is
// LEAN-0001.
func CheckMove(from, to State) error {
	if !ValidState(from) || !ValidState(to) {
		return errorf(CodeInvalid, "unknown kanban state")
	}
	if allowed(from, to) {
		return nil
	}
	return errorf(CodeMove, "kanban move %s → %s is not allowed", from, to)
}

func allowed(from, to State) bool {
	if to == NotPlanned && (from == Planned || from == Emptied) {
		return true
	}
	return to.index() == from.index()+1
}

// Replenishment is the FR-24.9 replenishment strategy of a kanban rule.
type Replenishment string

const (
	ReplenishFixed     Replenishment = "fixed"
	ReplenishScheduled Replenishment = "scheduled"
	ReplenishEvent     Replenishment = "event"
)

// CheckRuleMove applies BR-24.6 for a card of a rule with the given
// replenishment. On top of CheckMove, an emptied card of an event rule is
// spent: it never returns to Not planned (fixed-rule cards do, for reuse).
func CheckRuleMove(r Replenishment, from, to State) error {
	if err := CheckMove(from, to); err != nil {
		return err
	}
	if from == Emptied && to == NotPlanned && r != ReplenishFixed {
		return errorf(CodeMove, "kanban move %s → %s is not allowed: cards of %s rules are not reused", from, to, r)
	}
	return nil
}

// Active reports whether a card counts against a fixed rule's number of
// kanbans (BR-24.7: every card that is not Emptied).
func (s State) Active() bool { return s != Emptied }

// Kanban is a card that moves through the states. Quantities are in the item's
// inventory unit.
type Kanban struct {
	ID     string
	RuleID string
	Qty    decimal.Decimal
	State  State
}

// Move changes the card state if CheckMove allows it; otherwise the card is unchanged.
func (k *Kanban) Move(to State) error {
	if err := CheckMove(k.State, to); err != nil {
		return err
	}
	k.State = to
	return nil
}

// GenerateEventKanbans creates the cards for an event rule (FR-24.11, BR-24.8).
// Each card starts in Planned. newID supplies card identifiers.
func GenerateEventKanbans(ruleID string, demand, fixedQty decimal.Decimal, exact bool, newID func() string) ([]Kanban, error) {
	qtys, err := EventKanbanQuantities(demand, fixedQty, exact)
	if err != nil {
		return nil, err
	}
	out := make([]Kanban, len(qtys))
	for i, q := range qtys {
		out[i] = Kanban{ID: newID(), RuleID: ruleID, Qty: q, State: Planned}
	}
	return out, nil
}
