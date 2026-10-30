package domain

import (
	"sort"
	"time"
)

// Override kinds (FR-23.1).
const (
	OverrideDowntime = "downtime"
	OverrideExtra    = "extra"
)

// CapacityOverride closes (downtime) or opens (extra) a UTC span of a
// resource's calendar. A downtime wins over an extra shift that overlaps it.
type CapacityOverride struct {
	Kind  string
	Start time.Time
	End   time.Time
}

// ApplyOverrides returns iv with the extra spans added and the downtime spans
// removed, sorted and merged. Extra spans are clipped to [lo, hi) so that an
// override far beyond the horizon adds nothing. Whole minutes are kept: a span
// that leaves a fragment shorter than a minute drops it.
func ApplyOverrides(iv []Interval, ovs []CapacityOverride, lo, hi time.Time) []Interval {
	if len(ovs) == 0 {
		return iv
	}
	all := append([]Interval(nil), iv...)
	for _, o := range ovs {
		if o.Kind != OverrideExtra {
			continue
		}
		s, e := o.Start, o.End
		if s.Before(lo) {
			s = lo
		}
		if e.After(hi) {
			e = hi
		}
		if e.After(s) {
			all = append(all, Interval{Start: s.UTC(), End: e.UTC()})
		}
	}
	all = mergeIntervals(all)
	for _, o := range ovs {
		if o.Kind != OverrideDowntime {
			continue
		}
		var next []Interval
		for _, i := range all {
			if !o.End.After(i.Start) || !o.Start.Before(i.End) {
				next = append(next, i)
				continue
			}
			if o.Start.After(i.Start) {
				next = append(next, Interval{Start: i.Start, End: o.Start.UTC()})
			}
			if o.End.Before(i.End) {
				next = append(next, Interval{Start: o.End.UTC(), End: i.End})
			}
		}
		all = next
	}
	out := all[:0]
	for _, i := range all {
		if i.Minutes() > 0 {
			out = append(out, i)
		}
	}
	return out
}

func mergeIntervals(iv []Interval) []Interval {
	sort.Slice(iv, func(i, j int) bool { return iv[i].Start.Before(iv[j].Start) })
	var out []Interval
	for _, i := range iv {
		if n := len(out); n > 0 && !i.Start.After(out[n-1].End) {
			if i.End.After(out[n-1].End) {
				out[n-1].End = i.End
			}
			continue
		}
		out = append(out, i)
	}
	return out
}

// OpenWithOverrides is OpenIntervals with the resource's capacity overrides
// applied over the same window.
func OpenWithOverrides(loc *time.Location, shifts []Shift, ovs []CapacityOverride, from time.Time, days int) []Interval {
	iv := OpenIntervals(loc, shifts, from, days)
	if len(ovs) == 0 {
		return iv
	}
	if loc == nil {
		loc = time.UTC
	}
	y, m, d := from.In(loc).Date()
	lo := time.Date(y, m, d, 0, 0, 0, 0, loc).UTC()
	hi := time.Date(y, m, d+days, 0, 0, 0, 0, loc).UTC()
	return ApplyOverrides(iv, ovs, lo, hi)
}

// Open is the resource's open capacity over days local days from from, with
// its overrides applied.
func (r Resource) Open(from time.Time, days int) []Interval {
	return OpenWithOverrides(r.Location, r.Shifts, r.Overrides, from, days)
}

// OutsideOpen reports whether any part of [start, end) lies outside the open
// intervals iv (SCHED-0002): a booked job a later downtime overlaps, or one a
// calendar no longer covers (AC-23.7). Adjacent intervals count as one.
func OutsideOpen(iv []Interval, start, end time.Time) bool {
	t := start
	for _, i := range iv {
		if !t.Before(end) {
			return false
		}
		if !i.End.After(t) {
			continue
		}
		if i.Start.After(t) {
			return true
		}
		t = i.End
	}
	return t.Before(end)
}
