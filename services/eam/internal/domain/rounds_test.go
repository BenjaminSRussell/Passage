package domain

import (
	"errors"
	"strings"
	"testing"
	"time"

	"pgregory.net/rapid"
)

func stops(n int) []RoundStop {
	out := make([]RoundStop, n)
	for i := range out {
		out[i] = RoundStop{Sequence: i + 1, AssetID: "a", JobType: "INSPECT", TravelMinutes: 5}
	}
	return out
}

func TestValidateRoundStops_Table(t *testing.T) {
	outOfOrder := stops(2)
	outOfOrder[0].Sequence = 2
	gap := stops(3)
	gap[2].Sequence = 4
	noAsset := stops(1)
	noAsset[0].AssetID = ""
	freeText := stops(1)
	freeText[0].JobType = "check the pump"
	longTravel := stops(2)
	longTravel[1].TravelMinutes = MaxRoundTravelMinutes + 1
	negative := stops(2)
	negative[1].TravelMinutes = -1
	cases := []struct {
		name  string
		stops []RoundStop
		want  string // substring of the detail; "" means valid
	}{
		{"six stops", stops(6), ""},
		{"fifty stops", stops(MaxRoundStops), ""},
		{"no stops", nil, "1 to 50 stops"},
		{"too many stops", stops(MaxRoundStops + 1), "1 to 50 stops"},
		{"out of order", outOfOrder, "numbered 1 to 2"},
		{"gap in numbering", gap, "numbered 1 to 3"},
		{"missing asset", noAsset, "needs an asset"},
		{"job type not a code", freeText, "job type code"},
		{"travel over limit", longTravel, "travel must be 0 to 480"},
		{"negative travel", negative, "travel must be 0 to 480"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := ValidateRoundStops(c.stops)
			if c.want == "" {
				if err != nil {
					t.Fatalf("want valid, got %v", err)
				}
				return
			}
			var ge GateError
			if !errors.As(err, &ge) || ge.Code != CodeRoundDefinition || !strings.Contains(ge.Detail, c.want) {
				t.Fatalf("got %v, want %s containing %q", err, CodeRoundDefinition, c.want)
			}
		})
	}
}

func TestRoundTravelMinutes(t *testing.T) {
	s := stops(4)
	s[0].TravelMinutes = 0
	if got := RoundTravelMinutes(s); got != 15 {
		t.Fatalf("travel = %d, want 15", got)
	}
}

func TestNormalizeStopReason_Table(t *testing.T) {
	cases := []struct {
		outcome, reason, want string
		ok                    bool
	}{
		{StopDone, "", "", true},
		{StopDone, "  all good ", "all good", true},
		{StopSkipped, " pump locked out ", "pump locked out", true},
		{StopSkipped, "   ", "", false},
		{StopSkipped, "", "", false},
		{StopSkipped, strings.Repeat("x", 121), "", false},
		{StopSkipped, strings.Repeat("é", 120), strings.Repeat("é", 120), true},
		{"PARTIAL", "x", "", false},
		{"", "x", "", false},
	}
	for _, c := range cases {
		got, err := NormalizeStopReason(c.outcome, c.reason)
		if (err == nil) != c.ok || got != c.want {
			t.Errorf("NormalizeStopReason(%q, %q) = %q, %v; want %q ok=%v", c.outcome, c.reason, got, err, c.want, c.ok)
		}
		var ge GateError
		if err != nil && (!errors.As(err, &ge) || ge.Code != CodeStopOutcome) {
			t.Errorf("error %v is not %s", err, CodeStopOutcome)
		}
	}
}

func TestFollowUpRequired_Table(t *testing.T) {
	cases := []struct {
		mandatory bool
		outcome   string
		want      bool
	}{
		{true, StopSkipped, true},
		{false, StopSkipped, false},
		{true, StopDone, false},
		{false, StopDone, false},
	}
	for _, c := range cases {
		if got := FollowUpRequired(c.mandatory, c.outcome); got != c.want {
			t.Errorf("FollowUpRequired(%v, %s) = %v, want %v", c.mandatory, c.outcome, got, c.want)
		}
	}
}

func TestRoundLineSchedulesOnTime(t *testing.T) {
	last := time.Date(2026, 10, 14, 9, 0, 0, 0, time.UTC)
	s, err := Compute(PlanLine{Kind: KindRound, Interval: Interval{N: 1, Unit: "WEEKS"}, Start: last.AddDate(0, -1, 0),
		LastCompletedAt: &last}, CounterState{}, last)
	if err != nil {
		t.Fatal(err)
	}
	if s.NextDueAt == nil || !s.NextDueAt.Equal(last.AddDate(0, 0, 7)) || s.NextDueReading != nil {
		t.Fatalf("round line due = %v reading %v, want one week after the last completion", s.NextDueAt, s.NextDueReading)
	}
}

