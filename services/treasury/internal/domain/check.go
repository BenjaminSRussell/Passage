package domain

import (
	"fmt"
	"strings"
)

// CheckStatus is the lifecycle state of an issued check (FR-36.6, BR-36.5).
type CheckStatus string

// Check lifecycle states.
const (
	CheckCreated CheckStatus = "created"
	CheckPrinted CheckStatus = "printed"
	CheckSent    CheckStatus = "sent"
	CheckCleared CheckStatus = "cleared"
	CheckVoided  CheckStatus = "voided"
	CheckStopped CheckStatus = "stopped"
)

// AllCheckStatuses lists every lifecycle state.
var AllCheckStatuses = []CheckStatus{CheckCreated, CheckPrinted, CheckSent, CheckCleared, CheckVoided, CheckStopped}

// Problem codes raised by the check rules (see README).
const (
	CodeCheckCleared  = "TRS-0414"
	CodeCheckState    = "TRS-0420"
	CodeCheckCurrency = "TRS-0426"
)

// checkNext is the allowed transition graph (FR-36.6: Created -> Printed ->
// Sent -> Cleared / Voided / Stopped). A stop-payment order only makes sense
// once the check has left the company, so only a Sent check can be stopped; a
// check still in hand is voided instead. Cleared and Voided are terminal; a
// Stopped check can still be voided to retire its number.
var checkNext = map[CheckStatus][]CheckStatus{
	CheckCreated: {CheckPrinted, CheckVoided},
	CheckPrinted: {CheckSent, CheckVoided},
	CheckSent:    {CheckCleared, CheckStopped, CheckVoided},
	CheckStopped: {CheckVoided},
}

// CanCheckTransition reports whether the graph has an edge from -> to.
func CanCheckTransition(from, to CheckStatus) bool {
	for _, n := range checkNext[from] {
		if n == to {
			return true
		}
	}
	return false
}

// TransitionCheck returns nil when a check may move from one state to another.
// Voiding a cleared check is refused with TRS-0414 (BR-36.5).
func TransitionCheck(from, to CheckStatus) error {
	if from == CheckCleared && to == CheckVoided {
		return &Error{Code: CodeCheckCleared, Detail: "a cleared check cannot be voided; post a reversal through accounts payable instead"}
	}
	if !CanCheckTransition(from, to) {
		return &Error{Code: CodeCheckState, Detail: fmt.Sprintf("a %s check cannot move to %s", from, to)}
	}
	return nil
}

// NeedsPositivePayVoid reports whether voiding a check in this state must
// also send a void record to the bank's positive pay file (BR-36.5): the bank
// knows a check once it is Sent, and a Stopped check was Sent first.
func NeedsPositivePayVoid(from CheckStatus) bool {
	return from == CheckSent || from == CheckStopped
}

// ValidVoidReason requires a non-blank reason of at most 200 characters.
func ValidVoidReason(reason string) bool {
	n := len(strings.TrimSpace(reason))
	return n >= 1 && n <= 200
}
