package domain

import (
	"errors"
	"math"
	"testing"
	"time"

	"github.com/shopspring/decimal"
	"pgregory.net/rapid"
)

func TestCheckReading(t *testing.T) {
	cases := []struct {
		name       string
		cumulative bool
		last, v    float64
		reset      bool
		wantCode   string
	}{
		{"monotonic increase", true, 12300, 12400, false, ""},
		{"equal is allowed", true, 12300, 12300, false, ""},
		{"decrease without reset", true, 12300, 12250, false, CodeNonMonotonic},
		{"decrease with reset", true, 12300, 10, true, ""},
		{"resettable counter takes any value", false, 12300, 10, false, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := CheckReading(tc.cumulative, tc.last, tc.v, tc.reset)
			if tc.wantCode == "" {
				if err != nil {
					t.Fatalf("unexpected error %v", err)
				}
				return
			}
			var re *ReadingError
			if !errors.As(err, &re) || re.Code != tc.wantCode {
				t.Fatalf("got %v, want code %s", err, tc.wantCode)
			}
		})
	}
}

func TestRollingUsage(t *testing.T) {
	d := func(day int) time.Time { return time.Date(2026, 9, day, 0, 0, 0, 0, time.UTC) }
	cases := []struct {
		name   string
		points []UsagePoint
		want   float64
		ok     bool
	}{
		{"steady 6.5 a day over 30 days", []UsagePoint{{At: d(1), Value: 12105}, {At: d(16), Value: 12200}, {At: time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC), Value: 12300}}, 6.5, true},
		{"a reset counts from zero", []UsagePoint{{At: d(1), Value: 900}, {At: d(3), Value: 20, Reset: true}, {At: d(5), Value: 60}}, (20.0 + 40) / 4, true},
		{"a drop on a resettable counter counts from zero", []UsagePoint{{At: d(1), Value: 50}, {At: d(2), Value: 10}}, 10, true},
		{"one reading has no rate", []UsagePoint{{At: d(1), Value: 50}}, 0, false},
		{"readings at the same instant have no rate", []UsagePoint{{At: d(1), Value: 50}, {At: d(1), Value: 60}}, 0, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := RollingUsage(tc.points)
			if ok != tc.ok || math.Abs(got-tc.want) > 1e-9 {
				t.Fatalf("usage = %v, %v; want %v, %v", got, ok, tc.want, tc.ok)
			}
		})
	}
}

// Property: usage over non-decreasing readings is never negative and equals
// (last - first) / days.
func TestRollingUsage_Property(t *testing.T) {
	rapid.Check(t, func(rt *rapid.T) {
		n := rapid.IntRange(2, 20).Draw(rt, "n")
		at := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
		v := rapid.Float64Range(0, 1000).Draw(rt, "start")
		points := []UsagePoint{{At: at, Value: v}}
		for i := 1; i < n; i++ {
			at = at.Add(time.Duration(rapid.IntRange(1, 72).Draw(rt, "gap")) * time.Hour)
			v += rapid.Float64Range(0, 100).Draw(rt, "inc")
			points = append(points, UsagePoint{At: at, Value: v})
		}
		got, ok := RollingUsage(points)
		days := points[n-1].At.Sub(points[0].At).Hours() / 24
		want := (points[n-1].Value - points[0].Value) / days
		if !ok || got < 0 || math.Abs(got-want) > 1e-6 {
			rt.Fatalf("usage = %v ok=%v, want %v", got, ok, want)
		}
	})
}

func TestCanTransition(t *testing.T) {
	cases := []struct {
		from, to  Status
		emergency bool
		want      bool
	}{
		{StatusCreated, StatusScheduled, false, true},
		{StatusCreated, StatusInProgress, false, false},
		{StatusCreated, StatusInProgress, true, true},
		{StatusReleased, StatusInProgress, false, true},
		{StatusInProgress, StatusOnHold, false, true},
		{StatusOnHold, StatusCompleted, false, false},
		{StatusCompleted, StatusClosed, false, true},
		{StatusCompleted, StatusInProgress, false, false},
		{StatusClosed, StatusCanceled, false, false},
		{StatusScheduled, StatusCanceled, false, true},
		{StatusScheduled, StatusInProgress, false, false},
		{StatusScheduled, StatusInProgress, true, true},
		{StatusOnHold, StatusCanceled, false, true},
		{StatusCompleted, StatusCanceled, false, false},
	}
	for _, tc := range cases {
		if got := CanTransition(tc.from, tc.to, tc.emergency); got != tc.want {
			t.Errorf("%s→%s emergency=%v: got %v want %v", tc.from, tc.to, tc.emergency, got, tc.want)
		}
	}
}

