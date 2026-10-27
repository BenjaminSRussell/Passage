package domain

import (
	"testing"

	"github.com/shopspring/decimal"
	"pgregory.net/rapid"

	"github.com/BenjaminSRussell/passage/platform/money"
)

func w(v int64) decimal.Decimal { return decimal.NewFromInt(v) }

func amounts(got []Allocated) []string {
	out := make([]string, len(got))
	for i, a := range got {
		out[i] = a.Amount.Value.StringFixed(money.MinorUnits(a.Amount.Currency))
	}
	return out
}

// TestAllocateCost_AC23_12 covers AC-23.12: a USD 900.00 cleanout over orders
// with RAF 100, 200 and 300 units gives 150.00, 300.00 and 450.00.
func TestAllocateCost_AC23_12(t *testing.T) {
	shares := []CostShare{{OrderID: "o1", Weight: w(100)}, {OrderID: "o2", Weight: w(200)}, {OrderID: "o3", Weight: w(300)}}
	got, err := AllocateCost(money.Must("900.00", "USD"), shares)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"150.00", "300.00", "450.00"}
	for i, a := range got {
		if a.OrderID != shares[i].OrderID || a.Amount.Currency != "USD" || amounts(got)[i] != want[i] {
			t.Fatalf("allocation %d = %s %s, want %s USD", i, a.OrderID, a.Amount, want[i])
		}
	}
}

// TestAllocateCost_RoundsAndGivesResidualToLargest checks BR-23.14: each share
// is rounded half away from zero to the currency's minor unit and the residual
// goes to the largest share, ties broken by the lowest order id.
func TestAllocateCost_RoundsAndGivesResidualToLargest(t *testing.T) {
	cases := []struct {
		name   string
		cost   money.Amount
		shares []CostShare
		want   []string
	}{
		// 100/3 = 33.333.. rounds to 33.33 three times; 0.01 left goes to "a".
		{"thirds", money.Must("100.00", "USD"), []CostShare{{"a", w(1)}, {"b", w(1)}, {"c", w(1)}}, []string{"33.34", "33.33", "33.33"}},
		// 10 x 1/3 = 3.333.. -> 3.33, 10 x 2/3 = 6.666.. -> 6.67: rounding, not flooring.
		{"rounds up the larger share", money.Must("10.00", "USD"), []CostShare{{"a", w(1)}, {"b", w(2)}}, []string{"3.33", "6.67"}},
		// Two halves of 0.05 round to 0.03 each (half away from zero), overshooting
		// by 0.01, which the largest (tie: lowest id "x") gives back.
		{"overshoot comes back from the largest", money.Must("0.05", "USD"), []CostShare{{"y", w(1)}, {"x", w(1)}}, []string{"0.03", "0.02"}},
		{"zero-decimal currency", money.Must("1000", "JPY"), []CostShare{{"a", w(1)}, {"b", w(1)}, {"c", w(1)}}, []string{"334", "333", "333"}},
		{"three-decimal currency", money.Must("1.000", "KWD"), []CostShare{{"a", w(1)}, {"b", w(2)}}, []string{"0.333", "0.667"}},
		// 0.05 over ten equal orders rounds to 0.01 each (0.10): the largest would
		// end at -0.04, so the shares are floored and the residual goes to "a".
		{"tiny cost never credits an order", money.Must("0.05", "USD"),
			[]CostShare{{"a", w(1)}, {"b", w(1)}, {"c", w(1)}, {"d", w(1)}, {"e", w(1)}, {"f", w(1)}, {"g", w(1)}, {"h", w(1)}, {"i", w(1)}, {"j", w(1)}},
			[]string{"0.05", "0.00", "0.00", "0.00", "0.00", "0.00", "0.00", "0.00", "0.00", "0.00"}},
		{"zero weight gets nothing", money.Must("90.00", "EUR"), []CostShare{{"a", w(0)}, {"b", w(3)}}, []string{"0.00", "90.00"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := AllocateCost(tc.cost, tc.shares)
			if err != nil {
				t.Fatal(err)
			}
			for i, s := range amounts(got) {
				if s != tc.want[i] {
					t.Fatalf("allocation = %v, want %v", amounts(got), tc.want)
				}
			}
		})
	}
}

