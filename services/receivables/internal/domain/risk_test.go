package domain

import (
	"testing"

	"github.com/shopspring/decimal"
	"pgregory.net/rapid"
)

func TestRiskScore(t *testing.T) {
	tests := []struct {
		name string
		in   RiskInputs
		want int
	}{
		{"clean customer", RiskInputs{OverdueAmount: amt("0"), TotalOpen: amt("1000")}, 0},
		{"nothing open", RiskInputs{}, 0},
		{"all overdue", RiskInputs{OverdueAmount: amt("500"), TotalOpen: amt("500")}, 40},
		{"half overdue, 30 days late", RiskInputs{OverdueAmount: amt("250"), TotalOpen: amt("500"), AvgDaysLate: 30}, 30},
		{"dunning level 4", RiskInputs{DunningLevel: 4}, 20},
		{"disputes capped at three", RiskInputs{OpenDisputes: 7}, 15},
		{"broken promises capped at two", RiskInputs{BrokenPromises: 9}, 5},
		{"worst case", RiskInputs{OverdueAmount: amt("10"), TotalOpen: amt("10"), AvgDaysLate: 999,
			DunningLevel: 9, OpenDisputes: 9, BrokenPromises: 9}, 100},
		{"negative inputs clamp to zero", RiskInputs{OverdueAmount: amt("-5"), TotalOpen: amt("10"), AvgDaysLate: -3,
			DunningLevel: -1, OpenDisputes: -2, BrokenPromises: -4}, 0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := RiskScore(tc.in); got != tc.want {
				t.Errorf("RiskScore() = %d, want %d", got, tc.want)
			}
		})
	}
}

func TestRiskScore_BoundedAndMonotoneInOverdue(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		total := rapid.Int64Range(1, 1_000_000).Draw(t, "total")
		over := rapid.Int64Range(0, total).Draw(t, "over")
		extra := rapid.Int64Range(0, total-over).Draw(t, "extra")
		days := rapid.IntRange(-10, 500).Draw(t, "days")
		level := rapid.IntRange(-2, 8).Draw(t, "level")
		disputes := rapid.IntRange(-2, 8).Draw(t, "disputes")
		promises := rapid.IntRange(-2, 8).Draw(t, "promises")

		base := RiskInputs{OverdueAmount: decimal.NewFromInt(over), TotalOpen: decimal.NewFromInt(total),
			AvgDaysLate: days, DunningLevel: level, OpenDisputes: disputes, BrokenPromises: promises}
		more := base
		more.OverdueAmount = decimal.NewFromInt(over + extra)

		a, b := RiskScore(base), RiskScore(more)
		if a < 0 || a > 100 || b < 0 || b > 100 {
			t.Fatalf("score out of bounds: %d, %d", a, b)
		}
		if b < a {
			t.Fatalf("more overdue lowered the score: %d -> %d", a, b)
		}
	})
}
