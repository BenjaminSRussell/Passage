package domain

import (
	"errors"
	"testing"

	"github.com/shopspring/decimal"
	"pgregory.net/rapid"

	"github.com/BenjaminSRussell/passage/platform/money"
)

func TestProject_WeeklyBucketsWorkedExample(t *testing.T) {
	in := ProjectionInput{
		OpeningOnHand: dec("10"),
		SafetyStock:   dec("5"),
		UnitCost:      unitCost("2.50", "EUR"),
		Demand:        []Flow{{Day: 3, Qty: dec("4")}, {Day: 10, Qty: dec("6")}},
		Supply:        []Flow{{Day: 5, Qty: dec("20")}},
		Planned:       []Flow{{Day: 12, Qty: dec("8")}},
		HorizonDays:   14,
		Bucket:        BucketWeek,
	}
	p, err := Project(in)
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Periods) != 2 {
		t.Fatalf("periods = %d, want 2", len(p.Periods))
	}
	w1, w2 := p.Periods[0], p.Periods[1]
	if !w1.Demand.Equal(dec("4")) || !w1.Supply.Equal(dec("20")) || !w1.ClosingOnHand.Equal(dec("26")) {
		t.Fatalf("week 1 = %+v", w1)
	}
	if !w2.Demand.Equal(dec("6")) || !w2.Planned.Equal(dec("8")) || !w2.ClosingOnHand.Equal(dec("28")) {
		t.Fatalf("week 2 = %+v", w2)
	}
	if !w1.Value.Value.Equal(dec("65.00")) || !w2.Value.Value.Equal(dec("70.00")) || w1.Value.Currency != "EUR" {
		t.Fatalf("values = %s, %s", w1.Value, w2.Value)
	}
	if w1.Shortage || w2.Shortage {
		t.Fatal("no shortage expected")
	}
	if w1.DaysOfSupply == nil {
		t.Fatal("days of supply expected when demand follows")
	}
	// Days after week 1 end (day 6): demand at day 10 only, window 7..36 has 6 over 30 days = 0.2/day; 26 / 0.2 = 130.
	if !w1.DaysOfSupply.Equal(dec("130")) {
		t.Fatalf("days of supply = %s, want 130", w1.DaysOfSupply)
	}
	if w2.DaysOfSupply != nil {
		t.Fatalf("no demand after horizon, days of supply should be nil, got %s", w2.DaysOfSupply)
	}
	if !p.ClosingOnHand.Equal(dec("28")) || !p.TotalDemand.Equal(dec("10")) {
		t.Fatalf("totals: closing %s demand %s", p.ClosingOnHand, p.TotalDemand)
	}
}

func TestProject_ShortageAndNegativeDaysOfSupply(t *testing.T) {
	p, err := Project(ProjectionInput{
		OpeningOnHand: dec("3"),
		SafetyStock:   dec("2"),
		UnitCost:      unitCost("1", "EUR"),
		Demand:        []Flow{{Day: 0, Qty: dec("10")}},
		HorizonDays:   7,
		Bucket:        BucketWeek,
	})
	if err != nil {
		t.Fatal(err)
	}
	row := p.Periods[0]
	if !row.ClosingOnHand.Equal(dec("-7")) || !row.Shortage {
		t.Fatalf("row = %+v, want closing -7 and shortage", row)
	}
	if row.DaysOfSupply != nil {
		t.Fatalf("no demand after the bucket, want nil, got %s", row.DaysOfSupply)
	}
}

