package domain

import "fmt"

const (
	CodeVersionRolling    = "CST-0710"
	CodeActivationLedger  = "CST-0721"
	CodeOnHandUnavailable = "CST-0722"
	CodeReversalTarget    = "CST-0723"
	CodePostingAccounts   = "CST-0724"
	CodeNoStoredStandard  = "CST-0725"
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
