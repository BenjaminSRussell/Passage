package domain

import (
	"fmt"
	"time"
)

// CodeLateFallback is the SCHED-0003 warning: backward scheduling could not
// meet the delivery date, so the order was scheduled forward and is late.
const CodeLateFallback = "SCHED-0003"

// BackwardMargin is how far after the run start the first job of a backward
// schedule must begin; an order that would need to start sooner is scheduled
// forward from the start instead (BR-23.2).
const BackwardMargin = time.Hour

// orderDue is the earliest due date among the operations of one order, zero
// when none has one.
func orderDue(chain []Operation) time.Time {
	var due time.Time
	for _, op := range chain {
		if !op.DueAt.IsZero() && (due.IsZero() || op.DueAt.Before(due)) {
			due = op.DueAt
		}
	}
	return due
}

// probe places a chain with its first operation starting no earlier than
// seed, reports whether every operation was placed and when the chain starts
// and ends, then releases the bookings again.
func (e *engine) probe(chain []Operation, fixed []Placement, start, seed time.Time) (ok bool, first, last time.Time) {
	var scratch Result
	e.placeChain(chain, fixed, start, seed, &scratch)
	for _, pl := range scratch.Placements {
		e.unbook(pl)
		if first.IsZero() || pl.Start.Before(first) {
			first = pl.Start
		}
		if pl.End.After(last) {
			last = pl.End
		}
	}
	return len(scratch.Unplaced) == 0 && len(scratch.Placements) == len(chain), first, last
}

// placeBackward schedules one order backward from its due date (FR-23.2,
// BR-23.2): the first operation starts as late as it can while the last still
// ends by the due date, found by searching the start with the same forward
// engine, so calendars, bookings, capacity blocks, changeovers and networks
// apply exactly as they do forward. If the latest start is before the run
// start plus BackwardMargin, or the order cannot end by its due date even
// when started at once, the order is scheduled forward from the run start;
// when that ends after the due date the placements carry late minutes and
// SCHED-0003 is raised. An order without a due date is scheduled forward.
func (e *engine) placeBackward(chain []Operation, fixed []Placement, start time.Time, res *Result) {
	due := orderDue(chain)
	if due.IsZero() {
		e.placeChain(chain, fixed, start, time.Time{}, res)
		return
	}
	ok, first, last := e.probe(chain, fixed, start, time.Time{})
	if ok && !last.After(due) {
		total := 0
		for _, op := range chain {
			total += op.Minutes
		}
		lo, hi := first, due.Add(-minutes(total))
		for lo.Before(hi) {
			mid := lo.Add(hi.Sub(lo) / 2).Add(time.Minute).Truncate(time.Minute)
			if mid.After(hi) {
				mid = hi
			}
			if fits, _, end := e.probe(chain, fixed, start, mid); fits && !end.After(due) {
				lo = mid
			} else {
				hi = mid.Add(-time.Minute)
			}
		}
		if lo.Sub(start) >= BackwardMargin {
			e.placeChain(chain, fixed, start, lo, res)
			return
		}
	}
	before := len(res.Placements)
	e.placeChain(chain, fixed, start, time.Time{}, res)
	var end time.Time
	for _, pl := range res.Placements[before:] {
		if pl.End.After(end) {
			end = pl.End
		}
	}
	if end.After(due) {
		res.Warnings = append(res.Warnings, Warning{Code: CodeLateFallback, OperationID: chain[len(chain)-1].ID,
			Detail: fmt.Sprintf("Order %s is late by %d min (backward scheduling fell back to forward)", chain[0].OrderID, minutesBetween(due, end))})
	}
}
