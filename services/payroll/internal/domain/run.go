package domain

// RunStatus is the lifecycle state of a pay run.
type RunStatus string

const (
	RunDraft      RunStatus = "DRAFT"
	RunCalculated RunStatus = "CALCULATED"
	RunReviewed   RunStatus = "REVIEWED"
	RunFinalized  RunStatus = "FINALIZED"
	RunPosted     RunStatus = "POSTED"
	RunPaid       RunStatus = "PAID"
	RunCanceled   RunStatus = "CANCELED"
)

// CanTransition reports whether a pay run may move from one state to another:
// DRAFT to CALCULATED to REVIEWED to FINALIZED to POSTED to PAID; CALCULATED
// or REVIEWED back to DRAFT (recalculate); DRAFT, CALCULATED and REVIEWED to
// CANCELED (8.43.7 pay run state machine).
func CanTransition(from, to RunStatus) bool {
	switch from {
	case RunDraft:
		return to == RunCalculated || to == RunCanceled
	case RunCalculated:
		return to == RunReviewed || to == RunDraft || to == RunCanceled
	case RunReviewed:
		return to == RunFinalized || to == RunDraft || to == RunCanceled
	case RunFinalized:
		return to == RunPosted
	case RunPosted:
		return to == RunPaid
	}
	return false
}

// CheckTransition returns a PAY-RUN-005 error for a move the lifecycle does not allow.
func CheckTransition(from, to RunStatus) error {
	if CanTransition(from, to) {
		return nil
	}
	return &Error{Code: CodeRunTransition, Detail: "run cannot move from " + string(from) + " to " + string(to)}
}

// CheckFinalize enforces BR-43.8: the user who calculated a run cannot finalize it.
func CheckFinalize(calculatedBy, finalizedBy string) error {
	if calculatedBy != "" && calculatedBy == finalizedBy {
		return &Error{Code: CodeRunSoD, Detail: "you calculated this run; another user must finalize"}
	}
	return nil
}
