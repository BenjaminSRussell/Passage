package domain

import (
	"errors"
	"testing"

	"github.com/shopspring/decimal"
	"pgregory.net/rapid"
)

// November 2026 as one forecast period: day 0 = 1 Nov, day 29 = 30 Nov. The
// sales line on 20 Nov is day 19.
var novemberBucket = []ForecastBucket{{ID: "NOV", StartDay: 0, EndDay: 29, Qty: dec("100")}}

func TestConsumeForecast_AC2214AssembleToOrder(t *testing.T) {
	sales := []SalesLine{{ID: "SO-1", Day: 19, Qty: dec("40")}}
	r, err := ConsumeForecast(ConsumptionBackwardForward, 30, 0, novemberBucket, sales)
	if err != nil {
		t.Fatal(err)
	}
	if !r.Forecast[0].Equal(dec("60")) || !r.Sales[0].Consumed.Equal(dec("40")) || !r.Sales[0].Unconsumed.IsZero() {
		t.Fatalf("forecast %s, sale %+v; want 60 remaining and 40 consumed", r.Forecast[0], r.Sales[0])
	}
	if g := GrossQty(r, sales); !g.Equal(dec("100")) {
		t.Fatalf("gross = %s, want 100 (60 forecast + 40 sales)", g)
	}
}

func TestConsumeForecast_ModesSelectWindows(t *testing.T) {
	future := []ForecastBucket{{ID: "F", StartDay: 25, EndDay: 35, Qty: dec("100")}}
	sales := []SalesLine{{ID: "S", Day: 19, Qty: dec("40")}}
	tests := []struct {
		name         string
		mode         string
		back, fwd    int
		wantForecast string
		wantUncons   string
	}{
		{"backward ignores forward window", ConsumptionBackward, 30, 10, "100", "40"},
		{"backward-forward reaches forward window", ConsumptionBackwardForward, 30, 10, "60", "0"},
		{"forward ignores backward window", ConsumptionForward, 30, 0, "100", "40"},
		{"forward window too short", ConsumptionForward, 0, 5, "100", "40"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r, err := ConsumeForecast(tt.mode, tt.back, tt.fwd, future, sales)
			if err != nil {
				t.Fatal(err)
			}
			if !r.Forecast[0].Equal(dec(tt.wantForecast)) || !r.Sales[0].Unconsumed.Equal(dec(tt.wantUncons)) {
				t.Fatalf("forecast %s unconsumed %s; want %s and %s", r.Forecast[0], r.Sales[0].Unconsumed, tt.wantForecast, tt.wantUncons)
			}
		})
	}
}

func TestConsumeForecast_BackwardTakesLatestBucketFirst(t *testing.T) {
	buckets := []ForecastBucket{
		{ID: "W1", StartDay: 0, EndDay: 9, Qty: dec("30")},
		{ID: "W2", StartDay: 10, EndDay: 19, Qty: dec("30")},
	}
	r, err := ConsumeForecast(ConsumptionBackward, 30, 0, buckets, []SalesLine{{ID: "S", Day: 19, Qty: dec("40")}})
	if err != nil {
		t.Fatal(err)
	}
	if !r.Forecast[0].Equal(dec("20")) || !r.Forecast[1].IsZero() {
		t.Fatalf("remaining %v; want W1 20 and W2 0", r.Forecast)
	}
}

func TestConsumeForecast_ForwardTakesEarliestBucketFirst(t *testing.T) {
	buckets := []ForecastBucket{
		{ID: "F2", StartDay: 26, EndDay: 30, Qty: dec("50")},
		{ID: "F1", StartDay: 21, EndDay: 25, Qty: dec("10")},
	}
	r, err := ConsumeForecast(ConsumptionForward, 0, 30, buckets, []SalesLine{{ID: "S", Day: 19, Qty: dec("25")}})
	if err != nil {
		t.Fatal(err)
	}
	// F1 (earliest) gives 10, F2 gives the remaining 15.
	if !r.Forecast[1].Equal(dec("0")) || !r.Forecast[0].Equal(dec("35")) {
		t.Fatalf("remaining %v; want F2 35 and F1 0", r.Forecast)
	}
}

func TestConsumeForecast_SalesBeyondForecastAreUnconsumed(t *testing.T) {
	buckets := []ForecastBucket{{ID: "P", StartDay: 0, EndDay: 29, Qty: dec("20")}}
	sales := []SalesLine{{ID: "S", Day: 5, Qty: dec("50")}}
	r, err := ConsumeForecast(ConsumptionBackwardForward, 30, 0, buckets, sales)
	if err != nil {
		t.Fatal(err)
	}
	if !r.Forecast[0].IsZero() || !r.Sales[0].Consumed.Equal(dec("20")) || !r.Sales[0].Unconsumed.Equal(dec("30")) {
		t.Fatalf("forecast %s, sale %+v", r.Forecast[0], r.Sales[0])
	}
	if g := GrossQty(r, sales); !g.Equal(dec("50")) {
		t.Fatalf("gross = %s, want 50", g)
	}
}

