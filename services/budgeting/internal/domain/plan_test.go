package domain

import (
	"testing"

	"github.com/shopspring/decimal"
	"pgregory.net/rapid"
)

func dec(s string) decimal.Decimal { return decimal.RequireFromString(s) }

func sum(parts []decimal.Decimal) decimal.Decimal {
	total := decimal.Zero
	for _, p := range parts {
		total = total.Add(p)
	}
	return total
}

func TestSpreadEven_RemainderOnFirst(t *testing.T) {
	got := SpreadEven(dec("100.0000"), 3)
	want := []string{"33.3334", "33.3333", "33.3333"}
	for i, w := range want {
		if !got[i].Equal(dec(w)) {
			t.Fatalf("share %d = %s, want %s", i, got[i], w)
		}
	}
}

func TestSpreadWeighted_ByPriorYearActuals(t *testing.T) {
	got := SpreadWeighted(dec("1200.00"), []decimal.Decimal{dec("100"), dec("300"), dec("600")})
	for i, w := range []string{"120", "360", "720"} {
		if !got[i].Equal(dec(w)) {
			t.Fatalf("share %d = %s, want %s", i, got[i], w)
		}
	}
}

func TestSpreadWeighted_ZeroWeightsFallBackToEven(t *testing.T) {
	got := SpreadWeighted(dec("12"), make([]decimal.Decimal, 12))
	for i, g := range got {
		if !g.Equal(dec("1")) {
			t.Fatalf("share %d = %s, want 1", i, g)
		}
	}
}

func TestSpreadWeighted_NegativeWeightsCountAsZero(t *testing.T) {
	got := SpreadWeighted(dec("90"), []decimal.Decimal{dec("-5"), dec("1"), dec("2")})
	if !got[0].IsZero() || !got[1].Equal(dec("30")) || !got[2].Equal(dec("60")) {
		t.Fatalf("shares = %v", got)
	}
}

func TestSpreadWeighted_SharesAlwaysAddUp(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		total := decimal.New(rapid.Int64Range(0, 1_000_000_000_000).Draw(t, "total"), -4)
		n := rapid.IntRange(1, 24).Draw(t, "n")
		weights := make([]decimal.Decimal, n)
		for i := range weights {
			weights[i] = decimal.New(rapid.Int64Range(0, 1_000_000).Draw(t, "w"), -2)
		}
		shares := SpreadWeighted(total, weights)
		if len(shares) != n || !sum(shares).Equal(total) {
			t.Fatalf("shares %v do not add up to %s", shares, total)
		}
		for _, s := range shares {
			if s.Sign() < 0 {
				t.Fatalf("negative share in %v", shares)
			}
		}
	})
}

func TestPeriods_AndPeriodInYear(t *testing.T) {
	p := Periods(2026)
	if len(p) != 12 || p[0] != "2026-01" || p[11] != "2026-12" {
		t.Fatalf("periods = %v", p)
	}
	for period, want := range map[string]bool{"2026-01": true, "2026-12": true, "2026-00": false, "2026-13": false, "2025-12": false, "2026-1": false, "2026-011": false} {
		if got := PeriodInYear(period, 2026); got != want {
			t.Errorf("PeriodInYear(%q) = %v, want %v", period, got, want)
		}
	}
}

func TestStageStatusMapping(t *testing.T) {
	if StageOpenStatus(StageReview) != PlanSubmitted || StageOpenStatus(StageApprove) != PlanReviewed {
		t.Fatal("stage open status")
	}
	if StageForStatus(PlanSubmitted) != StageReview || StageForStatus(PlanReviewed) != StageApprove || StageForStatus(PlanDraft) != "" || StageForStatus(PlanApproved) != "" {
		t.Fatal("stage for status")
	}
}

func TestRegisterLines_SkipsZeroCells(t *testing.T) {
	lines := RegisterLines([]PlanCell{
		{Account: "6100", CostCenter: "CC10", Period: "2026-01", Amount: dec("500")},
		{Account: "6100", CostCenter: "CC10", Period: "2026-02", Amount: dec("0")},
	})
	if len(lines) != 1 || lines[0].DimensionKey != "6100|CC10" || lines[0].SpanKey != "2026-01" || !lines[0].Amount.Equal(dec("500")) {
		t.Fatalf("lines = %+v", lines)
	}
}
