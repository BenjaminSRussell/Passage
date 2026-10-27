package domain

import (
	"testing"
	"time"

	"pgregory.net/rapid"
)

var t0 = time.Date(2026, 9, 1, 8, 0, 0, 0, time.UTC)

func at(h float64) *time.Time {
	v := t0.Add(time.Duration(h * float64(time.Hour)))
	return &v
}

// The kanban rule analysis of PRD 4.24.14: four cycles, one planned before
// planning times were kept, one completed late, one exactly at its due time
// (on time) and one without a due time.
func TestAnalyzeCycles_LeadTimeAndLateShare(t *testing.T) {
	got := AnalyzeCycles([]Cycle{
		{PlannedAt: at(0), Due: at(8), CompletedAt: *at(6)},    // 6 h, on time
		{PlannedAt: at(0), Due: at(8), CompletedAt: *at(10.5)}, // 10.5 h, late
		{PlannedAt: at(0), Due: at(8), CompletedAt: *at(8)},    // 8 h, exactly due: on time
		{PlannedAt: nil, Due: nil, CompletedAt: *at(3)},        // no planning time, no due
	})
	if got.Cycles != 4 || got.TimedCycles != 3 || got.DueCycles != 3 || got.LateCycles != 1 {
		t.Fatalf("counts %+v", got)
	}
	// (6 + 10.5 + 8) / 3 = 8.1666… h → 8.17; 1 of 3 late → 33.33 %.
	if got.AvgLeadTimeH == nil || got.AvgLeadTimeH.String() != "8.17" {
		t.Fatalf("lead time %v", got.AvgLeadTimeH)
	}
	if got.LatePct == nil || got.LatePct.String() != "33.33" {
		t.Fatalf("late %v", got.LatePct)
	}
}

// Nothing measured is reported as unknown, not as zero.
func TestAnalyzeCycles_NothingMeasured(t *testing.T) {
	empty := AnalyzeCycles(nil)
	if empty.Cycles != 0 || empty.AvgLeadTimeH != nil || empty.LatePct != nil {
		t.Fatalf("empty %+v", empty)
	}
	untimed := AnalyzeCycles([]Cycle{{CompletedAt: t0}})
	if untimed.Cycles != 1 || untimed.TimedCycles != 0 || untimed.AvgLeadTimeH != nil || untimed.LatePct != nil {
		t.Fatalf("untimed %+v", untimed)
	}
	// A clock step that puts completion before planning is not a negative lead time.
	skewed := AnalyzeCycles([]Cycle{{PlannedAt: at(2), CompletedAt: *at(1)}})
	if skewed.TimedCycles != 0 || skewed.AvgLeadTimeH != nil {
		t.Fatalf("skewed %+v", skewed)
	}
}

// Property: the late share is between 0 and 100, the counts never exceed the
// cycles, and an all-late or all-on-time set is exactly 100 or 0.
func TestAnalyzeCycles_Bounds(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		n := rapid.IntRange(1, 40).Draw(t, "n")
		late := rapid.Bool().Draw(t, "late")
		cycles := make([]Cycle, n)
		for i := range cycles {
			lead := rapid.IntRange(0, 500).Draw(t, "lead")
			planned := t0.Add(time.Duration(i) * time.Hour)
			due := planned.Add(8 * time.Hour)
			done := planned.Add(time.Duration(lead) * time.Minute)
			if late {
				done = due.Add(time.Duration(lead+1) * time.Minute)
			} else if done.After(due) {
				done = due
			}
			cycles[i] = Cycle{PlannedAt: &planned, Due: &due, CompletedAt: done}
		}
		got := AnalyzeCycles(cycles)
		if got.Cycles != n || got.TimedCycles != n || got.DueCycles != n {
			t.Fatalf("counts %+v", got)
		}
		want := "0"
		if late {
			want = "100"
		}
		if got.LatePct.String() != want || got.AvgLeadTimeH.IsNegative() {
			t.Fatalf("late=%v pct=%s lead=%s", late, got.LatePct, got.AvgLeadTimeH)
		}
	})
}