func TestValidCompletion(t *testing.T) {
	a := time.Date(2026, 1, 2, 8, 0, 0, 0, time.UTC)
	b := a.Add(time.Hour)
	if err := ValidCompletion(&a, &b); err != nil {
		t.Fatal(err)
	}
	if err := ValidCompletion(&b, &a); err == nil {
		t.Fatal("end before start must be rejected")
	}
	if err := ValidCompletion(nil, &a); err == nil {
		t.Fatal("missing start must be rejected")
	}
}

func TestDefaultPriority(t *testing.T) {
	if got := DefaultPriority(5, true); got != 1 {
		t.Fatalf("criticality 5 corrective priority = %d, want 1", got)
	}
	if got := DefaultPriority(1, true); got != 5 {
		t.Fatalf("criticality 1 corrective priority = %d, want 5", got)
	}
	if got := DefaultPriority(5, false); got != 3 {
		t.Fatalf("preventive priority = %d, want 3", got)
	}
	for _, tc := range []struct {
		typ  string
		crit int
		want int
	}{{TypeEmergency, 1, 1}, {TypeCorrective, 2, 4}, {TypePreventive, 5, 3}, {TypeInspection, 1, 3}} {
		if got := DefaultTypePriority(tc.typ, tc.crit); got != tc.want {
			t.Errorf("%s at criticality %d: priority %d, want %d", tc.typ, tc.crit, got, tc.want)
		}
	}
}

// AC-44.3: 350.00 parts plus 8 h at 45.00 is 710.00.
func TestCosts_AC443(t *testing.T) {
	tot := Totals{
		Parts: PartCost(decimal.NewFromInt(7), decimal.RequireFromString("50.00")),
		Labor: LaborCost(decimal.NewFromInt(8), decimal.RequireFromString("45.00")),
	}
	if got := tot.Total().StringFixed(2); got != "710.00" {
		t.Fatalf("total = %s, want 710.00", got)
	}
}

// AC-44.8: 3 failures and 2,700 h uptime give MTBF 900 h.
func TestReliability_AC448(t *testing.T) {
	k := Reliability(2700+4, 4, []float64{2, 3, 5})
	if k.Failures != 3 || math.Abs(k.MTBFHours-900) > 1e-9 {
		t.Fatalf("kpi = %+v, want 3 failures and MTBF 900", k)
	}
	if math.Abs(k.MTTRHours-10.0/3) > 1e-9 {
		t.Fatalf("MTTR = %v", k.MTTRHours)
	}
	if none := Reliability(100, 0, nil); none.MTBFHours != 0 || none.Availability != 1 {
		t.Fatalf("no failures: %+v", none)
	}
}

// AC-44.4: a bearing temperature rule of > 85 breaches at 87 and not at 85.
func TestBreached_AC444(t *testing.T) {
	if !Breached(">", 85, 87) || Breached(">", 85, 85) || !Breached("<", 10, 9) {
		t.Fatal("threshold operators misbehave")
	}
	if Breached("=", 1, 1) {
		t.Fatal("unknown operator must never breach")
	}
}

// AC-44.4: while a request is open, or within the cooldown, no request is created.
func TestShouldRaise_Dedupe(t *testing.T) {
	fired := time.Date(2026, 10, 21, 9, 0, 0, 0, time.UTC)
	if ShouldRaise(true, nil, fired, DefaultCooldown) {
		t.Error("an open request must suppress a new one")
	}
	if ShouldRaise(false, &fired, fired.Add(59*time.Minute), DefaultCooldown) {
		t.Error("a breach inside the cooldown must be suppressed")
	}
	if !ShouldRaise(false, &fired, fired.Add(61*time.Minute), DefaultCooldown) {
		t.Error("a breach after the cooldown must raise")
	}
	if DedupeKey("a", "r") != DedupeKey("a", "r") || DedupeKey("a", "r") == DedupeKey("a", "s") {
		t.Error("dedupe key must be stable per (asset, rule)")
	}
}

// Property: a stricter counter never reads "valid" when it moves backwards.
func TestCheckReading_Property(t *testing.T) {
	rapid.Check(t, func(rt *rapid.T) {
		last := rapid.Float64Range(0, 1e6).Draw(rt, "last")
		v := rapid.Float64Range(0, 1e6).Draw(rt, "v")
		err := CheckReading(true, last, v, false)
		if (v < last) != (err != nil) {
			rt.Fatalf("last=%v v=%v err=%v", last, v, err)
		}
	})
}
