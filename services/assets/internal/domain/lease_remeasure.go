package domain

import (
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/money"
)

// CodeLeaseRemeasured is the AST-0516 info message returned with a
// remeasurement (BR-37.8).
const CodeLeaseRemeasured = "AST-0516"

// Lease statuses (Lease.status). Only Active and Modified leases can be
// remeasured.
const (
	LeaseActive     = "Active"
	LeaseModified   = "Modified"
	LeaseTerminated = "Terminated"
)

// Remeasurement is the outcome of a lease modification (BR-37.8).
type Remeasurement struct {
	// NewLiability is the present value of the remaining payments under the
	// revised terms at the start of the effective period.
	NewLiability decimal.Decimal
	// Adjustment is NewLiability minus the carrying liability before the
	// modification; the ROU asset moves by the same amount.
	Adjustment decimal.Decimal
	// ROUAsset is the ROU carrying amount after the adjustment, never negative.
	ROUAsset decimal.Decimal
	// ROUAdjustment is ROUAsset minus the carrying ROU before: equal to
	// Adjustment unless the ROU hit zero.
	ROUAdjustment decimal.Decimal
	// ProfitLossExcess is the part of a negative Adjustment beyond the ROU
	// carrying amount, recognized in profit or loss.
	ProfitLossExcess decimal.Decimal
	// Lines are the remaining periods, numbered from paid + 1; the first
	// carries the remeasurement.
	Lines []LeaseLine
}

// CanModify checks the lease status allows a remeasurement.
func CanModify(status string) error {
	if status != LeaseActive && status != LeaseModified {
		return fail(CodeInvalidLease, "only an Active or Modified lease can be modified; this lease is "+status)
	}
	return nil
}

// Remeasure recomputes the liability for the payments after the first paid
// ones under t, where t.TermMonths is the lease's full (possibly revised) term,
// t.Payment the revised payment and t.AnnualRate the rate the caller chose: the
// revised rate for a scope change, otherwise the original rate (BR-37.8).
//
// opening is the liability at the start of the effective period before any
// remeasurement (the closing of period paid, or the recognized liability when
// paid is 0); current is the carrying liability at that point under the terms
// in force (opening plus an earlier remeasurement of the same period). The
// ROU carrying amount carryingROU moves by the same adjustment; if it would go
// below zero the excess is returned as ProfitLossExcess and the ROU stops at
// zero.
func Remeasure(t LeaseTerms, paid int, opening, current, carryingROU decimal.Decimal) (Remeasurement, error) {
	total, err := validateLease(t)
	if err != nil {
		return Remeasurement{}, err
	}
	if paid < 0 || paid >= total {
		return Remeasurement{}, fail(CodeInvalidLease, "the effective period must leave at least one payment under the revised term")
	}
	if carryingROU.IsNegative() {
		return Remeasurement{}, fail(CodeInvalidLease, "the ROU carrying amount cannot be negative")
	}
	minor := money.MinorUnits(t.Currency)
	r := periodicRate(t)
	newLiability := annuityPV(t.Payment, r, total-paid, t.Advance).Round(minor)
	adjustment := newLiability.Sub(current)
	rou, excess := carryingROU.Add(adjustment), decimal.Zero
	if rou.IsNegative() {
		rou, excess = decimal.Zero, rou.Neg()
	}
	return Remeasurement{
		NewLiability:     newLiability,
		Adjustment:       adjustment,
		ROUAsset:         rou,
		ROUAdjustment:    rou.Sub(carryingROU),
		ProfitLossExcess: excess,
		Lines:            amortize(opening, newLiability, t.Payment, r, paid+1, total-paid, t.Advance, minor, rou),
	}, nil
}
