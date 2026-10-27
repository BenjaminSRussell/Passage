package domain

import (
	"fmt"
	"testing"
	"time"

	"github.com/shopspring/decimal"
	"pgregory.net/rapid"
)

var shift = time.Date(2026, 10, 20, 8, 0, 0, 0, time.UTC)

func startAt(id, job, bundle string, minutes int) OpenStart {
	return OpenStart{RegistrationID: id, JobID: job, BundleID: bundle, StartedAt: shift.Add(time.Duration(minutes) * time.Minute)}
}

func unitJobs(u StopUnit) string {
	out := ""
	for _, m := range u.Members {
		out += m.JobID
	}
	return out
}

func TestPlanAutoStop_FR25_3(t *testing.T) {
	plain := startAt("r1", "J1", "", 0)
	b1a, b1b := startAt("r2", "J2", "B1", 30), startAt("r3", "J3", "B1", 30)
	tests := []struct {
		name     string
		open     []OpenStart
		incoming OpenStart
		want     []string // jobs per stopped unit
		stopAt   time.Time
	}{
		{"nothing open", nil, startAt("n", "J9", "", 60), nil, time.Time{}},
		{"the previous job is stopped at the new start", []OpenStart{plain}, startAt("n", "J9", "", 60), []string{"J1"}, shift.Add(time.Hour)},
		{"an open bundle stops as one unit", []OpenStart{b1a, b1b}, startAt("n", "J9", "", 60), []string{"J2J3"}, shift.Add(time.Hour)},
		{"members of the incoming bundle stay open", []OpenStart{b1a}, startAt("n", "J3", "B1", 30), nil, time.Time{}},
		{"a new bundle stops the other bundle", []OpenStart{b1a, b1b}, startAt("n", "J9", "B2", 60), []string{"J2J3"}, shift.Add(time.Hour)},
		{"the incoming start itself is never stopped", []OpenStart{startAt("n", "J9", "", 60)}, startAt("n", "J9", "", 60), nil, time.Time{}},
		{"restarting the same job closes the earlier start", []OpenStart{plain}, startAt("n", "J1", "", 60), []string{"J1"}, shift.Add(time.Hour)},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := PlanAutoStop(tc.open, tc.incoming)
			if len(got) != len(tc.want) {
				t.Fatalf("stopped %d units, want %d: %+v", len(got), len(tc.want), got)
			}
			for i, u := range got {
				if unitJobs(u) != tc.want[i] || !u.StoppedAt.Equal(tc.stopAt) || u.Capped {
					t.Fatalf("unit %d = %s at %s (capped %v), want %s at %s", i, unitJobs(u), u.StoppedAt, u.Capped, tc.want[i], tc.stopAt)
				}
			}
		})
	}
}

// BR-25.7: an older start replayed after a newer one is the stale one. It is
// closed at the newer start and the newer job stays open.
func TestPlanAutoStop_LateReplayClosesTheIncomingStart(t *testing.T) {
	newer := startAt("r1", "J1", "", 120)
	late := startAt("n", "J2", "", 60)
	got := PlanAutoStop([]OpenStart{newer, late}, late)
	if len(got) != 1 || unitJobs(got[0]) != "J2" || !got[0].StoppedAt.Equal(newer.StartedAt) {
		t.Fatalf("plan = %+v", got)
	}
}

func TestPlanAutoStop_CapsAtTwentyFourHours(t *testing.T) {
	forgotten := startAt("r1", "J1", "", 0)
	got := PlanAutoStop([]OpenStart{forgotten}, startAt("n", "J2", "", 30*60))
	if len(got) != 1 || !got[0].Capped || !got[0].StoppedAt.Equal(shift.Add(MaxJobHours)) {
		t.Fatalf("plan = %+v", got)
	}
	h, err := got[0].Hours()
	if err != nil || h[0].String() != "24" {
		t.Fatalf("hours = %v, %v", h, err)
	}
}

// BR-25.1 on an auto-stopped bundle: the time from the earliest start is split by
// the good quantity on the start registrations, or equally when none is reported.
func TestStopUnitHours_BR25_1(t *testing.T) {
	a, b := startAt("r1", "J1", "B1", 0), startAt("r2", "J2", "B1", 0)
	equal := StopUnit{BundleID: "B1", Members: []OpenStart{a, b}, StoppedAt: shift.Add(3 * time.Hour)}
	if h, err := equal.Hours(); err != nil || h[0].String() != "1.5" || h[1].String() != "1.5" {
		t.Fatalf("equal split = %v, %v", h, err)
	}
	a.QtyGood, b.QtyGood = decimal.NewFromInt(3), decimal.NewFromInt(1)
	byQty := StopUnit{BundleID: "B1", Members: []OpenStart{a, b}, StoppedAt: shift.Add(2 * time.Hour)}
	if h, err := byQty.Hours(); err != nil || h[0].String() != "1.5" || h[1].String() != "0.5" {
		t.Fatalf("quantity split = %v, %v", h, err)
	}
	single := StopUnit{Members: []OpenStart{startAt("r3", "J3", "", 15)}, StoppedAt: shift.Add(time.Hour)}
	if h, err := single.Hours(); err != nil || h[0].String() != "0.75" {
		t.Fatalf("single job = %v, %v", h, err)
	}
}

// Property (PRD invariant "at most one open job-start without job-stop" per
// worker): applying the plan to any set of open starts leaves the starts of one
// unit open, never two, and every stop is at or after the start it closes.
func TestPlanAutoStop_LeavesOneOpenUnitProperty(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		n := rapid.IntRange(0, 6).Draw(t, "open")
		var open []OpenStart
		for i := 0; i < n; i++ {
			open = append(open, startAt(fmt.Sprintf("r%d", i), fmt.Sprintf("J%d", i),
				rapid.SampledFrom([]string{"", "B1", "B2"}).Draw(t, "bundle"), rapid.IntRange(0, 600).Draw(t, "minute")))
		}
		// The worker held one open unit before; normalize by applying the plan once per start.
		var held []OpenStart
		for _, s := range open {
			held = apply(append(held, s), s)
		}
		incoming := startAt("new", "JN", rapid.SampledFrom([]string{"", "B1", "B2"}).Draw(t, "incoming"), rapid.IntRange(0, 2000).Draw(t, "at"))
		after := apply(append(held, incoming), incoming)
		units := map[string]bool{}
		for _, s := range after {
			key := s.BundleID
			if key == "" {
				key = s.RegistrationID
			}
			units[key] = true
		}
		if len(units) > 1 {
			t.Fatalf("open units after %+v: %+v", incoming, after)
		}
	})
}

// apply removes the members of the planned stop units from open, checking each stop time.
func apply(open []OpenStart, incoming OpenStart) []OpenStart {
	stopped := map[string]bool{}
	for _, u := range PlanAutoStop(open, incoming) {
		for _, m := range u.Members {
			if u.StoppedAt.Before(m.StartedAt) {
				panic(fmt.Sprintf("stop %s before start %s", u.StoppedAt, m.StartedAt))
			}
			stopped[m.RegistrationID] = true
		}
	}
	var out []OpenStart
	for _, s := range open {
		if !stopped[s.RegistrationID] {
			out = append(out, s)
		}
	}
	return out
}
