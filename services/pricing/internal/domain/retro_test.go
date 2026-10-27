package domain

import (
	"testing"
	"time"

	"github.com/shopspring/decimal"
)

func retroDay(y, m, d int) time.Time { return time.Date(y, time.Month(m), d, 0, 0, 0, 0, time.UTC) }

func twoPercent() RebateAgreement {
	return RebateAgreement{Basis: BasisNetAmount, Period: PeriodMonth, Currency: "USD",
		Tiers: []Tier{{From: decimal.Zero, RatePct: dec("2")}}, ValidFrom: retroDay(2027, 3, 1), ValidTo: retroDay(2027, 12, 31)}
}

// AC-26.14: a 2% agreement activated 2027-03-20 with valid_from 2027-03-01 and 50,000.00 USD
// invoiced in between posts a 1,000.00 delta.
func TestRetro_AC26_14(t *testing.T) {
	a := twoPercent()
	activated := retroDay(2027, 3, 20)
	if !IsRetroactive(a, activated) {
		t.Fatal("activation after valid_from must be retroactive")
	}
	if IsRetroactive(a, retroDay(2027, 3, 1)) || IsRetroactive(a, retroDay(2027, 2, 28)) {
		t.Fatal("activation on or before valid_from is not retroactive")
	}
	d := RetroDeltas(a, []RetroPeriod{{Period: "2027-03", CumValue: dec("50000"), Posted: decimal.Zero}})
	if len(d) != 1 || d[0].Delta.StringFixed(2) != "1000.00" || d[0].Target.StringFixed(2) != "1000.00" {
		t.Fatalf("deltas = %+v, want one 1000.00 delta", d)
	}
}

func TestRetro_ScopeIsValidFromToActivation(t *testing.T) {
	a := twoPercent()
	activated := retroDay(2027, 3, 20)
	for _, c := range []struct {
		day  time.Time
		want bool
	}{
		{retroDay(2027, 2, 28), false},
		{retroDay(2027, 3, 1), true},
		{retroDay(2027, 3, 20), true},
		{retroDay(2027, 3, 21), false},
	} {
		if got := InRetroScope(a, activated, c.day); got != c.want {
			t.Errorf("InRetroScope(%s) = %v, want %v", c.day.Format("2006-01-02"), got, c.want)
		}
	}
	a.ValidTo = retroDay(2027, 3, 10)
	if InRetroScope(a, activated, retroDay(2027, 3, 15)) {
		t.Error("an invoice after valid_to is out of scope")
	}
}

// Only periods whose target moved post, in period order, and a second pass finds nothing.
func TestRetro_OnlyChangedPeriodsPostAndRerunIsEmpty(t *testing.T) {
	a := twoPercent()
	a.Tiers = []Tier{{From: decimal.Zero, RatePct: dec("1")}, {From: dec("10000"), RatePct: dec("2")}}
	periods := []RetroPeriod{
		{Period: "2027-05", CumValue: dec("12000"), Posted: dec("120")},
		{Period: "2027-03", CumValue: dec("5000"), Posted: dec("50")},
		{Period: "2027-04", CumValue: dec("8000"), Posted: dec("100")},
	}
	got := RetroDeltas(a, periods)
	if len(got) != 2 || got[0].Period != "2027-04" || got[0].Delta.StringFixed(2) != "-20.00" || got[1].Period != "2027-05" || got[1].Delta.StringFixed(2) != "120.00" {
		t.Fatalf("deltas = %+v, want 2027-04 -20.00 then 2027-05 120.00", got)
	}
	for i := range periods {
		periods[i].Posted = PeriodTarget(a, periods[i].CumValue, periods[i].CumQty)
	}
	if again := RetroDeltas(a, periods); len(again) != 0 {
		t.Fatalf("a recompute at target must post nothing, got %+v", again)
	}
}
