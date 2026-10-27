package domain

import (
	"errors"
	"testing"

	"github.com/shopspring/decimal"
	"pgregory.net/rapid"
)

// BR-25.5 shift example: planned 480 min, 60 min unplanned down, ideal cycle 60 s,
// 380 total, 361 good. A = 0.8750, P = 0.9048, Q = 0.9500.
//
// The business rule text says OEE = 0.7522. The formula gives 0.875 x 0.904762 x
// 0.95 = 0.752083, which rounds to 0.7521 at four places (the rounded factors
// give 0.7521 as well). The test asserts the formula; the discrepancy is logged
// in NOT-IMPLEMENTED.md for the requirements owner.
func TestCompute_BR25_5_ShiftExample(t *testing.T) {
	got, err := Compute(OEEInput{
		ScheduledMinutes:     decimal.NewFromInt(480),
		UnplannedDownMinutes: decimal.NewFromInt(60),
		IdealCycleSeconds:    decimal.NewFromInt(60),
		TotalCount:           380,
		GoodCount:            361,
	})
	if err != nil {
		t.Fatal(err)
	}
	checks := map[string][2]string{
		"availability": {got.Availability.StringFixed(4), "0.8750"},
		"performance":  {got.Performance.StringFixed(4), "0.9048"},
		"quality":      {got.Quality.StringFixed(4), "0.9500"},
		"oee":          {got.OEE.StringFixed(4), "0.7521"},
	}
	for name, c := range checks {
		if c[0] != c[1] {
			t.Errorf("%s = %s want %s", name, c[0], c[1])
		}
	}
	if got.OverRate {
		t.Error("over-rate flag set for a 0.90 performance")
	}
}

func TestCompute_Cases(t *testing.T) {
	cases := []struct {
		name     string
		in       OEEInput
		oee      string
		overRate bool
		wantErr  bool
	}{
		{"no count and no run is zero performance, full quality", OEEInput{ScheduledMinutes: decimal.NewFromInt(60), UnplannedDownMinutes: decimal.NewFromInt(60), IdealCycleSeconds: decimal.NewFromInt(30)}, "0.0000", false, false},
		{"planned downtime leaves planned time", OEEInput{ScheduledMinutes: decimal.NewFromInt(480), PlannedDownMinutes: decimal.NewFromInt(480), IdealCycleSeconds: decimal.NewFromInt(1)}, "", false, true},
		{"performance above 1 is capped and flagged", OEEInput{ScheduledMinutes: decimal.NewFromInt(60), IdealCycleSeconds: decimal.NewFromInt(30), TotalCount: 200, GoodCount: 200}, "1.0000", true, false},
		{"good above total is refused", OEEInput{ScheduledMinutes: decimal.NewFromInt(60), IdealCycleSeconds: decimal.NewFromInt(30), TotalCount: 5, GoodCount: 6}, "", false, true},
		{"downtime above planned is refused", OEEInput{ScheduledMinutes: decimal.NewFromInt(60), UnplannedDownMinutes: decimal.NewFromInt(61), IdealCycleSeconds: decimal.NewFromInt(30)}, "", false, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := Compute(c.in)
			if c.wantErr {
				if !errors.Is(err, ErrOEEInput) {
					t.Fatalf("err = %v, want ErrOEEInput", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if c.oee != "" && got.OEE.StringFixed(4) != c.oee {
				t.Fatalf("oee = %s want %s", got.OEE.StringFixed(4), c.oee)
			}
			if got.OverRate != c.overRate {
				t.Fatalf("overRate = %v", got.OverRate)
			}
		})
	}
}

// Property: every factor is within [0, 1] and OEE never exceeds the smallest factor.
func TestCompute_Property_FactorsBounded(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		scheduled := rapid.IntRange(1, 1440).Draw(t, "scheduled")
		plannedDown := rapid.IntRange(0, scheduled).Draw(t, "plannedDown")
		down := rapid.IntRange(0, scheduled-plannedDown).Draw(t, "down")
		total := rapid.Int64Range(0, 100000).Draw(t, "total")
		good := rapid.Int64Range(0, total).Draw(t, "good")
		ideal := rapid.IntRange(1, 600).Draw(t, "ideal")
		got, err := Compute(OEEInput{
			ScheduledMinutes:     decimal.NewFromInt(int64(scheduled)),
			PlannedDownMinutes:   decimal.NewFromInt(int64(plannedDown)),
			UnplannedDownMinutes: decimal.NewFromInt(int64(down)),
			IdealCycleSeconds:    decimal.NewFromInt(int64(ideal)),
			TotalCount:           total,
			GoodCount:            good,
		})
		if plannedDown == scheduled {
			if err == nil {
				t.Fatal("zero planned time must be refused")
			}
			return
		}
		if err != nil {
			t.Fatal(err)
		}
		for name, f := range map[string]decimal.Decimal{"A": got.Availability, "P": got.Performance, "Q": got.Quality, "OEE": got.OEE} {
			if f.IsNegative() || f.GreaterThan(decimal.NewFromInt(1)) {
				t.Fatalf("%s = %s out of [0,1]", name, f)
			}
		}
		if got.OEE.GreaterThan(got.Availability) || got.OEE.GreaterThan(got.Quality) || got.OEE.GreaterThan(got.Performance) {
			t.Fatalf("OEE %s above a factor (A=%s P=%s Q=%s)", got.OEE, got.Availability, got.Performance, got.Quality)
		}
	})
}
