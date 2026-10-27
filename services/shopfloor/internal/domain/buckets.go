package domain

import (
	"errors"
	"fmt"
	"time"

	"github.com/shopspring/decimal"
)

// ErrBucketWindow reports an invalid bucket request.
var ErrBucketWindow = errors.New("oee: invalid bucket window")

// Idle threshold bounds and default (BR-25.6, parameter "Idle threshold s").
const (
	DefaultIdleThreshold = 300 * time.Second
	MinIdleThreshold     = 30 * time.Second
	MaxIdleThreshold     = 3600 * time.Second
)

// OEEStream is one machine's history for an OEE window. Initial is the state in
// effect at the start of the history ("" when unknown): the window start, or
// HistoryFrom when the history begins earlier. Events are the machine's events
// in [HistoryFrom, to) in time order; those before the window start are a
// look-back that only tells idle detection when the last count was. PlannedDown
// lists the downtimes coded as planned that overlap the window (an open one
// ends at the window end).
//
// IdleThreshold turns RUNNING time into IDLE (BR-25.6); zero disables idle
// detection. Horizon is how far the history is known: the first count or state
// event at or after the window end, or the time of the computation when there
// is none. Zero means the history is known through the window end. Time after
// a Horizon inside the window is not scheduled (it has not happened yet).
type OEEStream struct {
	IdealCycleSeconds decimal.Decimal
	Initial           string
	Events            []MachineEvent
	PlannedDown       []Interval
	IdleThreshold     time.Duration
	HistoryFrom       time.Time
	Horizon           time.Time
}

// Interval is a half-open time span [Start, End).
type Interval struct {
	Start, End time.Time
}

// OEEBucket is the OEE of one bucket and the number of events that fell in it.
type OEEBucket struct {
	Start        time.Time
	SourceEvents int
	TotalCount   int64
	GoodCount    int64
	OEE
}

// BuildBuckets computes OEE per bucket of width over [from, to) (FR-25.10).
// Until the w23 calendar supplies planned time, time with a known state other
// than OFF counts as scheduled: RUNNING is run time and IDLE, DOWN and SETUP are
// downtime. Downtime coded as planned is taken out of planned time (BR-25.5);
// the rest is unplanned downtime. OFF (the machine switched off, outside open
// hours) and time before the first known state are not scheduled. Buckets
// without planned time are omitted.
//
// BR-25.6: when a RUNNING stretch has no count for longer than the idle
// threshold, the whole gap from the last count (or from the start of RUNNING)
// to the next count or state change is IDLE, which is downtime; a later count
// closes the gap. At the end of the window the gap runs to the Horizon.
func BuildBuckets(s OEEStream, from, to time.Time, width time.Duration) ([]OEEBucket, error) {
	if width <= 0 || !to.After(from) {
		return nil, fmt.Errorf("%w: from %s to %s width %s", ErrBucketWindow, from, to, width)
	}
	n := int(to.Sub(from) / width)
	if to.Sub(from)%width != 0 {
		n++
	}
	type acc struct {
		sched, run, down time.Duration
		plannedDown      time.Duration
		total, good      int64
		events           int
	}
	accs := make([]acc, n)
	index := func(t time.Time) int { return int(t.Sub(from) / width) }

	// addSpan spreads a known-state span over the buckets it overlaps.
	addSpan := func(start, end time.Time, state string) {
		if state == MachineOff {
			return
		}
		for i := index(start); i < n; i++ {
			bs := from.Add(time.Duration(i) * width)
			if !bs.Before(end) {
				break
			}
			be := bs.Add(width)
			lo, hi := maxTime(start, bs), minTime(end, be)
			if !hi.After(lo) {
				continue
			}
			d := hi.Sub(lo)
			accs[i].sched += d
			if state == MachineRunning {
				accs[i].run += d
			} else {
				accs[i].down += d
				for _, pd := range s.PlannedDown {
					if o := minTime(hi, pd.End).Sub(maxTime(lo, pd.Start)); o > 0 {
						accs[i].plannedDown += o
					}
				}
			}
		}
	}

	start := from
	if !s.HistoryFrom.IsZero() && s.HistoryFrom.Before(from) {
		start = s.HistoryFrom
	}
	// ref is the last count, or the instant the machine started RUNNING.
	cur, curStart, ref := s.Initial, from, start
	// flush books the open span [curStart, end) whose count gap ends at gapEnd.
	flush := func(end, gapEnd time.Time) {
		if cur == "" || !end.After(curStart) {
			return
		}
		state := cur
		if cur == MachineRunning && s.IdleThreshold > 0 && gapEnd.Sub(ref) > s.IdleThreshold {
			state = MachineIdle
		}
		addSpan(curStart, end, state)
	}
	for _, e := range s.Events {
		if e.At.Before(start) || !e.At.Before(to) {
			continue
		}
		if e.At.Before(from) {
			// Look-back: follow the state and the last count, book nothing.
			if e.Kind == EventCount {
				ref = e.At
			}
			if e.State != "" {
				if e.State == MachineRunning && cur != MachineRunning {
					ref = e.At
				}
				cur = e.State
			}
			continue
		}
		i := index(e.At)
		accs[i].events++
		if e.Kind == EventCount {
			accs[i].total += e.GoodCount + e.RejectCount
			accs[i].good += e.GoodCount
			// A count closes the gap: book the span up to it and restart there.
			// Splitting a span at a count does not change the sums.
			flush(e.At, e.At)
			curStart, ref = e.At, e.At
		}
		if e.State != "" {
			flush(e.At, e.At)
			if e.State == MachineRunning && cur != MachineRunning {
				ref = e.At
			}
			cur, curStart = e.State, e.At
		}
	}
	end, gapEnd := to, to
	if !s.Horizon.IsZero() {
		gapEnd = s.Horizon
		if s.Horizon.Before(to) {
			end = s.Horizon
		}
	}
	flush(end, gapEnd)

	var out []OEEBucket
	for i, a := range accs {
		// Planned downtime only counts where the machine was not running, and
		// overlapping planned records never count twice past the down time.
		pd := min(a.plannedDown, a.down)
		if a.sched-pd <= 0 {
			continue
		}
		o, err := Compute(OEEInput{
			ScheduledMinutes:     msToMinutes(a.sched),
			PlannedDownMinutes:   msToMinutes(pd),
			UnplannedDownMinutes: msToMinutes(a.down - pd),
			IdealCycleSeconds:    s.IdealCycleSeconds,
			TotalCount:           a.total,
			GoodCount:            a.good,
		})
		if err != nil {
			return nil, err
		}
		out = append(out, OEEBucket{Start: from.Add(time.Duration(i) * width), SourceEvents: a.events,
			TotalCount: a.total, GoodCount: a.good, OEE: o})
	}
	return out, nil
}

func msToMinutes(d time.Duration) decimal.Decimal {
	return decimal.NewFromInt(d.Milliseconds()).Div(decimal.NewFromInt(60000))
}

func maxTime(a, b time.Time) time.Time {
	if a.After(b) {
		return a
	}
	return b
}

func minTime(a, b time.Time) time.Time {
	if a.Before(b) {
		return a
	}
	return b
}
