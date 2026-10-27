package domain

import "time"

// Assignment is a worker's hold of a position over a validity range.
type Assignment struct {
	WorkerID   string
	PositionID string
	Primary    bool
	Validity   Range
}

// Edge says a child position reports to a parent position over a range.
type Edge struct {
	ChildPositionID  string
	ParentPositionID string
	Validity         Range
}

// Manager answers BR-42.1: the manager of a worker as-of an instant is the
// primary incumbent of the position the worker's primary position reports to.
// A vacant reports-to position is skipped by walking up, at most maxDepth
// levels. It returns false when no incumbent is found (AX "vacant position").
func Manager(workerID string, at time.Time, assignments []Assignment, edges []Edge, maxDepth int) (string, bool) {
	position := ""
	for _, a := range assignments {
		if a.WorkerID == workerID && a.Primary && a.Validity.Contains(at) {
			position = a.PositionID
			break
		}
	}
	if position == "" {
		return "", false
	}
	for depth := 0; depth < maxDepth; depth++ {
		parent := ""
		for _, e := range edges {
			if e.ChildPositionID == position && e.Validity.Contains(at) {
				parent = e.ParentPositionID
				break
			}
		}
		if parent == "" {
			return "", false
		}
		for _, a := range assignments {
			if a.PositionID == parent && a.Primary && a.Validity.Contains(at) {
				return a.WorkerID, true
			}
		}
		position = parent
	}
	return "", false
}

// WouldCycle reports whether making child report to parent, as-of the given
// instant, closes a loop in the hierarchy (BR-42.7, HCM-ORG-001). The walk up
// from parent is bounded by len(edges) so corrupt data cannot loop forever.
func WouldCycle(edges []Edge, child, parent string, at time.Time) bool {
	if child == parent {
		return true
	}
	current := parent
	for steps := 0; steps <= len(edges); steps++ {
		next := ""
		for _, e := range edges {
			if e.ChildPositionID == current && e.Validity.Contains(at) {
				next = e.ParentPositionID
				break
			}
		}
		if next == "" {
			return false
		}
		if next == child {
			return true
		}
		current = next
	}
	return false
}
