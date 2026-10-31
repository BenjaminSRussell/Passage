package domain

import "slices"

// WorkStatus is the state of a Work header (BR-31.3).
type WorkStatus string

const (
	WorkOpen              WorkStatus = "OPEN"
	WorkAssigned          WorkStatus = "ASSIGNED"
	WorkInProgress        WorkStatus = "IN_PROGRESS"
	WorkCompleted         WorkStatus = "COMPLETED"
	WorkPartiallyComplete WorkStatus = "PARTIALLY_COMPLETED"
	WorkCancelled         WorkStatus = "CANCELLED"
	WorkBlockedNoLocation WorkStatus = "BLOCKED_NO_LOCATION"
	WorkWaiting           WorkStatus = "WAITING"
)

// allowedNext lists the legal targets from a status. COMPLETED,
// PARTIALLY_COMPLETED and CANCELLED are terminal; reversal creates
// compensating MOVEMENT work instead of changing history. Work enters
// BLOCKED_NO_LOCATION only at creation (BR-31.1 step 6), never from OPEN.
// BLOCKED_NO_LOCATION → CANCELLED is not named by BR-31.3 but is allowed: a
// blocked work has no confirmed line, and without it a wave could never drop
// a line no directive can place. WAITING work has a place for every line but
// depends on open refill work; it becomes OPEN when the last of them completes.
func allowedNext(from WorkStatus) []WorkStatus {
	switch from {
	case WorkOpen:
		return []WorkStatus{WorkAssigned, WorkCancelled}
	case WorkAssigned:
		return []WorkStatus{WorkInProgress, WorkOpen, WorkCancelled}
	case WorkInProgress:
		return []WorkStatus{WorkCompleted, WorkPartiallyComplete}
	case WorkBlockedNoLocation, WorkWaiting:
		return []WorkStatus{WorkOpen, WorkCancelled}
	}
	return nil
}

// CheckTransition enforces BR-31.3. confirmedLines is the number of lines
// already confirmed on the work. Cancellation is refused once any line is
// confirmed, so a confirmed pick can only be undone by a compensating movement.
func CheckTransition(from, to WorkStatus, confirmedLines int) error {
	if !slices.Contains(allowedNext(from), to) {
		return errorf(CodeIllegalTransition, "transition %s to %s is not allowed", from, to)
	}
	if to == WorkCancelled && confirmedLines > 0 {
		return errorf(CodeIllegalTransition, "transition %s to %s is refused after %d confirmed lines", from, to, confirmedLines)
	}
	return nil
}
