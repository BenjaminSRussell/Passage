package domain

import (
	"errors"
	"sort"
	"testing"
	"time"

	"github.com/shopspring/decimal"
	"pgregory.net/rapid"
)

func minutes(m int) time.Time { return floorNow.Add(time.Duration(m) * time.Minute) }

// BR-25.5 shift example: planned 480 min, 60 min unplanned down, ideal 60 s,
// total 380, good 361. A = 0.8750, P = 0.9048, Q = 0.9500.
//
// The exact product is 0.75208, which rounds to 0.7521. The PRD states 0.7522;
// the formula is applied as written here and the difference is noted in
// NOT-IMPLEMENTED.md for the PRD owner.
func TestBuildBuckets_ShiftExample(t *testing.T) {
	s := OEEStream{
		IdealCycleSeconds: decimal.NewFromInt(60),
		Initial:           MachineRunning,
		Events: []MachineEvent{
			{Seq: 1, Kind: EventCount, At: minutes(100), GoodCount: 361, RejectCount: 19},
			{Seq: 2, Kind: EventState, At: minutes(420), State: MachineDown},
		},
	}
	got, err := BuildBuckets(s, floorNow, minutes(480), 480*time.Minute)
	if err != nil || len(got) != 1 {
		t.Fatalf("buckets %+v, %v", got, err)
	}
	b := got[0]
	if !b.PlannedMinutes.Equal(decimal.NewFromInt(480)) || !b.RunMinutes.Equal(decimal.NewFromInt(420)) {
		t.Fatalf("planned %s run %s", b.PlannedMinutes, b.RunMinutes)
	}
	if b.TotalCount != 380 || b.GoodCount != 361 || b.SourceEvents != 2 {
		t.Fatalf("counts total %d good %d events %d", b.TotalCount, b.GoodCount, b.SourceEvents)
	}
	if !b.Availability.Equal(decimal.RequireFromString("0.875")) ||
		!b.Performance.Equal(decimal.RequireFromString("0.9048")) ||
		!b.Quality.Equal(decimal.RequireFromString("0.95")) {
		t.Fatalf("A %s P %s Q %s", b.Availability, b.Performance, b.Quality)
	}
	if !b.OEE.OEE.Equal(decimal.RequireFromString("0.7521")) {
		t.Fatalf("oee %s", b.OEE.OEE)
	}
}

func TestBuildBuckets_SplitsAcrossBuckets(t *testing.T) {
	s := OEEStream{
		IdealCycleSeconds: decimal.NewFromInt(60),
		Initial:           MachineRunning,
		Events:            []MachineEvent{{Seq: 1, Kind: EventState, At: minutes(10), State: MachineDown}},
	}
	got, err := BuildBuckets(s, floorNow, minutes(30), 15*time.Minute)
	if err != nil || len(got) != 2 {
		t.Fatalf("buckets %d, %v", len(got), err)
	}
	// Bucket 0: RUNNING 0-10 then DOWN 10-15. Bucket 1: DOWN throughout.
	if !got[0].RunMinutes.Equal(decimal.NewFromInt(10)) || !got[1].RunMinutes.Equal(decimal.Zero) {
		t.Fatalf("run %s and %s", got[0].RunMinutes, got[1].RunMinutes)
	}
	if !got[1].Start.Equal(minutes(15)) {
		t.Fatalf("second bucket starts %s", got[1].Start)
	}
	if !got[1].OEE.OEE.IsZero() {
		t.Fatalf("oee of a stopped bucket = %s", got[1].OEE.OEE)
	}
}

func TestBuildBuckets_OverRateIsFlagged(t *testing.T) {
	s := OEEStream{
		IdealCycleSeconds: decimal.NewFromInt(60),
		Initial:           MachineRunning,
		Events:            []MachineEvent{{Seq: 1, Kind: EventCount, At: minutes(1), GoodCount: 500}},
	}
	got, err := BuildBuckets(s, floorNow, minutes(15), 15*time.Minute)
	if err != nil || len(got) != 1 {
		t.Fatalf("buckets %+v, %v", got, err)
	}
	if !got[0].OverRate || !got[0].Performance.Equal(decimal.NewFromInt(1)) {
		t.Fatalf("over rate %v performance %s", got[0].OverRate, got[0].Performance)
	}
}

