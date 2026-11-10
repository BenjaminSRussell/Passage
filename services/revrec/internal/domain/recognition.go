package domain

import (
	"time"

	"github.com/shopspring/decimal"
)

// InitialStatus is the status a schedule line takes when its contract is
// created: a POINT_IN_TIME line dated at creation is recognized at once (BR-29.1,
// the invoice carries that revenue). Every other line waits for a recognition
// run or a milestone satisfaction.
func InitialStatus(method string, date *time.Time) string {
	if method == MethodPointInTime && date != nil {
		return ScheduleRecognized
	}
	return ScheduleScheduled
}

// DeferredMethods are the recognition methods whose revenue sits in deferred
// revenue (2400) until recognized: RATABLE and MILESTONE. POINT_IN_TIME revenue
// is credited to 4000 by the invoice itself and never enters the roll-forward
// (FR-29.9); USAGE recognizes as usage posts (later slice).
func DeferredMethods() []string { return []string{MethodRatable, MethodMilestone} }

// FullyRecognized reports whether a contract has nothing left to recognize, so
// its status becomes COMPLETE: no USAGE obligation (usage keeps arriving) and no
// schedule line still SCHEDULED. methods are the obligations' methods and
// lineStatuses the statuses of all their schedule lines.
func FullyRecognized(methods, lineStatuses []string) bool {
	for _, m := range methods {
		if m == MethodUsage {
			return false
		}
	}
	for _, s := range lineStatuses {
		if s == ScheduleScheduled {
			return false
		}
	}
	return true
}

// RollForward is the deferred revenue movement of one currency over one period
// (FR-29.9). Closing = Opening + Additions − Recognized + Reversed + Adjustments.
// Adjustments are the credit notes' reductions of deferred revenue (negative: the
// deferred part billing debits to 2400 for the credited lines, BR-29.6) and
// Reversed the recognized revenue those credit notes reversed back through 2400.
type RollForward struct {
	Opening     decimal.Decimal
	Additions   decimal.Decimal
	Recognized  decimal.Decimal
	Reversed    decimal.Decimal
	Adjustments decimal.Decimal
	Closing     decimal.Decimal
}

// NewRollForward closes a period without credit notes.
func NewRollForward(opening, additions, recognized decimal.Decimal) RollForward {
	return RollForward{
		Opening:     opening,
		Additions:   additions,
		Recognized:  recognized,
		Reversed:    decimal.Zero,
		Adjustments: decimal.Zero,
		Closing:     opening.Add(additions).Sub(recognized),
	}
}

// WithCredits adds the period's credit-note movements: reversed (recognized revenue
// reversed, positive) and adjustments (deferred revenue reduced, negative).
func (r RollForward) WithCredits(reversed, adjustments decimal.Decimal) RollForward {
	r.Reversed, r.Adjustments = reversed, adjustments
	r.Closing = r.Opening.Add(r.Additions).Sub(r.Recognized).Add(reversed).Add(adjustments)
	return r
}
