package domain

import (
	"testing"
	"time"

	"github.com/shopspring/decimal"
	"pgregory.net/rapid"
)

var asOf = time.Date(2026, 10, 23, 0, 0, 0, 0, time.UTC)

func contrib(date string, amount string, cur string) Contribution {
	d, _ := Day(date)
	return Contribution{SourceType: "AP", SourceID: "x", ExpectedDate: d, Amount: decimal.RequireFromString(amount), Currency: cur}
}

func TestBuild_PlacesContributionsInBuckets(t *testing.T) {
	cs := []Contribution{
		contrib("2026-10-23", "100.00", "EUR"),  // day 0
		contrib("2026-10-20", "-25.00", "EUR"),  // overdue, first bucket
		contrib("2026-10-25", "-10.00", "EUR"),  // day 2
		contrib("2026-10-25", "5.00", "USD"),    // other currency, same day
		contrib("2026-11-15", "5000.00", "USD"), // day 23
		contrib("2027-12-01", "1.00", "EUR"),    // beyond daily horizon
	}
	f, err := Build(asOf, LayoutDaily, cs)
	if err != nil {
		t.Fatal(err)
	}
	if f.OutOfHorizon != 1 || len(f.Buckets) != 4 {
		t.Fatalf("forecast = %+v", f)
	}
	first := f.Buckets[0]
	if first.Key != "2026-10-23" || first.Currency != "EUR" || !first.Net.Equal(decimal.RequireFromString("75")) || !first.Inflow.Equal(decimal.RequireFromString("100")) || !first.Outflow.Equal(decimal.RequireFromString("-25")) {
		t.Fatalf("first bucket = %+v", first)
	}
	if last := f.Buckets[len(f.Buckets)-1]; last.Key != "2026-11-15" || last.Currency != "USD" || !last.Net.Equal(decimal.RequireFromString("5000")) {
		t.Fatalf("last bucket = %+v", last)
	}
	for _, b := range f.Buckets {
		if b.Currency == "EUR" && b.Key == "2026-10-25" && !b.Cumulative.Equal(decimal.RequireFromString("65")) {
			t.Fatalf("running total = %s; want 65", b.Cumulative)
		}
	}
}

func TestBuild_WeeklyAndMonthlyLayouts(t *testing.T) {
	cs := []Contribution{contrib("2026-10-29", "10.00", "EUR"), contrib("2026-11-02", "20.00", "EUR"), contrib("2027-09-01", "7.00", "EUR"), contrib("2027-10-01", "1.00", "EUR")}
	weekly, err := Build(asOf, LayoutWeekly, cs)
	if err != nil || len(weekly.Buckets) != 2 || weekly.Buckets[0].Key != "2026-10-23" || weekly.Buckets[1].Key != "2026-10-30" || weekly.OutOfHorizon != 2 {
		t.Fatalf("weekly = %+v, %v", weekly, err)
	}
	monthly, err := Build(asOf, LayoutMonthly, cs)
	if err != nil || len(monthly.Buckets) != 3 || monthly.Buckets[0].Key != "2026-10-01" || monthly.Buckets[1].Key != "2026-11-01" || monthly.Buckets[2].Key != "2027-09-01" || monthly.OutOfHorizon != 1 {
		t.Fatalf("monthly = %+v, %v", monthly, err)
	}
	if _, err := Build(asOf, Layout("hourly"), cs); err == nil {
		t.Fatal("unknown layout must be rejected")
	}
	if _, err := Build(asOf, LayoutDaily, []Contribution{contrib("2026-10-24", "1", "")}); err == nil {
		t.Fatal("contribution without currency must be rejected")
	}
}

// TestBuild_AdditivityProperty checks that every bucket total is the sum of its
// contributions, so the horizon total equals the sum of contributions inside it (AC2).
func TestBuild_AdditivityProperty(t *testing.T) {
	layouts := []Layout{LayoutDaily, LayoutWeekly, LayoutMonthly}
	rapid.Check(t, func(t *rapid.T) {
		layout := rapid.SampledFrom(layouts).Draw(t, "layout")
		n := rapid.IntRange(0, 200).Draw(t, "n")
		cs := make([]Contribution, n)
		for i := range cs {
			days := rapid.IntRange(-20, 420).Draw(t, "days")
			cents := rapid.Int64Range(-10000000, 10000000).Draw(t, "cents")
			cur := rapid.SampledFrom([]string{"EUR", "USD", "GBP"}).Draw(t, "cur")
			cs[i] = Contribution{SourceType: "AR", SourceID: "s", ExpectedDate: asOf.AddDate(0, 0, days), Amount: decimal.New(cents, -2), Currency: cur}
		}
		f, err := Build(asOf, layout, cs)
		if err != nil {
			t.Fatal(err)
		}
		want := map[string]decimal.Decimal{}
		inside := 0
		size, _ := layout.Buckets()
		for _, c := range cs {
			if idx, ok := bucketIndex(asOf, c.ExpectedDate, layout); ok && idx < size {
				want[c.Currency] = want[c.Currency].Add(c.Amount)
				inside++
			}
		}
		got := map[string]decimal.Decimal{}
		for _, b := range f.Buckets {
			if !b.Inflow.Add(b.Outflow).Equal(b.Net) {
				t.Fatalf("bucket %+v: inflow + outflow != net", b)
			}
			got[b.Currency] = got[b.Currency].Add(b.Net)
		}
		for cur := range want {
			if !got[cur].Equal(want[cur]) {
				t.Fatalf("%s: buckets sum to %s, contributions to %s", cur, got[cur], want[cur])
			}
		}
		for cur := range got {
			if _, ok := want[cur]; !ok {
				t.Fatalf("%s bucket without contributions", cur)
			}
		}
		if f.OutOfHorizon != n-inside {
			t.Fatalf("out of horizon = %d; want %d", f.OutOfHorizon, n-inside)
		}
	})
}
