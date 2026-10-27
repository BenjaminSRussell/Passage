package domain

import (
	"testing"
	"time"
)

// blockClock is a Monday 2026-03-02 clock in UTC.
func blockClock(h, m int) time.Time {
	return time.Date(2026, 3, 2, h, m, 0, 0, time.UTC)
}

func covers(ivs []Interval, at time.Time) bool {
	for _, iv := range ivs {
		if !at.Before(iv.Start) && at.Before(iv.End) {
			return true
		}
	}
	return false
}

// mondayBlock is the FR-23.17 example: Monday 06:00-14:00 restricted to color = white.
func mondayBlock(priority int, cond ...Predicate) CapacityBlock {
	return CapacityBlock{ID: "mon", ResourceID: "r1", Priority: priority,
		Windows:   []Interval{{Start: blockClock(6, 0), End: blockClock(14, 0)}},
		Condition: cond}
}

// TestPlaceable_AC23_13 covers AC-23.13: a white job may use the Monday block,
// a red job may not, but both may use unblocked capacity.
func TestPlaceable_AC23_13(t *testing.T) {
	open := OpenIntervals(time.UTC, allDays(0, 1440), blockClock(0, 0), 1)
	blocks := []CapacityBlock{mondayBlock(1, Predicate{Attribute: "color", Op: "=", Value: "white"})}

	white := Placeable(open, blocks, map[string]string{"color": "white"})
	if !covers(white, blockClock(8, 0)) {
		t.Fatal("white job cannot use the white block")
	}
	red := Placeable(open, blocks, map[string]string{"color": "red"})
	if covers(red, blockClock(8, 0)) {
		t.Fatal("red job placed inside the white block")
	}
	if !covers(red, blockClock(15, 0)) || !covers(red, blockClock(3, 0)) {
		t.Fatal("red job lost unblocked capacity")
	}
}

// TestPlaceable_PriorityTakesTime checks that a higher-priority block which does
// not match the job keeps its time, even where a matching block overlaps it.
func TestPlaceable_PriorityTakesTime(t *testing.T) {
	open := OpenIntervals(time.UTC, allDays(0, 1440), blockClock(0, 0), 1)
	blocks := []CapacityBlock{
		mondayBlock(1, Predicate{Attribute: "color", Op: "=", Value: "white"}),
		{ID: "red-only", ResourceID: "r1", Priority: 5,
			Windows:   []Interval{{Start: blockClock(8, 0), End: blockClock(10, 0)}},
			Condition: []Predicate{{Attribute: "color", Op: "=", Value: "red"}}},
	}
	white := Placeable(open, blocks, map[string]string{"color": "white"})
	if covers(white, blockClock(9, 0)) {
		t.Fatal("higher-priority red block did not take its time from the white block")
	}
	if !covers(white, blockClock(7, 0)) || !covers(white, blockClock(12, 0)) {
		t.Fatal("white job lost the parts of its block outside the red block")
	}
}

func TestPlaceable_NoBlocksIsOpenCapacity(t *testing.T) {
	open := OpenIntervals(time.UTC, allDays(0, 1440), blockClock(0, 0), 1)
	got := Placeable(open, nil, nil)
	if len(got) != 1 || !got[0].Start.Equal(blockClock(0, 0)) || !got[0].End.Equal(blockClock(0, 0).AddDate(0, 0, 1)) {
		t.Fatalf("open day merged to %v", got)
	}
}

func TestPredicate(t *testing.T) {
	attrs := map[string]string{"grade": "3", "color": "white"}
	cases := []struct {
		name string
		p    Predicate
		want bool
	}{
		{"equal", Predicate{"color", "=", "white"}, true},
		{"not equal", Predicate{"color", "!=", "white"}, false},
		{"grade at least 2", Predicate{"grade", ">=", "2"}, true},
		{"grade below 2", Predicate{"grade", "<", "2"}, false},
		{"missing attribute never holds", Predicate{"lot", "=", ""}, false},
		{"non-numeric compare never holds", Predicate{"color", ">", "1"}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.p.Holds(attrs); got != tc.want {
				t.Fatalf("Holds = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestPredicateValidate(t *testing.T) {
	bad := []Predicate{
		{Attribute: "", Op: "="},
		{Attribute: "grade", Op: "~"},
		{Attribute: "grade", Op: ">=", Value: "two"},
	}
	for _, p := range bad {
		if err := p.Validate(); err == nil {
			t.Fatalf("%+v accepted", p)
		}
	}
	if err := (Predicate{Attribute: "grade", Op: ">=", Value: "2"}).Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestCapacityBlockMatchesEmptyCondition(t *testing.T) {
	if !(CapacityBlock{}).Matches(nil) {
		t.Fatal("a block without conditions must match every job")
	}
}
