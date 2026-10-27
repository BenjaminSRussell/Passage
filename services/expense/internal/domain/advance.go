package domain

import (
	"errors"
	"time"

	"github.com/shopspring/decimal"
)

// AdvanceLifecycle is the cash advance lifecycle (FR-43.15). The post-payment
// statuses PAID, PARTIALLY_SETTLED and SETTLED are the untyped constants in
// settlement.go, so both views of an advance report the same strings.
type AdvanceLifecycle string

const (
	AdvanceDraft     AdvanceLifecycle = "DRAFT"
	AdvanceSubmitted AdvanceLifecycle = "SUBMITTED"
	AdvanceApproved  AdvanceLifecycle = "APPROVED"
	AdvanceRejected  AdvanceLifecycle = "REJECTED"
)

// advanceTransitions lists the legal lifecycle edges. Settlement edges are used
// after payment: PAID may go straight to SETTLED when a report clears the advance.
var advanceTransitions = map[AdvanceLifecycle][]AdvanceLifecycle{
	AdvanceDraft:            {AdvanceSubmitted},
	AdvanceSubmitted:        {AdvanceApproved, AdvanceRejected},
	AdvanceApproved:         {AdvancePaid},
	AdvancePaid:             {AdvancePartiallySettled, AdvanceSettled},
	AdvancePartiallySettled: {AdvanceSettled},
}

// CanTransitionAdvance reports whether an advance may move from one status to another.
func CanTransitionAdvance(from, to AdvanceLifecycle) bool {
	for _, next := range advanceTransitions[from] {
		if next == to {
			return true
		}
	}
	return false
}

// ErrAdvanceNotPaid is returned when settlement or return is attempted before payment.
var ErrAdvanceNotPaid = errors.New("expense: cash advance has not been paid")

// ErrOverReturn is returned when a cash return exceeds the open balance.
var ErrOverReturn = errors.New("expense: return exceeds the open cash advance balance")

// CashAdvance carries the amounts that drive the settlement status. All amounts
// are in the advance currency. Settled is what expense reports applied; Returned,
// Recovered and WrittenOff are BR-43.9 options (a), (b) and (c).
type CashAdvance struct {
	ID         string
	WorkerID   string
	Currency   string
	Amount     decimal.Decimal
	Paid       decimal.Decimal
	Settled    decimal.Decimal
	Returned   decimal.Decimal
	Recovered  decimal.Decimal
	WrittenOff decimal.Decimal
	TripEnd    time.Time
}

// Cleared is everything taken off the paid amount so far.
func (a CashAdvance) Cleared() decimal.Decimal {
	return a.Settled.Add(a.Returned).Add(a.Recovered).Add(a.WrittenOff)
}

// Open is the balance still owed back: paid minus everything cleared, never negative.
func (a CashAdvance) Open() decimal.Decimal {
	return decimal.Max(decimal.Zero, a.Paid.Sub(a.Cleared()))
}

// SettlementStatus derives PAID, PARTIALLY_SETTLED or SETTLED from the amounts,
// with the same rule the report settlement uses (AdvanceStatus). It returns
// ErrAdvanceNotPaid when nothing has been paid.
func (a CashAdvance) SettlementStatus() (string, error) {
	if !a.Paid.IsPositive() {
		return "", ErrAdvanceNotPaid
	}
	return AdvanceStatus(a.Paid, a.Cleared()), nil
}

// Return records cash handed back by the worker (BR-43.9 option a). It may not
// exceed the open balance.
func (a CashAdvance) Return(amount decimal.Decimal) (CashAdvance, error) {
	if !amount.IsPositive() {
		return a, errors.New("expense: cash return must be positive")
	}
	if _, err := a.SettlementStatus(); err != nil {
		return a, err
	}
	if amount.GreaterThan(a.Open()) {
		return a, ErrOverReturn
	}
	a.Returned = a.Returned.Add(amount)
	return a, nil
}

// DueDate is the settlement due date: trip end plus the parameter limit in days (default 30).
func DueDate(tripEnd time.Time, limitDays int) time.Time {
	return tripEnd.AddDate(0, 0, limitDays)
}

// Overdue is true when an advance still has an open balance after its due date;
// the due date itself is still in time. Dates compare by calendar day.
func Overdue(a CashAdvance, asOf time.Time, limitDays int) bool {
	return a.Open().IsPositive() && dayOnly(asOf).After(dayOnly(DueDate(a.TripEnd, limitDays)))
}

// NewAdvancesBlocked is true while any advance of the worker is overdue (EXP-ADV-002).
func NewAdvancesBlocked(advances []CashAdvance, asOf time.Time, limitDays int) bool {
	for _, a := range advances {
		if Overdue(a, asOf, limitDays) {
			return true
		}
	}
	return false
}