func TestRegularizationOverdue_Table(t *testing.T) {
	created := time.Date(2026, 10, 1, 8, 0, 0, 0, time.UTC)
	cases := []struct {
		name     string
		priority int
		status   Status
		planned  bool
		at       time.Time
		want     bool
	}{
		{"within window", 1, StatusCreated, false, created.Add(23*time.Hour + 59*time.Minute), false},
		{"at window", 1, StatusCreated, false, created.Add(24 * time.Hour), true},
		{"started without release", 1, StatusInProgress, false, created.Add(48 * time.Hour), true},
		{"on hold, never released", 1, StatusOnHold, false, created.Add(48 * time.Hour), true},
		{"released then started", 1, StatusInProgress, true, created.Add(48 * time.Hour), false},
		{"scheduled", 1, StatusScheduled, true, created.Add(48 * time.Hour), false},
		{"priority 2 ignored", 2, StatusCreated, false, created.Add(48 * time.Hour), false},
		{"completed ignored", 1, StatusCompleted, false, created.Add(48 * time.Hour), false},
		{"canceled ignored", 1, StatusCanceled, false, created.Add(48 * time.Hour), false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := RegularizationOverdue(c.priority, c.status, c.planned, created, c.at, RegularizationWindow); got != c.want {
				t.Fatalf("got %v, want %v", got, c.want)
			}
		})
	}
}

func TestReviewDue_Table(t *testing.T) {
	now := time.Date(2026, 10, 21, 0, 0, 0, 0, time.UTC)
	fresh := now.AddDate(0, -3, 0)
	stale := now.AddDate(-1, -1, 0)
	edge := now.AddDate(-1, 0, 0)
	justInside := edge.Add(time.Second)
	cases := []struct {
		name string
		crit int
		last *time.Time
		want bool
	}{
		{"criticality 3 never due", 3, nil, false},
		{"never reviewed", 4, nil, true},
		{"recent review", 5, &fresh, false},
		{"older than a year", 4, &stale, true},
		{"exactly a year", 5, &edge, true},
		{"a second short of a year", 5, &justInside, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := ReviewDue(c.crit, c.last, now); got != c.want {
				t.Fatalf("got %v, want %v", got, c.want)
			}
		})
	}
}

// Once an order is overdue it stays overdue while nothing about it changes.
func TestRegularizationOverdue_MonotoneInTime(t *testing.T) {
	rapid.Check(t, func(rt *rapid.T) {
		created := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
		a := rapid.Int64Range(0, 90*24).Draw(rt, "a")
		b := rapid.Int64Range(0, 90*24).Draw(rt, "b")
		if a > b {
			a, b = b, a
		}
		status := rapid.SampledFrom([]Status{StatusCreated, StatusInProgress, StatusOnHold, StatusReleased}).Draw(rt, "status")
		planned := rapid.Bool().Draw(rt, "planned")
		window := time.Duration(rapid.IntRange(1, 72).Draw(rt, "hours")) * time.Hour
		early := RegularizationOverdue(1, status, planned, created, created.Add(time.Duration(a)*time.Hour), window)
		late := RegularizationOverdue(1, status, planned, created, created.Add(time.Duration(b)*time.Hour), window)
		if early && !late {
			rt.Fatalf("overdue at %dh but not at %dh", a, b)
		}
		if planned && late {
			rt.Fatalf("a planned order is never overdue")
		}
	})
}

func TestReviewDue_MonotoneInTime(t *testing.T) {
	rapid.Check(t, func(rt *rapid.T) {
		last := time.Date(2025, 6, 1, 0, 0, 0, 0, time.UTC)
		crit := rapid.IntRange(1, 5).Draw(rt, "crit")
		a := rapid.IntRange(0, 800).Draw(rt, "days-a")
		b := rapid.IntRange(0, 800).Draw(rt, "days-b")
		if a > b {
			a, b = b, a
		}
		early := ReviewDue(crit, &last, last.AddDate(0, 0, a))
		late := ReviewDue(crit, &last, last.AddDate(0, 0, b))
		if early && !late {
			rt.Fatalf("review due at day %d but not at day %d", a, b)
		}
	})
}
