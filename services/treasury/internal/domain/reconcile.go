package domain

import (
	"fmt"

	"github.com/shopspring/decimal"
)

// Reconciliation error codes. TRS-0410 is in the catalog; TRS-0436 is a
// slice-local code for a state that does not allow the transition (see README).
// The rules are pure for now: the reconciliation header, store and close
// endpoint arrive with the workspace (FR-36.5, see NOT-IMPLEMENTED.md).
const (
	CodeReconcileDifference = "TRS-0410"
	CodeReconcileState      = "TRS-0436"
)

// ReconciliationState is the header state of BR-36.3.
type ReconciliationState string

const (
	ReconOpen       ReconciliationState = "open"
	ReconInProgress ReconciliationState = "in_progress"
	ReconClosed     ReconciliationState = "closed"
	ReconReopened   ReconciliationState = "reopened"
)

// CanTransition encodes BR-36.3: Open -> InProgress -> Closed, and Closed ->
// Reopened only (reopening allows new matches and reverses nothing). Reopened
// behaves like InProgress and can close again. Who may reopen
// (TreasuryReconcileApprove, open fiscal period) is checked by the caller.
func (s ReconciliationState) CanTransition(to ReconciliationState) bool {
	switch s {
	case ReconOpen:
		return to == ReconInProgress
	case ReconInProgress, ReconReopened:
		return to == ReconClosed
	case ReconClosed:
		return to == ReconReopened
	default:
		return false
	}
}

// Difference is the close test of FR-36.5: bank closing − (book balance +
// outstanding items). Zero means the reconciliation can close.
func Difference(bankClosing, bookBalance, outstanding decimal.Decimal) decimal.Decimal {
	return bankClosing.Sub(bookBalance.Add(outstanding))
}

// CheckClose returns nil when the reconciliation may close: the state allows
// the transition and the difference is exactly zero (AC-36.4: 0.01 is refused).
// The error carries TRS-0410 with the difference, or TRS-0436 for a state.
func CheckClose(state ReconciliationState, bankClosing, bookBalance, outstanding decimal.Decimal) error {
	if !state.CanTransition(ReconClosed) {
		return &Error{Code: CodeReconcileState, Detail: fmt.Sprintf("reconciliation in state %s cannot close", state)}
	}
	if diff := Difference(bankClosing, bookBalance, outstanding); !diff.IsZero() {
		return &Error{Code: CodeReconcileDifference, Detail: "reconciliation cannot close: difference " + diff.String()}
	}
	return nil
}
