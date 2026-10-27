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

func count(seq int64, m int) MachineEvent {
	return MachineEvent{Seq: seq, Kind: EventCount, At: minutes(m), GoodCount: 1}
}

// BR-25.6: a RUNNING stretch with no count for longer than the idle threshold is
// IDLE for the whole gap, from the last count (or the start of RUNNING) to the
// next count or state change; a later count closes the gap.
func TestBuildBuckets_IdleDetection(t *testing.T) {
	cases := []struct {
		name        string
		initial     string
		historyFrom int
		horizon     *int
		events      []MachineEvent
		planned     int64
		run         int64
	}{
		{name: "a gap over the threshold is idle from the last count to the next", initial: MachineRunning,
			events: []MachineEvent{count(1, 2), count(2, 12), count(3, 14)}, planned: 15, run: 5},
		{name: "a gap of exactly the threshold stays running", initial: MachineRunning,
			events: []MachineEvent{count(1, 5), count(2, 10), count(3, 15)}, planned: 15, run: 15},
		{name: "no count until the horizon idles the whole window", initial: MachineRunning,
			horizon: ptrInt(20), planned: 15, run: 0},
		{name: "a short tail before the horizon stays running", initial: MachineRunning,
			events: []MachineEvent{count(1, 4), count(2, 8), count(3, 12)}, horizon: ptrInt(16), planned: 15, run: 15},
		{name: "a count in the look-back starts the gap before the window", initial: MachineRunning, historyFrom: -5,
			events: []MachineEvent{count(1, -1), count(2, 3)}, planned: 15, run: 3},
		{name: "without a look-back count the gap starts at the history start", initial: MachineRunning, historyFrom: -5,
			events: []MachineEvent{count(1, 3)}, planned: 15, run: 0},
		{name: "RUNNING in the look-back after DOWN starts the gap at the state change", initial: MachineDown, historyFrom: -5,
			events:  []MachineEvent{{Seq: 1, Kind: EventState, At: minutes(-2), State: MachineRunning}, count(2, 2), count(3, 6), count(4, 10), count(5, 15)},
			planned: 15, run: 15},
		{name: "RUNNING after DOWN counts the gap from the state change", initial: MachineDown,
			events: []MachineEvent{{Seq: 1, Kind: EventState, At: minutes(10), State: MachineRunning}, count(2, 14)}, planned: 15, run: 5},
		{name: "a stop ends the gap: RUNNING without counts before DOWN is idle", initial: MachineRunning,
			events: []MachineEvent{{Seq: 1, Kind: EventState, At: minutes(8), State: MachineDown}}, planned: 15, run: 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s := OEEStream{IdealCycleSeconds: decimal.NewFromInt(60), IdleThreshold: 5 * time.Minute, Initial: c.initial, Events: c.events}
			if c.historyFrom != 0 {
				s.HistoryFrom = minutes(c.historyFrom)
			}
			if c.horizon != nil {
				s.Horizon = minutes(*c.horizon)
			}
			got, err := BuildBuckets(s, floorNow, minutes(15), 15*time.Minute)
			if err != nil || len(got) != 1 {
				t.Fatalf("buckets %+v, %v", got, err)
			}
			if !got[0].PlannedMinutes.Equal(decimal.NewFromInt(c.planned)) || !got[0].RunMinutes.Equal(decimal.NewFromInt(c.run)) {
				t.Fatalf("planned %s run %s, want %d and %d", got[0].PlannedMinutes, got[0].RunMinutes, c.planned, c.run)
			}
			// Look-back events are not counted in the window.
			inWindow := 0
			for _, e := range c.events {
				if !e.At.Before(floorNow) && e.At.Before(minutes(15)) {
					inWindow++
				}
			}
			if got[0].SourceEvents != inWindow {
				t.Fatalf("source events %d, want %d", got[0].SourceEvents, inWindow)
			}
		})
	}
}

func ptrInt(n int) *int { return &n }

// Without a threshold RUNNING stays run time however sparse the counts are.
func TestBuildBuckets_IdleDetectionOffWithoutThreshold(t *testing.T) {
	got, err := BuildBuckets(OEEStream{IdealCycleSeconds: decimal.NewFromInt(60), Initial: MachineRunning, Horizon: minutes(90)},
		floorNow, minutes(15), 15*time.Minute)
	if err != nil || len(got) != 1 || !got[0].RunMinutes.Equal(decimal.NewFromInt(15)) {
		t.Fatalf("buckets %+v, %v", got, err)
	}
}

// A horizon inside the window (the computation runs before the window ends)
// leaves the time after it unscheduled instead of extending the last state.
func TestBuildBuckets_HorizonInsideWindowIsNotScheduled(t *testing.T) {
	got, err := BuildBuckets(OEEStream{IdealCycleSeconds: decimal.NewFromInt(60), Initial: MachineRunning, Horizon: minutes(20)},
		floorNow, minutes(60), 15*time.Minute)
	if err != nil || len(got) != 2 {
		t.Fatalf("buckets %+v, %v", got, err)
	}
	if !got[1].PlannedMinutes.Equal(decimal.NewFromInt(5)) || !got[1].RunMinutes.Equal(decimal.NewFromInt(5)) {
		t.Fatalf("partial bucket %+v", got[1])
	}
}

// Property: idle detection only moves time from run to downtime. Planned time
// and counts are unchanged and run time never grows.
func TestBuildBuckets_PropertyIdleOnlyLowersRun(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		const window = 120
		var events []MachineEvent
		at := 0
		for i := 0; i < rapid.IntRange(0, 12).Draw(t, "events"); i++ {
			at += rapid.IntRange(1, 15).Draw(t, "step")
			if at >= window {
				break
			}
			if rapid.Bool().Draw(t, "isCount") {
				events = append(events, count(int64(i+1), at))
			} else {
				events = append(events, MachineEvent{Seq: int64(i + 1), Kind: EventState, At: minutes(at),
					State: rapid.SampledFrom([]string{MachineRunning, MachineDown, MachineIdle}).Draw(t, "state")})
			}
		}
		threshold := time.Duration(rapid.IntRange(1, 30).Draw(t, "threshold")) * time.Minute
		base := OEEStream{IdealCycleSeconds: decimal.NewFromInt(1), Initial: MachineRunning, Events: events}
		idle := base
		idle.IdleThreshold = threshold
		plain, err := BuildBuckets(base, floorNow, minutes(window), 15*time.Minute)
		if err != nil {
			t.Fatal(err)
		}
		withIdle, err := BuildBuckets(idle, floorNow, minutes(window), 15*time.Minute)
		if err != nil {
			t.Fatal(err)
		}
		if len(plain) != len(withIdle) {
			t.Fatalf("bucket count %d vs %d", len(plain), len(withIdle))
		}
		for i := range plain {
			p, q := plain[i], withIdle[i]
			if !p.PlannedMinutes.Equal(q.PlannedMinutes) || p.TotalCount != q.TotalCount || q.RunMinutes.GreaterThan(p.RunMinutes) {
				t.Fatalf("bucket %d: plain %+v idle %+v", i, p, q)
			}
		}
	})
}