func TestConsumeForecast_OrderOfInputDoesNotMatter(t *testing.T) {
	buckets := []ForecastBucket{{ID: "P", StartDay: 0, EndDay: 29, Qty: dec("60")}}
	a := []SalesLine{{ID: "S1", Day: 4, Qty: dec("40")}, {ID: "S2", Day: 4, Qty: dec("40")}}
	b := []SalesLine{a[1], a[0]}
	ra, _ := ConsumeForecast(ConsumptionBackwardForward, 30, 0, buckets, a)
	rb, _ := ConsumeForecast(ConsumptionBackwardForward, 30, 0, buckets, b)
	if !ra.Forecast[0].Equal(rb.Forecast[0]) {
		t.Fatalf("forecast differs: %s vs %s", ra.Forecast[0], rb.Forecast[0])
	}
	for _, s := range ra.Sales {
		for _, u := range rb.Sales {
			if s.ID == u.ID && !s.Consumed.Equal(u.Consumed) {
				t.Fatalf("%s consumed %s vs %s", s.ID, s.Consumed, u.Consumed)
			}
		}
	}
}

func TestConsumeForecast_RejectsBadSetup(t *testing.T) {
	ok := []ForecastBucket{{StartDay: 0, EndDay: 5, Qty: dec("1")}}
	cases := []struct {
		name    string
		mode    string
		back    int
		buckets []ForecastBucket
		sales   []SalesLine
	}{
		{"unknown mode", "sideways", 0, ok, nil},
		{"negative window", ConsumptionBackward, -1, ok, nil},
		{"bucket ends before start", ConsumptionBackward, 0, []ForecastBucket{{StartDay: 5, EndDay: 1, Qty: dec("1")}}, nil},
		{"negative sales", ConsumptionBackward, 0, ok, []SalesLine{{ID: "x", Qty: dec("-1")}}},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := ConsumeForecast(tt.mode, tt.back, 0, tt.buckets, tt.sales); !errors.Is(err, ErrInvalidStrategy) {
				t.Fatalf("err = %v, want ErrInvalidStrategy", err)
			}
		})
	}
}

func TestStrategyCatalogue(t *testing.T) {
	for _, code := range []string{"mts-net", "mts-gross", "mto", "assemble-to-order-final", "plan-without-final-assembly", "subassembly"} {
		if !ValidStrategy(code) {
			t.Errorf("%s should be valid", code)
		}
	}
	if ValidStrategy("make-to-stock") {
		t.Error("unknown code accepted")
	}
	if !UsesMarking(StrategyMTO) || !UsesMarking(StrategyPlanWithoutFinal) || UsesMarking(StrategyMTSNet) {
		t.Error("marking must be set for mto and plan-without-final-assembly only")
	}
}

// Property: consumption conserves quantity. Consumed plus unconsumed equals each
// sales line, the forecast falls by exactly the total consumed, and no bucket goes negative.
func TestProperty_ConsumptionConservesQuantity(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		nb := rapid.IntRange(0, 6).Draw(t, "nb")
		buckets := make([]ForecastBucket, nb)
		total := decimal.Zero
		for i := range buckets {
			start := rapid.IntRange(-10, 60).Draw(t, "start")
			buckets[i] = ForecastBucket{ID: string(rune('a' + i)), StartDay: start,
				EndDay: start + rapid.IntRange(0, 15).Draw(t, "len"),
				Qty:    decimal.NewFromInt(int64(rapid.IntRange(0, 200).Draw(t, "qty")))}
			total = total.Add(buckets[i].Qty)
		}
		ns := rapid.IntRange(0, 6).Draw(t, "ns")
		sales := make([]SalesLine, ns)
		for i := range sales {
			sales[i] = SalesLine{ID: string(rune('A' + i)), Day: rapid.IntRange(-5, 60).Draw(t, "day"),
				Qty: decimal.NewFromInt(int64(rapid.IntRange(0, 120).Draw(t, "sq")))}
		}
		mode := rapid.SampledFrom([]string{ConsumptionBackward, ConsumptionForward, ConsumptionBackwardForward}).Draw(t, "mode")
		back := rapid.IntRange(0, 40).Draw(t, "back")
		fwd := rapid.IntRange(0, 40).Draw(t, "fwd")

		r, err := ConsumeForecast(mode, back, fwd, buckets, sales)
		if err != nil {
			t.Fatal(err)
		}
		consumed, left := decimal.Zero, decimal.Zero
		for i, f := range r.Forecast {
			if f.IsNegative() {
				t.Fatalf("bucket %d negative: %s", i, f)
			}
			left = left.Add(f)
		}
		for i, s := range r.Sales {
			if !s.Consumed.Add(s.Unconsumed).Equal(sales[i].Qty) || s.Unconsumed.IsNegative() || s.Consumed.IsNegative() {
				t.Fatalf("sale %s does not conserve: %+v of %s", s.ID, s, sales[i].Qty)
			}
			consumed = consumed.Add(s.Consumed)
		}
		if !total.Sub(left).Equal(consumed) {
			t.Fatalf("forecast fell by %s but %s was consumed", total.Sub(left), consumed)
		}
	})
}
