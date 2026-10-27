package domain

import "fmt"

// Triage actions a planner takes on a maintenance request (FR-44.6).
const (
	TriageTriage = "TRIAGE"
	TriageReject = "REJECT"
	TriageDefer  = "DEFER"
)

// TriageTarget returns the status a request moves to when action is applied in
// status from. Repeating an action that already holds is a no-op (returns from).
// CONVERTED is terminal; a request converted to a work order cannot be triaged.
func TriageTarget(from, action string) (string, error) {
	switch action {
	case TriageTriage:
		switch from {
		case "NEW", "DEFERRED", "TRIAGED":
			return "TRIAGED", nil
		}
	case TriageReject:
		switch from {
		case "NEW", "TRIAGED", "DEFERRED", "REJECTED":
			return "REJECTED", nil
		}
	case TriageDefer:
		switch from {
		case "NEW", "TRIAGED", "DEFERRED":
			return "DEFERRED", nil
		}
	default:
		return "", fmt.Errorf("unknown triage action %q", action)
	}
	return "", fmt.Errorf("triage %s not allowed from %s", action, from)
}
