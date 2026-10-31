package domain

import (
	"testing"

	"github.com/shopspring/decimal"
	"pgregory.net/rapid"
)

func TestNextStatus(t *testing.T) {
	tests := []struct {
		cmd, from, to, requires string
		ok                      bool
	}{
		{CmdRelease, StatusPlanned, StatusReleased, StatusPlanned, true},
		{CmdStart, StatusReleased, StatusStarted, StatusReleased, true},
		{CmdReport, StatusStarted, StatusReported, StatusStarted, true},
		{CmdEnd, StatusReported, StatusEnded, StatusReported, true},
		{CmdStart, StatusPlanned, StatusStarted, StatusReleased, false},
		{CmdEnd, StatusStarted, StatusEnded, StatusReported, false},
		{CmdRelease, StatusReleased, StatusReleased, StatusPlanned, false},
		{"close", StatusEnded, "", "", false},
	}
	for _, tc := range tests {
		to, requires, ok := NextStatus(tc.cmd, tc.from)
		if ok != tc.ok || to != tc.to || requires != tc.requires {
			t.Errorf("%s from %s = %q %q %v, want %q %q %v", tc.cmd, tc.from, to, requires, ok, tc.to, tc.requires, tc.ok)
		}
	}
}

// AC-24.3 on an order: A 200 at std 40, co-product B 100 at std 20, by-product C
// 50 at 1.50 per unit, cost 10,000 (the BR-24.3 fixture) split by TCA.
func TestAllocateOrder_BR243Fixture(t *testing.T) {
	got, err := AllocateOrder(OrderAllocationInput{
		Method: MethodTCA, BatchCost: dec("10000"), MainItemID: "A", MainRafQty: dec("200"), MainBasis: CostBasis{StandardCost: dec("40")},
		CoProducts: []OutputRef{{LineNumber: 4, ItemID: "B", RafQty: dec("100")}},
		CoBasis:    map[int]CostBasis{4: {StandardCost: dec("20")}},
		ByProducts: []OutputRef{{LineNumber: 5, ItemID: "C", RafQty: dec("50")}},
		ByRates:    map[int]ByRate{5: {CostType: "per-quantity", Rate: dec("1.5")}},
	})
	if err != nil {
		t.Fatal(err)
	}
	want := map[int]string{0: "7940", 4: "1985", 5: "75"}
	if len(got) != 3 {
		t.Fatalf("recipients %+v", got)
	}
	for _, r := range got {
		if !r.Amount.Equal(dec(want[r.LineNumber])) {
			t.Errorf("line %d (%s) = %s, want %s", r.LineNumber, r.Role, r.Amount, want[r.LineNumber])
		}
	}
	if got[0].Role != "main" || got[1].Role != "co-product" || got[2].Role != "by-product-burden" {
		t.Fatalf("order of roles %+v", got)
	}
}

// A main product with zero standard cost and co-products whose rounded shares
// overshoot the remainder: the overshoot comes off the largest co-product first, so
// no share is negative and the shares still add up to the cost.
func TestAllocateOrder_ZeroCostMainTakesNoNegativeResidual(t *testing.T) {
	got, err := AllocateOrder(OrderAllocationInput{
		Method: MethodTCA, BatchCost: dec("1.00"), MainItemID: "A", MainRafQty: dec("1"),
		CoProducts: []OutputRef{{LineNumber: 1, ItemID: "B", RafQty: dec("1")}, {LineNumber: 2, ItemID: "C", RafQty: dec("1")}, {LineNumber: 3, ItemID: "D", RafQty: dec("4")}},
		CoBasis:    map[int]CostBasis{1: {StandardCost: dec("1")}, 2: {StandardCost: dec("1")}, 3: {StandardCost: dec("1")}},
	})
	if err != nil {
		t.Fatal(err)
	}
	want := map[int][2]string{0: {"0", "0"}, 1: {"0.1667", "16.6667"}, 2: {"0.1667", "16.6667"}, 3: {"0.6666", "66.6666"}}
	for _, r := range got {
		w := want[r.LineNumber]
		if !r.Amount.Equal(dec(w[0])) || !r.Percent.Equal(dec(w[1])) {
			t.Errorf("line %d = %s / %s%%, want %s / %s%%", r.LineNumber, r.Amount, r.Percent, w[0], w[1])
		}
	}
}

