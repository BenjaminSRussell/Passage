package domain

import (
	"fmt"
	"sort"
	"time"
)

// CodeSecondaryBusy is SCHED-0005: a secondary resource is not free for the
// selected slot (BR-23.4).
const CodeSecondaryBusy = "SCHED-0005"

// MaxSecondaries bounds the secondary resources one operation books.
const MaxSecondaries = 5

// farShift is the shift reported when a secondary has no open capacity left in
// the horizon; it carries the retry past the horizon so placement fails.
const farShift = 3650 * 24 * time.Hour

// secFn checks the segments of a slot on the primary against the operation's
// secondary resources (BR-23.4). A zero shift means every secondary is free
// for every segment; otherwise shift is how much later the slot must start to
// clear the first obstacle, and detail names it.
type secFn func(segs []Segment) (shift time.Duration, detail string)

// secondaryChecker returns the check for op, or nil when it books no
// secondary resource. Secondaries must be inside open capacity for each
// segment (adjacent intervals, such as two back-to-back shifts or midnight on
// a 24-hour calendar, count as one), and free of booked work when they are
// finite and the run is finite. busy is read at call time, so it sees
// bookings made after this call.
//
// The shift is measured from the slot's first segment: work is laid
// continuously over the primary's open time, so every later start up to the
// end of the obstacle still covers it, and the retry jumps straight past it.
func secondaryChecker(caps map[string]capacity, busy map[string][]Busy, op Operation, infinite bool) secFn {
	if len(op.Secondaries) == 0 {
		return nil
	}
	return func(segs []Segment) (time.Duration, string) {
		first := segs[0].Start
		for _, sid := range op.Secondaries {
			c, ok := caps[sid]
			if !ok {
				continue
			}
			for _, seg := range segs {
				if gap, closed := firstGap(c.iv, seg.Start, seg.End); closed {
					shift := farShift
					if next, ok := nextOpen(c.iv, gap); ok {
						shift = next.Sub(first)
					}
					return shift, fmt.Sprintf("secondary resource %s has no open capacity at %s", c.res.Code, gap.UTC().Format(time.RFC3339))
				}
				if !c.res.Finite || infinite {
					continue
				}
				bs := busy[sid]
				if k := sort.Search(len(bs), func(k int) bool { return bs[k].End.After(seg.Start) }); k < len(bs) && bs[k].Start.Before(seg.End) {
					return bs[k].End.Sub(first), fmt.Sprintf("secondary resource %s is booked by job %s", c.res.Code, bs[k].JobID)
				}
			}
		}
		return 0, ""
	}
}

// secondaryBooked is the SCHED-0005 conflict of the first secondary resource
// of op that another job already holds during segs, or nil. Unlike
// secondaryChecker it ignores closed time, so a move over constraints can tell
// a closed secondary (overridable) from a booked one (never overridden).
func secondaryBooked(caps map[string]capacity, busy map[string][]Busy, op Operation, segs []Segment) *Conflict {
	for _, sid := range op.Secondaries {
		c, ok := caps[sid]
		if !ok || !c.res.Finite {
			continue
		}
		bs := busy[sid]
		for _, seg := range segs {
			if k := sort.Search(len(bs), func(k int) bool { return bs[k].End.After(seg.Start) }); k < len(bs) && bs[k].Start.Before(seg.End) {
				return &Conflict{Code: CodeSecondaryBusy, JobID: op.ID,
					Detail: fmt.Sprintf("secondary resource %s is booked by job %s and a booking is never overridden", c.res.Code, bs[k].JobID)}
			}
		}
	}
	return nil
}

// firstGap is the first instant of [start, end) that the open intervals iv do
// not cover, with closed set; adjacent or overlapping intervals count as one.
func firstGap(iv []Interval, start, end time.Time) (time.Time, bool) {
	t := start
	for i := sort.Search(len(iv), func(k int) bool { return iv[k].End.After(start) }); i < len(iv) && t.Before(end); i++ {
		if iv[i].Start.After(t) {
			return t, true
		}
		if iv[i].End.After(t) {
			t = iv[i].End
		}
	}
	return t, t.Before(end)
}

// placeOnSec is placeOn with the secondary resources of the operation
// booked for the same segments: when one is not free, the slot is retried no
// earlier than the shift that clears it (BR-23.4: the primary slot is skipped).
// When the primary runs out of capacity after a slot was skipped for a
// secondary, the conflict is SCHED-0005 naming the last obstacle.
func placeOnSec(c capacity, op Operation, bs []Busy, ready time.Time, seq bool, sec secFn) (slot, *Conflict) {
	var skipped string
	for {
		s, conf := placeOn(c, op, bs, ready, seq)
		if conf != nil && skipped != "" {
			return s, &Conflict{Code: CodeSecondaryBusy, Detail: skipped, JobID: op.ID}
		}
		if conf != nil || sec == nil {
			return s, conf
		}
		shift, detail := sec(s.segs)
		if shift <= 0 {
			return s, nil
		}
		if shift < time.Minute {
			shift = time.Minute
		}
		skipped = detail
		ready = s.segs[0].Start.Add(shift)
	}
}

// secondaryBusy is the booking a placement makes on each secondary: the same
// segments as the primary, keyed by the job.
func secondaryBusy(pl Placement) []Busy {
	var out []Busy
	for _, sid := range pl.Secondaries {
		for _, s := range pl.Segments {
			out = append(out, Busy{ResourceID: sid, Start: s.Start, End: s.End, JobID: pl.OperationID})
		}
	}
	return out
}

// bookSecondaries adds a placement's secondary bookings to busy for finite
// resources.
func bookSecondaries(busy map[string][]Busy, caps map[string]capacity, pl Placement) {
	for _, b := range secondaryBusy(pl) {
		if caps[b.ResourceID].res.Finite {
			busy[b.ResourceID] = insertBusy(busy[b.ResourceID], b)
		}
	}
}

// dropJob removes every booking of a job, on its primary and secondaries.
func dropJob(busy map[string][]Busy, jobID string) {
	for rid := range busy {
		busy[rid] = removeJob(busy[rid], jobID)
	}
}
