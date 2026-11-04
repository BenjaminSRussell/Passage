package domain

import (
	"fmt"
	"sort"
)

// Checks a rollback runs on the plan it would leave (PRD 4.23.14). A rollback
// writes back placements a previous run made; the operations they belong to
// have not changed since (the store skips those that have), but the jobs
// around them may have: a later run, a move or a firm can have put another
// operation of the same order, or another job on the same resource, where the
// restored job no longer fits. Only pairs with a restored job are checked, so
// whatever the plan already held is not blamed on the rollback.

// ChainBreaks lists the restored jobs that no longer follow, or are no longer
// followed by, the job of the neighbouring operation of their order (BR-23.5
// with the FR-23.3 queue times): the next placed operation starts no earlier
// than the previous one's release plus the queue after it and before the
// next, and ends no earlier than the previous one's end plus that queue. ops
// are the operations of the orders concerned with their queue times; placed
// is the plan by operation id, each placement carrying its queue after and
// overlap share; restored names the restored jobs.
func ChainBreaks(ops []Operation, placed map[string]Placement, restored map[string]bool) []Conflict {
	sorted := append([]Operation(nil), ops...)
	sort.SliceStable(sorted, func(i, j int) bool {
		if sorted[i].OrderID != sorted[j].OrderID {
			return sorted[i].OrderID < sorted[j].OrderID
		}
		return sorted[i].OperNumber < sorted[j].OperNumber
	})
	var out []Conflict
	var prev *Placement
	prevOrder := ""
	for _, op := range sorted {
		if op.OrderID != prevOrder {
			prev, prevOrder = nil, op.OrderID
		}
		pl, ok := placed[op.ID]
		if !ok {
			continue
		}
		if prev != nil && (restored[prev.OperationID] || restored[op.ID]) {
			gap := queueWait(prev, op)
			startFloor, endFloor := prev.Release().Add(gap), prev.End.Add(gap)
			if pl.Start.Before(startFloor) || pl.End.Before(endFloor) {
				job := op.ID
				if !restored[job] {
					job = prev.OperationID
				}
				out = append(out, Conflict{Code: CodeOverlap, JobID: job, Detail: fmt.Sprintf(
					"operation %d of order %s would start at %s, before operation %d of the order lets it (%s)",
					op.OperNumber, op.OrderID, pl.Start.UTC().Format("2006-01-02T15:04Z"), prev.OperNumber, startFloor.UTC().Format("2006-01-02T15:04Z"))})
			}
		}
		p := pl
		prev = &p
	}
	return out
}

// ChangeoverShortfalls lists the jobs on a finite resource with a setup
// matrix whose stored changeover no longer covers the step from the setup
// group before them (FR-23.15), where the job or the job before it is
// restored. bs is the resource's finite bookings in start order; r carries the
// matrix and the group the resource is in before the first of them.
func ChangeoverShortfalls(r Resource, bs []Busy, restored map[string]bool) []Conflict {
	if !r.Finite || r.Matrix.Len() == 0 {
		return nil
	}
	c := capacity{res: r}
	var out []Conflict
	group, before := r.CurrentGroup, ""
	for _, b := range bs {
		if b.JobID != before && (restored[b.JobID] || restored[before]) {
			if need, _ := c.need(group, b.Group, b.Setup); need > b.Changeover {
				out = append(out, Conflict{Code: CodeOverlap, JobID: b.JobID, Detail: fmt.Sprintf(
					"job %s would need a %d min changeover from setup group %s but has %d min", b.JobID, need, group, b.Changeover)})
			}
		}
		if b.Group != "" {
			group, before = b.Group, b.JobID
		}
	}
	return out
}
