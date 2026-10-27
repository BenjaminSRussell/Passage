package domain

import (
	"testing"

	"github.com/shopspring/decimal"
	"pgregory.net/rapid"
)

func cell(qty, sales string) ForecastCell {
	return ForecastCell{Qty: decimal.RequireFromString(qty), SalesQty: decimal.RequireFromString(sales)}
}

func TestReducePeriods_Principles(t *testing.T) {
	tests := []struct {
		name      string
		principle string
		percent   string
		cells     []ForecastCell
		want      []string
	}{
		// AC-22.6: forecast 100, sales 30, transactions -> 70.
		{"transactions subtracts sales", ReductionTransactions, "0", []ForecastCell{cell("100", "30")}, []string{"70"}},
		{"transactions floors at zero", ReductionTransactions, "0", []ForecastCell{cell("10", "25")}, []string{"0"}},
		{"none keeps forecast", ReductionNone, "0", []ForecastCell{cell("100", "30")}, []string{"100"}},
		{"percent ignores sales", ReductionPercent, "20", []ForecastCell{cell("100", "30")}, []string{"80"}},
		{"percent 100 removes all", ReductionPercent, "100", []ForecastCell{cell("100", "0")}, []string{"0"}},
		{"percent rounds to 6 decimals", ReductionPercent, "33.3333333", []ForecastCell{cell("1", "0")}, []string{"0.666667"}},
		// Excess sales of period one (25 vs 10) carry into period two: 20 - 15 = 5.
		{"dynamic carries excess sales", ReductionTransactionsDynamic, "0",
			[]ForecastCell{cell("10", "25"), cell("20", "0")}, []string{"0", "5"}},
		{"dynamic without carry equals transactions", ReductionTransactionsDynamic, "0",
			[]ForecastCell{cell("100", "30"), cell("50", "10")}, []string{"70", "40"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ReducePeriods(tc.principle, decimal.RequireFromString(tc.percent), tc.cells)
			if err != nil {
				t.Fatalf("err = %v", err)
			}
			if len(got) != len(tc.want) {
				t.Fatalf("got %d periods, want %d", len(got), len(tc.want))
			}
			for i, w := range tc.want {
				if !got[i].Equal(decimal.RequireFromString(w)) {
					t.Fatalf("period %d = %s, want %s", i, got[i], w)
				}
			}
		})
	}
}

func TestReducePeriods_RejectsBadInput(t *testing.T) {
	cells := []ForecastCell{cell("10", "0")}
	if _, err := ReducePeriods("bogus", decimal.Zero, cells); err != ErrInvalidReduction {
		t.Fatalf("unknown principle: err = %v", err)
	}
	if _, err := ReducePeriods(ReductionPercent, decimal.NewFromInt(101), cells); err != ErrInvalidReduction {
		t.Fatalf("percent > 100: err = %v", err)
	}
	if _, err := ReducePeriods(ReductionPercent, decimal.NewFromInt(-1), cells); err != ErrInvalidReduction {
		t.Fatalf("negative percent: err = %v", err)
	}
}

// Property: every principle keeps 0 <= reduced <= forecast, and dynamic
// reduction never leaves more forecast than plain transactions.
func TestReducePeriods_Bounds(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		n := rapid.IntRange(1, 12).Draw(t, "periods")
		cells := make([]ForecastCell, n)
		for i := range cells {
			cells[i] = ForecastCell{
				Qty:      decimal.NewFromInt(rapid.Int64Range(0, 500).Draw(t, "qty")),
				SalesQty: decimal.NewFromInt(rapid.Int64Range(0, 500).Draw(t, "sales")),
			}
		}
		trans, err := ReducePeriods(ReductionTransactions, decimal.Zero, cells)
		if err != nil {
			t.Fatal(err)
		}
		dyn, err := ReducePeriods(ReductionTransactionsDynamic, decimal.Zero, cells)
		if err != nil {
			t.Fatal(err)
		}
		for i, c := range cells {
			if trans[i].IsNegative() || trans[i].GreaterThan(c.Qty) {
				t.Fatalf("transactions[%d] = %s outside [0, %s]", i, trans[i], c.Qty)
			}
			if dyn[i].IsNegative() || dyn[i].GreaterThan(c.Qty) {
				t.Fatalf("dynamic[%d] = %s outside [0, %s]", i, dyn[i], c.Qty)
			}
			if dyn[i].GreaterThan(trans[i]) {
				t.Fatalf("dynamic[%d] = %s exceeds transactions %s", i, dyn[i], trans[i])
			}
		}
	})
}