func TestBuildBuckets_UnknownStateIsNotScheduled(t *testing.T) {
	s := OEEStream{IdealCycleSeconds: decimal.NewFromInt(60), Events: []MachineEvent{
		{Seq: 1, Kind: EventState, At: minutes(20), State: MachineRunning},
	}}
	got, err := BuildBuckets(s, floorNow, minutes(30), 15*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	// Only 20-30 is known: the 15-30 bucket has 10 scheduled minutes, 0-15 has none.
	if len(got) != 1 || !got[0].PlannedMinutes.Equal(decimal.NewFromInt(10)) || !got[0].Start.Equal(minutes(15)) {
		t.Fatalf("buckets %+v", got)
	}
	if none, err := BuildBuckets(OEEStream{IdealCycleSeconds: decimal.NewFromInt(60)}, floorNow, minutes(30), 15*time.Minute); err != nil || len(none) != 0 {
		t.Fatalf("no state history gave %d buckets, %v", len(none), err)
	}
}

func TestBuildBuckets_InvalidWindow(t *testing.T) {
	_, err := BuildBuckets(OEEStream{}, minutes(10), minutes(10), 15*time.Minute)
	if !errors.Is(err, ErrBucketWindow) {
		t.Fatalf("empty window: %v", err)
	}
	_, err = BuildBuckets(OEEStream{}, minutes(0), minutes(10), 0)
	if !errors.Is(err, ErrBucketWindow) {
		t.Fatalf("zero width: %v", err)
	}
}

// Property: over a window whose state is known throughout, the bucketed run and
// scheduled minutes equal a minute-by-minute walk of the same timeline, and no
// bucket reports more run time than planned time (BR-25.5 invariant).
func TestBuildBuckets_PropertyMatchesMinuteWalk(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		const window = 120
		n := rapid.IntRange(0, 8).Draw(t, "changes")
		at := make([]int, 0, n)
		for i := 0; i < n; i++ {
			at = append(at, rapid.IntRange(1, window-1).Draw(t, "minute"))
		}
		sort.Ints(at)
		states := []string{MachineRunning, MachineDown, MachineIdle}
		var events []MachineEvent
		state := map[int]string{}
		cur := MachineRunning
		last := -1
		for i, m := range at {
			if m == last {
				continue
			}
			last = m
			st := rapid.SampledFrom(states).Draw(t, "state")
			events = append(events, MachineEvent{Seq: int64(i + 1), Kind: EventState, At: minutes(m), State: st})
			state[m] = st
		}
		// Reference walk: minute k takes the state of the last change at or before k,
		// and the initial RUNNING state applies before the first change.
		wantRun := 0
		cur = MachineRunning
		for k := 0; k < window; k++ {
			if st, ok := state[k]; ok {
				cur = st
			}
			if cur == MachineRunning {
				wantRun++
			}
		}

		got, err := BuildBuckets(OEEStream{IdealCycleSeconds: decimal.NewFromInt(60), Initial: MachineRunning, Events: events},
			floorNow, minutes(window), 15*time.Minute)
		if err != nil {
			t.Fatal(err)
		}
		var planned, run decimal.Decimal
		for _, b := range got {
			if b.RunMinutes.GreaterThan(b.PlannedMinutes) {
				t.Fatalf("run %s above planned %s", b.RunMinutes, b.PlannedMinutes)
			}
			planned = planned.Add(b.PlannedMinutes)
			run = run.Add(b.RunMinutes)
		}
		if !planned.Equal(decimal.NewFromInt(window)) {
			t.Fatalf("planned %s, want %d", planned, window)
		}
		if !run.Equal(decimal.NewFromInt(int64(wantRun))) {
			t.Fatalf("run %s, want %d", run, wantRun)
		}
	})
}

// A machine switched OFF is outside open hours: its time is not scheduled and
// does not lower availability, while IDLE, DOWN and SETUP do.
func TestBuildBuckets_OffIsNotScheduled(t *testing.T) {
	s := OEEStream{
		IdealCycleSeconds: decimal.NewFromInt(60),
		Initial:           MachineRunning,
		Events: []MachineEvent{
			{Seq: 1, Kind: EventState, At: minutes(5), State: MachineSetup},
			{Seq: 2, Kind: EventState, At: minutes(10), State: MachineOff},
		},
	}
	got, err := BuildBuckets(s, floorNow, minutes(30), 15*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	// 0-5 RUNNING, 5-10 SETUP, 10-30 OFF: one bucket with 10 planned and 5 run minutes.
	if len(got) != 1 || !got[0].PlannedMinutes.Equal(decimal.NewFromInt(10)) || !got[0].RunMinutes.Equal(decimal.NewFromInt(5)) {
		t.Fatalf("buckets %+v", got)
	}
	if !got[0].Availability.Equal(decimal.RequireFromString("0.5")) {
		t.Fatalf("availability %s", got[0].Availability)
	}
}

// BR-25.5: downtime coded as planned leaves planned time, so it lowers neither
// availability nor the bucket's planned minutes; unplanned downtime does.
func TestBuildBuckets_PlannedDowntimeLeavesPlannedTime(t *testing.T) {
	s := OEEStream{
		IdealCycleSeconds: decimal.NewFromInt(60),
		Initial:           MachineRunning,
		Events: []MachineEvent{
			{Seq: 1, Kind: EventState, At: minutes(30), State: MachineDown},
			{Seq: 2, Kind: EventState, At: minutes(50), State: MachineRunning},
		},
		// 30-40 planned (changeover), 40-50 unplanned.
		PlannedDown: []Interval{{Start: minutes(30), End: minutes(40)}},
	}
	got, err := BuildBuckets(s, floorNow, minutes(60), 60*time.Minute)
	if err != nil || len(got) != 1 {
		t.Fatalf("buckets %+v, %v", got, err)
	}
	b := got[0]
	if !b.PlannedMinutes.Equal(decimal.NewFromInt(50)) || !b.RunMinutes.Equal(decimal.NewFromInt(40)) || !b.Availability.Equal(decimal.RequireFromString("0.8")) {
		t.Fatalf("planned %s run %s A %s", b.PlannedMinutes, b.RunMinutes, b.Availability)
	}
	// A bucket that is wholly planned downtime has no planned time and is omitted.
	all := OEEStream{IdealCycleSeconds: decimal.NewFromInt(60), Initial: MachineDown, PlannedDown: []Interval{{Start: floorNow, End: minutes(15)}}}
	if got, err := BuildBuckets(all, floorNow, minutes(15), 15*time.Minute); err != nil || len(got) != 0 {
		t.Fatalf("planned-only bucket: %+v, %v", got, err)
	}
}