func TestAllocateCost_Rejects(t *testing.T) {
	cases := []struct {
		name   string
		cost   money.Amount
		shares []CostShare
	}{
		{"negative cost", money.Must("-1", "USD"), []CostShare{{"a", w(1)}}},
		{"no currency", money.Amount{Value: w(1)}, []CostShare{{"a", w(1)}}},
		{"no orders", money.Must("10", "USD"), nil},
		{"negative weight", money.Must("10", "USD"), []CostShare{{"a", w(-1)}, {"b", w(2)}}},
		{"all weights zero", money.Must("10", "USD"), []CostShare{{"a", w(0)}, {"b", w(0)}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := AllocateCost(tc.cost, tc.shares); err == nil {
				t.Fatal("expected an error")
			}
		})
	}
}

func TestAllocationWeight(t *testing.T) {
	qty := decimal.RequireFromString("300.5")
	cases := []struct {
		basis   string
		want    string
		wantErr bool
	}{
		{BasisQuantity, "300.5", false},
		{BasisEqual, "1", false},
		{BasisRunMinutes, "50", false},
		{"by-magic", "", true},
	}
	for _, tc := range cases {
		got, err := AllocationWeight(tc.basis, qty, 50)
		if (err != nil) != tc.wantErr {
			t.Fatalf("%s: err = %v", tc.basis, err)
		}
		if err == nil && got.String() != tc.want {
			t.Fatalf("%s: weight %s, want %s", tc.basis, got, tc.want)
		}
	}
	if _, err := AllocationWeight(BasisQuantity, w(-1), 0); err == nil {
		t.Fatal("negative quantity accepted")
	}
	if _, err := AllocationWeight(BasisRunMinutes, w(1), -1); err == nil {
		t.Fatal("negative run minutes accepted")
	}
}

// TestAllocateCost_Reconciles is the reconciliation check from the test plan
// (Σ allocated = Σ actual cost, PRD 4.23.4 invariant): the amounts always sum
// exactly to the cost, carry its currency and minor unit, and none is negative.
func TestAllocateCost_Reconciles(t *testing.T) {
	currencies := []string{"USD", "JPY", "KWD"}
	rapid.Check(t, func(t *rapid.T) {
		cur := rapid.SampledFrom(currencies).Draw(t, "currency")
		exp := money.MinorUnits(cur)
		cost := money.Amount{Value: decimal.New(rapid.Int64Range(0, 1_000_000_000).Draw(t, "cost"), -exp), Currency: cur}
		n := rapid.IntRange(1, 12).Draw(t, "n")
		shares := make([]CostShare, n)
		for i := range shares {
			shares[i] = CostShare{OrderID: string(rune('a' + i)), Weight: decimal.New(rapid.Int64Range(0, 10_000_000).Draw(t, "weight"), -3)}
		}
		shares[0].Weight = shares[0].Weight.Add(w(1)) // the total is never zero
		got, err := AllocateCost(cost, shares)
		if err != nil {
			t.Fatal(err)
		}
		sum := decimal.Zero
		for _, a := range got {
			if a.Amount.Value.IsNegative() || a.Amount.Currency != cur {
				t.Fatalf("bad allocation %+v", a)
			}
			if !a.Amount.Value.Equal(a.Amount.Value.Round(exp)) {
				t.Fatalf("allocation %s has more than %d decimals", a.Amount.Value, exp)
			}
			sum = sum.Add(a.Amount.Value)
		}
		if !sum.Equal(cost.Value) {
			t.Fatalf("allocated %s of %s", sum, cost.Value)
		}
	})
}
