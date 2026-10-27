package domain

import "github.com/shopspring/decimal"

// Kanban jobs (FR-24.10): a card has one job per activity of its rule's
// production flow. The card's In progress and Completed states come from its
// jobs (BR-24.6): the first started job moves the card to In progress, the last
// completed job completes it.

// ValidJobState reports whether s is a job state: every card state but Emptied
// (PRD 4.24.4 KanbanJob.state).
func ValidJobState(s State) bool { return ValidState(s) && s != Emptied }

// JobDriven reports whether a move of a card to `to` belongs to its jobs: a
// card with jobs reaches In progress and Completed only through them.
func JobDriven(to State) bool { return to == InProgress || to == Completed }

// StartJob checks that a job can start and returns the states the card passes
// through, in order (empty when the card is already In progress). A job starts
// from Planned or Prepared. The first job of a card needs the card Planned or
// Prepared; a card still Planned passes through Prepared, which needs picking to
// be complete, so an activity that requires picking waits for a Prepared card.
func StartJob(job, card State, pickingRequired bool) ([]State, error) {
	if job != Planned && job != Prepared {
		return nil, errorf(CodeMove, "kanban job move %s → %s is not allowed", job, InProgress)
	}
	switch card {
	case InProgress:
		return []State{}, nil
	case Prepared:
		return []State{InProgress}, nil
	case Planned:
		if pickingRequired {
			return nil, errorf(CodeMove, "the activity needs picking: move the kanban to %s before starting the job", Prepared)
		}
		return []State{Prepared, InProgress}, nil
	}
	return nil, errorf(CodeMove, "a job cannot start while its kanban is %s", card)
}

// Completion is what an operator reports when a job finishes: the good
// quantity, the scrap and the handling units, all in the item's inventory unit.
type Completion struct {
	ReportedQty   decimal.Decimal
	ScrapQty      decimal.Decimal
	HandlingUnits int
}

// CompleteJob checks that an in-progress job can complete with c and returns
// the states the card passes through: Completed when every other job of the
// card (others) is completed, else none. A job that is not In progress is
// refused with LEAN-0001 (AC-24.7: Planned → Completed is refused).
func CompleteJob(job, card State, others []State, c Completion) ([]State, error) {
	if job != InProgress {
		return nil, errorf(CodeMove, "kanban job move %s → %s is not allowed", job, Completed)
	}
	if card != InProgress {
		return nil, errorf(CodeMove, "a job cannot complete while its kanban is %s", card)
	}
	if c.ReportedQty.IsNegative() || c.ScrapQty.IsNegative() {
		return nil, errorf(CodeInvalid, "reported and scrap quantities must not be negative")
	}
	if !c.ReportedQty.Add(c.ScrapQty).IsPositive() {
		return nil, errorf(CodeInvalid, "report a good or scrap quantity")
	}
	if c.HandlingUnits < 1 {
		return nil, errorf(CodeInvalid, "handling units must be at least 1")
	}
	for _, s := range others {
		if s != Completed {
			return []State{}, nil
		}
	}
	return []State{Completed}, nil
}
