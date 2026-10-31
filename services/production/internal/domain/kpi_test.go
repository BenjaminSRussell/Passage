package domain

import (
	"testing"

	"github.com/shopspring/decimal"
	"pgregory.net/rapid"
)

func TestKPIGroup_Percentages(t *testing.T) {
	g := KPIGroup{Ended: 3, Dated: 3, OnTime: 2, Cycled: 2, CycleDaysSum: d("7.5"), QtyFinished: d("97"), QtyError: d("3")}
	if got := g.OnTimePct(); got == nil || !got.Equal(d("66.67")) {
		t.Fatalf("on time = %v", got)
	}
	if got := g.AvgCycleDays(); got == nil || !got.Equal(d("3.75")) {
		t.Fatalf("cycle = %v", got)
	}
	if got := g.ScrapPct(); got == nil || !got.Equal(d("3")) {
		t.Fatalf("scrap = %v", got)
	}
	var empty KPIGroup
	if empty.OnTimePct() != nil || empty.AvgCycleDays() != nil || empty.ScrapPct() != nil {
		t.Fatal("an empty group has no percentages")
	}
}

func TestKPIGroup_AddKeepsRatesInRange(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		mk := func(label string) KPIGroup {
			dated := rapid.IntRange(0, 50).Draw(t, label+"dated")
			return KPIGroup{Ended: dated + rapid.IntRange(0, 5).Draw(t, label+"undated"), Dated: dated, OnTime: rapid.IntRange(0, dated).Draw(t, label+"ontime"),
				QtyFinished: decimal.NewFromInt(int64(rapid.IntRange(0, 1000).Draw(t, label+"good"))),
				QtyError:    decimal.NewFromInt(int64(rapid.IntRange(0, 1000).Draw(t, label+"bad")))}
		}
		a, b := mk("a"), mk("b")
		sum := a
		sum.Add(b)
		if sum.Ended != a.Ended+b.Ended || sum.OnTime != a.OnTime+b.OnTime || !sum.QtyError.Equal(a.QtyError.Add(b.QtyError)) {
			t.Fatalf("sum = %+v", sum)
		}
		for _, p := range []*decimal.Decimal{sum.OnTimePct(), sum.ScrapPct()} {
			if p != nil && (p.IsNegative() || p.GreaterThan(d("100"))) {
				t.Fatalf("rate %s out of range for %+v", p, sum)
			}
		}
	})
}
