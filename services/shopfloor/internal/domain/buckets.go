package domain

import (
	"errors"
	"fmt"
	"time"

	"github.com/shopspring/decimal"
)

// ErrBucketWindow reports an invalid bucket request.
var ErrBucketWindow = errors.New("oee: invalid bucket window")

// OEEStream is one machine's history for an OEE window. Initial is the state in
// effect at the window start ("" when unknown). Events are the machine's events
// inside [from, to) in time order. PlannedDown lists the downtimes coded as
// planned that overlap the window (an open one ends at the window end).
type OEEStream struct {
	IdealCycleSeconds decimal.Decimal
	Initial           string
	Events            []MachineEvent
	PlannedDown       []Interval
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

	cur, curStart := s.Initial, from
	for _, e := range s.Events {
		if e.At.Before(from) || !e.At.Before(to) {
			continue
		}
		i := index(e.At)
		accs[i].events++
		if e.Kind == EventCount {
			accs[i].total += e.GoodCount + e.RejectCount
			accs[i].good += e.GoodCount
		}
		if e.State != "" {
			if cur != "" {
				addSpan(curStart, e.At, cur)
			}
			cur, curStart = e.State, e.At
		}
	}
	if cur != "" {
		addSpan(curStart, to, cur)
	}

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