func TestProject_PastDueFoldsToDayZeroAndHorizonClipsFlows(t *testing.T) {
	p, err := Project(ProjectionInput{
		OpeningOnHand: dec("0"),
		UnitCost:      unitCost("0", "EUR"),
		Demand:        []Flow{{Day: -4, Qty: dec("5")}, {Day: 40, Qty: dec("9")}},
		Supply:        []Flow{{Day: 50, Qty: dec("100")}},
		HorizonDays:   10,
		Bucket:        BucketDay,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !p.Periods[0].Demand.Equal(dec("5")) {
		t.Fatalf("past-due demand on day 0 = %s, want 5", p.Periods[0].Demand)
	}
	if !p.TotalSupply.IsZero() || !p.TotalDemand.Equal(dec("5")) {
		t.Fatalf("out-of-horizon supply must be ignored: supply %s demand %s", p.TotalSupply, p.TotalDemand)
	}
	if !p.Periods[0].ClosingOnHand.Equal(dec("-5")) {
		t.Fatalf("closing day 0 = %s", p.Periods[0].ClosingOnHand)
	}
}

func TestProject_MonthBucketClipsLastPeriod(t *testing.T) {
	p, err := Project(ProjectionInput{HorizonDays: 45, Bucket: BucketMonth, UnitCost: unitCost("0", "EUR")})
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Periods) != 2 || p.Periods[0].End != 29 || p.Periods[1].Start != 30 || p.Periods[1].End != 44 {
		t.Fatalf("periods = %+v", p.Periods)
	}
}

// unitCost keeps the cost's full precision: a unit cost is not a document
// total, so only the value it produces is rounded.
func unitCost(v, currency string) money.Amount {
	return money.Amount{Value: dec(v), Currency: currency}
}

// Value is money: it rounds to the currency's minor unit (JPY has none, KWD three).
func TestProject_ValueRoundsToCurrencyMinorUnit(t *testing.T) {
	for _, tc := range []struct{ cost, currency, want string }{
		{"0.335", "JPY", "1"},
		{"0.3335", "KWD", "1.001"},
		{"0.335", "EUR", "1.01"},
	} {
		p, err := Project(ProjectionInput{OpeningOnHand: dec("3"), UnitCost: unitCost(tc.cost, tc.currency), HorizonDays: 1, Bucket: BucketDay})
		if err != nil {
			t.Fatal(err)
		}
		if v := p.Periods[0].Value; !v.Value.Equal(dec(tc.want)) || v.Currency != tc.currency {
			t.Fatalf("%s %s x 3 = %s, want %s", tc.cost, tc.currency, v, tc.want)
		}
	}
}

func TestProject_Validation(t *testing.T) {
	tests := map[string]struct {
		in   ProjectionInput
		want error
	}{
		"zero horizon":      {ProjectionInput{HorizonDays: 0, Bucket: BucketDay}, ErrInvalidHorizon},
		"horizon too long":  {ProjectionInput{HorizonDays: 4000, Bucket: BucketDay}, ErrInvalidHorizon},
		"unknown bucket":    {ProjectionInput{HorizonDays: 7, Bucket: "quarter"}, ErrInvalidBucket},
		"negative opening":  {ProjectionInput{HorizonDays: 7, Bucket: BucketDay, OpeningOnHand: dec("-1")}, ErrNegativeInput},
		"negative cost":     {ProjectionInput{HorizonDays: 7, Bucket: BucketDay, UnitCost: unitCost("-1", "EUR")}, ErrNegativeInput},
		"negative flow qty": {ProjectionInput{HorizonDays: 7, Bucket: BucketDay, UnitCost: unitCost("1", "EUR"), Supply: []Flow{{Day: 1, Qty: dec("-2")}}}, ErrNegativeInput},
		"no currency":       {ProjectionInput{HorizonDays: 7, Bucket: BucketDay, UnitCost: money.Amount{Value: dec("1")}}, ErrInvalidCurrency},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			if _, err := Project(tc.in); !errors.Is(err, tc.want) {
				t.Fatalf("err = %v, want %v", err, tc.want)
			}
		})
	}
}

// Property: PAB recurrence holds for every bucket, and the totals agree with the
// in-horizon flows (conservation of quantity).
func TestProperty_ProjectionPABIdentity(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		horizon := rapid.IntRange(1, 120).Draw(t, "horizon")
		bucket := rapid.SampledFrom([]string{BucketDay, BucketWeek, BucketMonth}).Draw(t, "bucket")
		flows := func(name string) []Flow {
			n := rapid.IntRange(0, 8).Draw(t, name+"-n")
			out := make([]Flow, n)
			for i := range out {
				out[i] = Flow{
					Day: rapid.IntRange(-2, horizon+5).Draw(t, name+"-day"),
					Qty: decimal.NewFromInt(int64(rapid.IntRange(0, 50).Draw(t, name+"-qty"))),
				}
			}
			return out
		}
		in := ProjectionInput{
			OpeningOnHand: decimal.NewFromInt(int64(rapid.IntRange(0, 100).Draw(t, "open"))),
			SafetyStock:   decimal.NewFromInt(int64(rapid.IntRange(0, 20).Draw(t, "safety"))),
			UnitCost:      money.Amount{Value: decimal.NewFromInt(int64(rapid.IntRange(0, 9).Draw(t, "cost"))), Currency: "EUR"},
			Demand:        flows("demand"),
			Supply:        flows("supply"),
			Planned:       flows("planned"),
			HorizonDays:   horizon,
			Bucket:        bucket,
		}
		p, err := Project(in)
		if err != nil {
			t.Fatal(err)
		}
		prev := in.OpeningOnHand
		for _, row := range p.Periods {
			want := prev.Add(row.Supply).Add(row.Planned).Sub(row.Demand)
			if !row.ClosingOnHand.Equal(want) {
				t.Fatalf("PAB identity broken: closing %s, want %s", row.ClosingOnHand, want)
			}
			prev = row.ClosingOnHand
		}
		if !p.ClosingOnHand.Equal(prev) {
			t.Fatalf("closing %s != last bucket %s", p.ClosingOnHand, prev)
		}
		var inDemand decimal.Decimal
		for _, f := range in.Demand {
			d := f.Day
			if d < 0 {
				d = 0
			}
			if d < horizon {
				inDemand = inDemand.Add(f.Qty)
			}
		}
		if !p.TotalDemand.Equal(inDemand) {
			t.Fatalf("total demand %s, want %s", p.TotalDemand, inDemand)
		}
	})
}
