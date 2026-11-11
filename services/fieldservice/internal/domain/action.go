package domain

import (
	"fmt"
	"sort"
	"time"

	"github.com/shopspring/decimal"
)

// ActionKind is one append-only action a technician's device records (BR-45.6).
type ActionKind string

const (
	ActTravelStart ActionKind = "TRAVEL_START"
	ActWorkStart   ActionKind = "WORK_START"
	ActHold        ActionKind = "HOLD"
	ActResume      ActionKind = "RESUME"
	ActPartUsed    ActionKind = "PART_USED"
	ActSignature   ActionKind = "SIGNATURE"
	ActNote        ActionKind = "NOTE"
	ActComplete    ActionKind = "COMPLETE"
	// ActPhoto attaches up to MaxPhotosPerAction captured photos (FR-45.8); ActChecklist
	// ticks or clears one checklist item of the work order.
	ActPhoto     ActionKind = "PHOTO"
	ActChecklist ActionKind = "CHECKLIST"
)

// ActionKinds lists every kind the action log accepts.
var ActionKinds = []ActionKind{ActTravelStart, ActWorkStart, ActHold, ActResume, ActPartUsed, ActSignature, ActNote, ActComplete, ActPhoto, ActChecklist}

// Outcomes of an action once the server has merged it.
const (
	OutcomeApplied    = "APPLIED"
	OutcomeSuperseded = "SUPERSEDED"
	OutcomeRejected   = "REJECTED"
)

// Codes of the action log; SVC-WO-001 and SVC-WO-002 are the PRD 8.45.5 validations.
const (
	CodeSignatureRequired = "SVC-WO-001"
	CodePartsNotConsumed  = "SVC-WO-002"
	CodeActionStatus      = "SVC-SYNC-002"
	CodeActionTechnician  = "SVC-SYNC-003"
	CodeActionPart        = "SVC-SYNC-004"
	CodeActionReuse       = "SVC-SYNC-005"
	CodeActionClock       = "SVC-SYNC-006"
	// CodeChecklistOpen refuses a completion while a required checklist item is open.
	CodeChecklistOpen = "SVC-WO-003"
	// CodeChecklistItem names an item the work order's checklist does not hold.
	CodeChecklistItem = "SVC-SYNC-007"
)

// statusTarget is the status a status action moves the work order to.
var statusTarget = map[ActionKind]WOStatus{
	ActTravelStart: WOTraveling,
	ActWorkStart:   WOInProgress,
	ActHold:        WOOnHold,
	ActResume:      WOInProgress,
	ActComplete:    WOCompleted,
}

// IsStatusAction reports whether the kind moves the work order between statuses.
func (k ActionKind) IsStatusAction() bool { _, ok := statusTarget[k]; return ok }

// Valid reports whether k is a known kind.
func (k ActionKind) Valid() bool {
	for _, c := range ActionKinds {
		if c == k {
			return true
		}
	}
	return false
}

// rank orders the technician-driven statuses: the furthest state wins (BR-45.6).
var rank = map[WOStatus]int{WODispatched: 0, WOTraveling: 1, WOInProgress: 2, WOOnHold: 2, WOCompleted: 3}

// Merge decides what a status action does to a work order currently in status from.
// A device that was offline may skip intermediate states, so a forward move is valid
// when the target is further than the current state; a target at or behind the current
// state is superseded (a conflict resolved to the furthest state); a move that cannot
// follow the current state at all is rejected.
func Merge(from WOStatus, kind ActionKind) (outcome, code, reason string) {
	to, ok := statusTarget[kind]
	if !ok {
		return OutcomeRejected, CodeActionStatus, "not a status action"
	}
	cur, started := rank[from]
	if !started {
		return OutcomeRejected, CodeActionStatus, fmt.Sprintf("a %s work order cannot be worked on from the device", from)
	}
	switch {
	case kind == ActHold && from != WOInProgress:
		if from == WOOnHold || from == WOCompleted {
			return OutcomeSuperseded, "", "the work order is already " + string(from)
		}
		return OutcomeRejected, CodeActionStatus, "only work in progress can be put on hold"
	case kind == ActResume && from != WOOnHold:
		if from == WOInProgress || from == WOCompleted {
			return OutcomeSuperseded, "", "the work order is already " + string(from)
		}
		return OutcomeRejected, CodeActionStatus, "only a work order on hold can be resumed"
	case from == WOOnHold && kind != ActResume:
		if kind == ActComplete {
			return OutcomeRejected, CodeActionStatus, "resume the work order before completing it"
		}
		return OutcomeSuperseded, "", "the work order is already further along"
	case rank[to] < cur || (rank[to] == cur && to == from):
		return OutcomeSuperseded, "", "the work order is already " + string(from)
	}
	return OutcomeApplied, "", ""
}

// Mark is a work-time boundary taken from an applied status action.
type Mark struct {
	Kind ActionKind
	At   time.Time
}

// WorkMinutes sums the minutes spent IN_PROGRESS: from each WORK_START or RESUME to the
// next HOLD or COMPLETE, by device time. An open span at the end counts nothing.
func WorkMinutes(marks []Mark) int {
	sorted := append([]Mark(nil), marks...)
	sort.SliceStable(sorted, func(i, j int) bool { return sorted[i].At.Before(sorted[j].At) })
	var open *time.Time
	var total time.Duration
	for _, m := range sorted {
		switch m.Kind {
		case ActWorkStart, ActResume:
			if open == nil {
				at := m.At
				open = &at
			}
		case ActHold, ActComplete:
			if open != nil {
				total += m.At.Sub(*open)
				open = nil
			}
		}
	}
	return int(total / time.Minute)
}

// PartsOutstanding returns the ids of PART lines whose consumed quantity is below the
// line quantity (SVC-WO-002).
func PartsOutstanding(lines []WorkLine, consumed map[string]decimal.Decimal) []string {
	var out []string
	for _, l := range lines {
		if l.Kind == KindPart && consumed[l.ID].LessThan(l.Qty) {
			out = append(out, l.ID)
		}
	}
	return out
}
