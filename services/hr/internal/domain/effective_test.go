package domain

import (
	"testing"
	"time"

	"github.com/shopspring/decimal"
	"pgregory.net/rapid"
)

func day(n int) time.Time { return time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC).AddDate(0, 0, n) }

func TestRange_HalfOpen(t *testing.T) {
	end := day(10)
	r := Range{From: day(0), To: &end}
	cases := []struct {
		name string
		at   time.Time
		want bool
	}{
		{"start is inside", day(0), true},
		{"last instant is inside", day(10).Add(-time.Nanosecond), true},
		{"end is outside", day(10), false},
		{"before start is outside", day(-1), false},
	}
	for _, c := range cases {
		if got := r.Contains(c.at); got != c.want {
			t.Errorf("%s: Contains = %v, want %v", c.name, got, c.want)
		}
	}
	// Adjacent ranges share no instant, so they do not overlap.
	next := Range{From: day(10)}
	if r.Overlaps(next) || next.Overlaps(r) {
		t.Error("adjacent [a,b) and [b,c) must not overlap")
	}
}

func TestRange_OverlapSymmetricAndOpenEnded(t *testing.T) {
	end := day(5)
	bounded := Range{From: day(0), To: &end}
	open := Range{From: day(3)}
	if !bounded.Overlaps(open) || !open.Overlaps(bounded) {
		t.Error("bounded [0,5) and open [3,) overlap")
	}
	rapid.Check(t, func(t *rapid.T) {
		a := randomRange(t, "a")
		b := randomRange(t, "b")
		if a.Overlaps(b) != b.Overlaps(a) {
			t.Fatalf("overlap not symmetric: %v %v", a, b)
		}
	})
}

// Property: a sequence of non-overlapping ranges gives at most one holder for
// any instant, which is the AC1 guarantee behind as-of queries.
func TestRange_AsOfAtMostOneHolder(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		n := rapid.IntRange(1, 8).Draw(t, "segments")
		var segs []Range
		start := 0
		for i := 0; i < n; i++ {
			start += rapid.IntRange(0, 5).Draw(t, "gap")
			length := rapid.IntRange(1, 40).Draw(t, "len")
			end := day(start + length)
			segs = append(segs, Range{From: day(start), To: &end})
			start += length
		}
		for probe := -2; probe < start+3; probe++ {
			holders := 0
			for _, s := range segs {
				if s.Contains(day(probe)) {
					holders++
				}
			}
			if holders > 1 {
				t.Fatalf("instant %d held by %d ranges", probe, holders)
			}
		}
	})
}

func randomRange(t *rapid.T, label string) Range {
	from := rapid.IntRange(0, 50).Draw(t, label+"from")
	if rapid.Bool().Draw(t, label+"open") {
		return Range{From: day(from)}
	}
	to := day(from + rapid.IntRange(1, 30).Draw(t, label+"len"))
	return Range{From: day(from), To: &to}
}

func TestCheckFTE(t *testing.T) {
	d := decimal.RequireFromString
	cases := []struct {
		name      string
		fte       string
		overfill  bool
		existing  string
		add       string
		wantError bool
	}{
		{"fits exactly", "1.00", false, "0.50", "0.50", false},
		{"second full assignment rejected", "1.00", false, "1.00", "1.00", true},
		{"overfill allowed", "1.00", true, "1.00", "1.00", false},
		{"job share within FTE", "1.00", false, "0.00", "0.60", false},
	}
	for _, c := range cases {
		err := CheckFTE(d(c.fte), c.overfill, d(c.existing), d(c.add))
		if (err != nil) != c.wantError {
			t.Errorf("%s: err = %v, wantError %v", c.name, err, c.wantError)
		}
		if err != nil {
			if e, ok := err.(*Error); !ok || e.Code != "HCM-POS-001" {
				t.Errorf("%s: want HCM-POS-001, got %v", c.name, err)
			}
		}
	}
}

func TestEmploymentStatusFollowsTheHalfOpenRange(t *testing.T) {
	hire := time.Date(2026, 3, 16, 0, 0, 0, 0, time.UTC)
	end := DayAfter(time.Date(2026, 6, 30, 15, 0, 0, 0, time.UTC))
	if !end.Equal(time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)) {
		t.Fatalf("DayAfter(2026-06-30) = %s, want 2026-07-01T00:00Z", end)
	}
	r := Range{From: hire, To: &end}
	cases := []struct {
		at   time.Time
		want string
	}{
		{hire.Add(-time.Nanosecond), StatusPending},
		{hire, StatusActive},
		{end.Add(-time.Nanosecond), StatusActive},
		{end, StatusTerminated},
	}
	for _, c := range cases {
		if got := EmploymentStatus(r, c.at); got != c.want {
			t.Errorf("status at %s = %s, want %s", c.at, got, c.want)
		}
	}
	if got := EmploymentStatus(Range{From: hire}, hire.AddDate(10, 0, 0)); got != StatusActive {
		t.Errorf("open-ended employment ten years on = %s, want ACTIVE", got)
	}
}
