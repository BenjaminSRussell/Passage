package domain

import (
	"testing"

	"github.com/shopspring/decimal"
	"pgregory.net/rapid"
)

func TestComputeScorecardFixture(t *testing.T) {
	audit := dec("80")
	in := ScorecardInput{
		LotsInspected: 100, LotsAccepted: 96,
		UnitsInspected: dec("10000"), UnitsRejected: dec("25"),
		Deliveries: 50, OnTimeDeliveries: 45,
		NCRs:       map[string]int{NCRCritical: 1, NCRMajor: 2, NCRMinor: 3},
		AuditScore: &audit,
	}
	sc, err := ComputeScorecard(in, DefaultWeights())
	if err != nil {
		t.Fatal(err)
	}
	// AC-33.9: PPM = 25 / 10,000 × 10^6 = 2500.
	if !sc.PPM.Equal(dec("2500")) {
		t.Errorf("ppm = %s", sc.PPM)
	}
	if !sc.AcceptanceRate.Equal(dec("96")) || !sc.OnTimePct.Equal(dec("90")) || !sc.NCRScore.Equal(dec("90")) {
		t.Errorf("components: rate %s on-time %s ncr %s", sc.AcceptanceRate, sc.OnTimePct, sc.NCRScore)
	}
	// (40·96 + 30·90 + 20·90 + 10·80) / 100 = 91.40
	if !sc.Total.Equal(dec("91.4")) {
		t.Errorf("total = %s, want 91.4", sc.Total)
	}
}

func TestComputeScorecardRenormalisesMissingEvidence(t *testing.T) {
	// Only lot evidence: the quality component alone is the total.
	sc, err := ComputeScorecard(ScorecardInput{LotsInspected: 4, LotsAccepted: 3, UnitsInspected: dec("40"), UnitsRejected: dec("1")}, DefaultWeights())
	if err != nil {
		t.Fatal(err)
	}
	// quality 75 (w40) and NCR 100 (w20): (75·40 + 100·20) / 60 = 83.33
	if !sc.Total.Equal(dec("83.33")) {
		t.Errorf("total = %s, want 83.33", sc.Total)
	}
	if sc.OnTimePct != nil || sc.AuditScore != nil {
		t.Error("absent evidence must not be reported")
	}
}

func TestComputeScorecardRejects(t *testing.T) {
	cases := []struct {
		name string
		in   ScorecardInput
		w    Weights
	}{
		{"weights not 100", ScorecardInput{LotsInspected: 1, LotsAccepted: 1}, Weights{Quality: 50, Delivery: 30, NCR: 10}},
		{"negative weight", ScorecardInput{LotsInspected: 1, LotsAccepted: 1}, Weights{Quality: 110, Delivery: -10}},
		{"accepted above inspected", ScorecardInput{LotsInspected: 1, LotsAccepted: 2}, DefaultWeights()},
		{"on time above deliveries", ScorecardInput{Deliveries: 1, OnTimeDeliveries: 2}, DefaultWeights()},
		{"rejected above inspected", ScorecardInput{UnitsInspected: dec("1"), UnitsRejected: dec("2")}, DefaultWeights()},
		{"no evidence", ScorecardInput{}, DefaultWeights()},
	}
	for _, c := range cases {
		if _, err := ComputeScorecard(c.in, c.w); err == nil {
			t.Errorf("%s: expected an error", c.name)
		}
	}
}

func TestScorecardPropertiesHold(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		inspected := rapid.IntRange(1, 500).Draw(t, "inspected")
		accepted := rapid.IntRange(0, inspected).Draw(t, "accepted")
		deliveries := rapid.IntRange(0, 200).Draw(t, "deliveries")
		onTime := rapid.IntRange(0, deliveries).Draw(t, "onTime")
		units := rapid.IntRange(1, 100000).Draw(t, "units")
		rejected := rapid.IntRange(0, units).Draw(t, "rejected")
		crit := rapid.IntRange(0, 5).Draw(t, "crit")
		audit := decimal.NewFromInt(int64(rapid.IntRange(0, 100).Draw(t, "audit")))
		sc, err := ComputeScorecard(ScorecardInput{
			LotsInspected: inspected, LotsAccepted: accepted,
			UnitsInspected: decimal.NewFromInt(int64(units)), UnitsRejected: decimal.NewFromInt(int64(rejected)),
			Deliveries: deliveries, OnTimeDeliveries: onTime,
			NCRs: map[string]int{NCRCritical: crit}, AuditScore: &audit,
		}, DefaultWeights())
		if err != nil {
			t.Fatal(err)
		}
		if sc.Total.IsNegative() || sc.Total.GreaterThan(decimal.NewFromInt(100)) {
			t.Fatalf("total %s outside 0-100", sc.Total)
		}
		want := decimal.NewFromInt(int64(rejected)).Div(decimal.NewFromInt(int64(units))).Mul(decimal.NewFromInt(1000000))
		if !sc.PPM.Round(2).Equal(want.Round(2)) {
			t.Fatalf("ppm %s, want %s", sc.PPM, want.Round(2))
		}
	})
}
