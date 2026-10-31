package domain

import (
	"errors"
	"fmt"
	"time"

	"github.com/shopspring/decimal"
)

// QuotaPeriod is one allocation quota row as the planner sees it: the days it
// covers (inclusive), its quantity and what is already consumed (FR-27.16).
type QuotaPeriod struct {
	ID       string
	Start    time.Time
	End      time.Time
	Quantity decimal.Decimal
	Consumed decimal.Decimal
}

// Remaining is the quantity still available in the period.
func (q QuotaPeriod) Remaining() decimal.Decimal { return q.Quantity.Sub(q.Consumed) }

// Allocation outcomes for one line.
const (
	AllocationUnconstrained = "UNCONSTRAINED" // no quota covers the requested date
	AllocationRequested     = "REQUESTED"     // consumed in the requested period
	AllocationNextPeriod    = "NEXT_PERIOD"   // requested period exhausted; confirmed in a later one
	AllocationExhausted     = "EXHAUSTED"     // no period has enough left
)

// AllocationPlan is the result of PlanAllocation. QuotaID and ConfirmDate are
// set for REQUESTED and NEXT_PERIOD.
type AllocationPlan struct {
	Outcome     string
	QuotaID     string
	ConfirmDate time.Time
}

// PlanAllocation decides where one line of qty requested on requested draws its
// quota (BR-27.15). periods must be sorted by Start. The quota of the period
// containing the requested date is used when it has qty left; otherwise the
// first later period with qty left, and the line is confirmed on that period's
// first day (source ALLOCATION). A requested date no quota covers is not
// constrained. When no period has enough left the line is EXHAUSTED.
func PlanAllocation(periods []QuotaPeriod, requested time.Time, qty decimal.Decimal) AllocationPlan {
	cur := -1
	for i, p := range periods {
		if !requested.Before(p.Start) && !requested.After(p.End) {
			cur = i
			break
		}
	}
	if cur < 0 {
		return AllocationPlan{Outcome: AllocationUnconstrained}
	}
	if periods[cur].Remaining().GreaterThanOrEqual(qty) {
		return AllocationPlan{Outcome: AllocationRequested, QuotaID: periods[cur].ID, ConfirmDate: requested}
	}
	for _, p := range periods[cur+1:] {
		if p.Remaining().GreaterThanOrEqual(qty) {
			return AllocationPlan{Outcome: AllocationNextPeriod, QuotaID: p.ID, ConfirmDate: p.Start}
		}
	}
	return AllocationPlan{Outcome: AllocationExhausted}
}

// maxQuotaQty is the first quantity numeric(20,6) cannot store.
var maxQuotaQty = decimal.New(1, 14)

// validateQuotaQty is ValidateQuantity plus the column's upper bound.
func validateQuotaQty(qty decimal.Decimal) error {
	if err := ValidateQuantity(qty); err != nil {
		return err
	}
	if qty.GreaterThanOrEqual(maxQuotaQty) {
		return errors.New("quantity must be below 100000000000000")
	}
	return nil
}

// ValidateQuotaPeriod checks the days and quantity of a new quota. Field
// errors are keyed by JSON name.
func ValidateQuotaPeriod(start, end time.Time, qty decimal.Decimal) map[string]string {
	f := map[string]string{}
	if end.Before(start) {
		f["periodEnd"] = "must not be before periodStart"
	}
	if err := validateQuotaQty(qty); err != nil {
		f["quotaQty"] = err.Error()
	}
	return f
}

// ErrQuotaBelowConsumed refuses a quota change below what is consumed (BR-27.15).
var ErrQuotaBelowConsumed = errors.New("quota cannot be set below the consumed quantity")

// ValidateQuotaChange checks a new quota quantity against the consumed quantity.
func ValidateQuotaChange(newQty, consumed decimal.Decimal) error {
	if err := validateQuotaQty(newQty); err != nil {
		return err
	}
	if newQty.LessThan(consumed) {
		return fmt.Errorf("%w (%s consumed)", ErrQuotaBelowConsumed, consumed.String())
	}
	return nil
}
