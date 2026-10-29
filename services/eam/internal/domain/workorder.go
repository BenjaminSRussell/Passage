package domain

import (
	"errors"
	"time"

	"github.com/shopspring/decimal"
)

// Status is a work order lifecycle state (BR-44.3).
type Status string

const (
	StatusCreated    Status = "CREATED"
	StatusScheduled  Status = "SCHEDULED"
	StatusReleased   Status = "RELEASED"
	StatusInProgress Status = "IN_PROGRESS"
	StatusOnHold     Status = "ON_HOLD"
	StatusCompleted  Status = "COMPLETED"
	StatusClosed     Status = "CLOSED"
	StatusCanceled   Status = "CANCELED"
)

// Open reports whether a work order still needs work (not completed, closed or canceled).
func (s Status) Open() bool {
	return s != StatusCompleted && s != StatusClosed && s != StatusCanceled
}

var transitions = map[Status][]Status{
	StatusCreated:    {StatusScheduled, StatusCanceled},
	StatusScheduled:  {StatusReleased, StatusCanceled},
	StatusReleased:   {StatusInProgress, StatusCanceled},
	StatusInProgress: {StatusOnHold, StatusCompleted, StatusCanceled},
	StatusOnHold:     {StatusInProgress, StatusCanceled},
	StatusCompleted:  {StatusClosed},
}

// CanTransition reports whether from → to is allowed. Emergency work orders
// (priority 1) may skip SCHEDULED and start before release (BR-44.7).
func CanTransition(from, to Status, emergency bool) bool {
	if emergency && (from == StatusCreated && (to == StatusReleased || to == StatusInProgress) ||
		from == StatusScheduled && to == StatusInProgress) {
		return true
	}
	for _, next := range transitions[from] {
		if next == to {
			return true
		}
	}
	return false
}

// ErrCompletionTimes is returned when a completion lacks ordered actual times.
var ErrCompletionTimes = errors.New("eam: a completed work order needs actualStart <= actualEnd")

// ValidCompletion enforces the completed-work-order invariant.
func ValidCompletion(start, end *time.Time) error {
	if start == nil || end == nil || end.Before(*start) {
		return ErrCompletionTimes
	}
	return nil
}

// Work order types (FR-44.7). Types enabled by default (8.44.13 parameter
// "Work order types enabled") are PREVENTIVE, CORRECTIVE, INSPECTION and EMERGENCY.
const (
	TypePreventive = "PREVENTIVE"
	TypeCorrective = "CORRECTIVE"
	TypeInspection = "INSPECTION"
	TypeEmergency  = "EMERGENCY"
	TypeCapital    = "CAPITAL"
	// TypeRound orders come only from maintenance rounds (FR-44.14): one per
	// occurrence, with one job per stop.
	TypeRound = "ROUND"
)

// DefaultTypePriority is the priority a new work order of typ gets when the
// caller gives none: 1 for EMERGENCY (BR-44.7), 6 − criticality for
// CORRECTIVE (BR-44.15) and 3 otherwise.
func DefaultTypePriority(typ string, criticality int) int {
	if typ == TypeEmergency {
		return 1
	}
	return DefaultPriority(criticality, typ == TypeCorrective)
}

// DefaultPriority is BR-44.15: 6 − criticality for corrective work, 3 otherwise.
func DefaultPriority(criticality int, corrective bool) int {
	if !corrective {
		return 3
	}
	p := 6 - criticality
	if p < 1 {
		return 1
	}
	if p > 5 {
		return 5
	}
	return p
}

// Money is rounded to the two-decimal minor unit used by the work order currency.
func Money(d decimal.Decimal) decimal.Decimal { return d.Round(2) }

// LaborCost is hours × trade rate (FR-44.10), rounded to the minor unit.
func LaborCost(hours, rate decimal.Decimal) decimal.Decimal { return Money(hours.Mul(rate)) }

// PartCost is quantity × unit cost at issue time (BR-44.6).
func PartCost(qty, unit decimal.Decimal) decimal.Decimal { return Money(qty.Mul(unit)) }

// Totals sums parts and labor for a work order (FR-44.11 accumulation).
type Totals struct {
	Parts decimal.Decimal
	Labor decimal.Decimal
}

// Total is the parts plus labor cost.
func (t Totals) Total() decimal.Decimal { return t.Parts.Add(t.Labor) }
