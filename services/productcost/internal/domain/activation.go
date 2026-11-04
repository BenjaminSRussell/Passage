package domain

import "fmt"

const (
	CodeVersionRolling    = "CST-0710"
	CodeActivationLedger  = "CST-0721"
	CodeOnHandUnavailable = "CST-0722"
	CodeReversalTarget    = "CST-0723"
	CodePostingAccounts   = "CST-0724"
	CodeNoStoredStandard  = "CST-0725"
	CodeNoOnHand          = "CST-0726"
	CodeActivationPending = "CST-0729"
	CodeActivationTooBig  = "CST-0728"

	CodeNotStandardRevaluation = "CST-0727"
	CodeLedgerReplayExpired    = "CST-0730"
	// CodeAttemptNotFound and CodeAttemptNotExpired refuse an operator's decision
	// on an activation attempt: the version has none, or it is still inside the
	// ledger replay window and a retry of the activation settles it (issue 435).
	CodeAttemptNotFound    = "CST-0742"
	CodeAttemptNotExpired  = "CST-0743"
	CodeVersionNotRollable = "CST-0709"

	CodeEngineeringUnavailable = "CST-0737"
	CodeRollerActivates        = "CST-0738"
	CodeRollInput              = "CST-0739"

	MaxLedgerIdempotencyTTLDays  = 3650
	MinLedgerIdempotencyTTLDays  = 400
	LedgerReplaySafetyMarginDays = 30
)

// RollLockKey names the advisory lock a running roll of a version holds and an
// activation of it tries to take (BR-39.7).
func RollLockKey(tenantID, versionID string) string {
	return fmt.Sprintf("productcost.roll:%s:%s", tenantID, versionID)
}

// ReversalLines turns the lines of an activation into the lines of its
// reversal: the same positions and quantities with the old and new standard
// swapped, so the reversal undoes exactly what the activation booked.
func ReversalLines(lines []RevalPreviewLine) []RevalLine {
	out := make([]RevalLine, len(lines))
	for i, ln := range lines {
		out[i] = RevalLine{ItemID: ln.ItemID, SiteID: ln.SiteID, Qty: ln.Qty, OldStd: ln.NewStd, NewStd: ln.OldStd}
	}
	return out
}
