package domain

import (
	"testing"
	"time"

	"github.com/shopspring/decimal"
	"pgregory.net/rapid"
)

func TestApplyMappings_LatestEffectiveMappingWins(t *testing.T) {
	d := func(day int) time.Time { return time.Date(2026, 3, day, 0, 0, 0, 0, time.UTC) }
	mappings := []AccountMapping{
		{Source: "1000", Target: "1100", EffectiveFrom: d(1)},
		{Source: "1000", Target: "1200", EffectiveFrom: d(10)},
		{Source: "2000", Target: "2100", EffectiveFrom: d(20)},
	}
	lines := []LineInput{line("1000", SideDebit, "5.00"), line("2000", SideCredit, "5.00")}
	for _, tc := range []struct {
		name string
		date time.Time
		want [2]string
	}{
		{"before any mapping", time.Date(2026, 2, 28, 0, 0, 0, 0, time.UTC), [2]string{"1000", "2000"}},
		{"first mapping", d(5), [2]string{"1100", "2000"}},
		{"superseded", d(10), [2]string{"1200", "2000"}},
		{"second source", d(25), [2]string{"1200", "2100"}},
	} {
		got := ApplyMappings(lines, mappings, tc.date)
		if got[0].Account != tc.want[0] || got[1].Account != tc.want[1] {
			t.Errorf("%s: accounts %s, %s; want %v", tc.name, got[0].Account, got[1].Account, tc.want)
		}
	}
	if lines[0].Account != "1000" {
		t.Fatal("the input lines must not be modified")
	}
}

func TestProperty_MappedEntriesStayBalanced(t *testing.T) {
	pd := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	rapid.Check(t, func(t *rapid.T) {
		lines := balancedEntry(t)
		targets := []string{"1000", "2000", "5000"}
		var mappings []AccountMapping
		for _, src := range []string{"1000", "2000"} {
			if rapid.Bool().Draw(t, "map "+src) {
				mappings = append(mappings, AccountMapping{Source: src, Target: rapid.SampledFrom(targets).Draw(t, "target "+src), EffectiveFrom: pd})
			}
		}
		mapped := ApplyMappings(lines, mappings, pd)
		debit, credit := decimal.Zero, decimal.Zero
		for _, l := range mapped {
			if l.Side == SideDebit {
				debit = debit.Add(l.Amount)
			} else {
				credit = credit.Add(l.Amount)
			}
		}
		if !debit.Equal(credit) || len(mapped) != len(lines) {
			t.Fatalf("mapping changed totals: debit %s credit %s", debit, credit)
		}
	})
}