func TestAllocateOrder_Refuses(t *testing.T) {
	base := func() OrderAllocationInput {
		return OrderAllocationInput{Method: MethodQuantity, BatchCost: dec("100"), MainItemID: "A", MainRafQty: dec("10"),
			CoProducts: []OutputRef{{LineNumber: 4, ItemID: "B", RafQty: dec("5")}},
			ByProducts: []OutputRef{{LineNumber: 5, ItemID: "C", RafQty: dec("1")}}}
	}
	tests := []struct {
		name string
		edit func(*OrderAllocationInput)
		code string
	}{
		{"no main quantity", func(i *OrderAllocationInput) { i.MainRafQty = decimal.Zero }, CodeInvalid},
		{"basis for a line that is not a co-product", func(i *OrderAllocationInput) { i.CoBasis = map[int]CostBasis{9: {}} }, CodeInvalid},
		{"rate for a line that is not a by-product", func(i *OrderAllocationInput) { i.ByRates = map[int]ByRate{4: {CostType: "percent", Rate: dec("1")}} }, CodeInvalid},
		{"burden above the batch cost", func(i *OrderAllocationInput) {
			i.ByRates = map[int]ByRate{5: {CostType: "per-quantity", Rate: dec("500")}}
		}, CodeAllocation},
		{"unknown cost type", func(i *OrderAllocationInput) { i.ByRates = map[int]ByRate{5: {CostType: "weird", Rate: dec("1")}} }, CodeInvalid},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			in := base()
			tc.edit(&in)
			_, err := AllocateOrder(in)
			de, ok := err.(*Error)
			if !ok || de.Code != tc.code {
				t.Fatalf("err = %v, want %s", err, tc.code)
			}
		})
	}
}

// AC-24.3 / AC2 of the ticket: across every method the shares add up to the
// batch cost exactly.
func TestAllocateOrder_SharesAlwaysSumToBatchCost(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		method := rapid.SampledFrom([]AllocationMethod{MethodNone, MethodManual, MethodTCA, MethodQuantity}).Draw(t, "method")
		cost := decimal.New(rapid.Int64Range(0, 99999999).Draw(t, "cost"), -4)
		n := rapid.IntRange(0, 4).Draw(t, "co")
		// With co-products the main product may weigh nothing (zero standard
		// cost or percent), the case where rounding overshoots the remainder.
		minMain := int64(1)
		if n > 0 {
			minMain = 0
		}
		in := OrderAllocationInput{Method: method, BatchCost: cost, MainItemID: "A", MainRafQty: decimal.New(rapid.Int64Range(1, 100000).Draw(t, "raf"), -3),
			MainBasis: CostBasis{StandardCost: decimal.New(rapid.Int64Range(minMain, 5000).Draw(t, "std"), -2), Percent: decimal.NewFromInt(rapid.Int64Range(minMain, 60).Draw(t, "pct"))},
			CoBasis:   map[int]CostBasis{}}
		for i := 0; i < n; i++ {
			ln := 10 + i
			in.CoProducts = append(in.CoProducts, OutputRef{LineNumber: ln, ItemID: "B", RafQty: decimal.New(rapid.Int64Range(1, 100000).Draw(t, "coraf"), -3)})
			in.CoBasis[ln] = CostBasis{StandardCost: decimal.New(rapid.Int64Range(1, 5000).Draw(t, "costd"), -2), Percent: decimal.NewFromInt(rapid.Int64Range(1, 30).Draw(t, "copct"))}
		}
		got, err := AllocateOrder(in)
		if err != nil {
			t.Fatal(err)
		}
		sum := decimal.Zero
		for _, r := range got {
			if r.Amount.IsNegative() || r.Percent.IsNegative() || r.Percent.GreaterThan(decimal.NewFromInt(100)) {
				t.Fatalf("share out of range %+v", r)
			}
			sum = sum.Add(r.Amount)
		}
		if !sum.Equal(cost) {
			t.Fatalf("shares sum to %s, cost %s", sum, cost)
		}
	})
}
