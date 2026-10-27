package domain

import (
	"testing"

	"github.com/shopspring/decimal"
	"pgregory.net/rapid"
)

// TestAllocateCost_BR243 is the BR-24.3 fixture: cost 10,000; A std 40 × 200,
// B std 20 × 100; by-product C per-quantity 1.50 × 50 (burden 75).
func TestAllocateCost_BR243(t *testing.T) {
	main := Product{ItemID: "A", StandardCost: dec("40"), RafQty: dec("200")}
	co := []Product{{ItemID: "B", StandardCost: dec("20"), RafQty: dec("100")}}
	by := []ByProduct{{ItemID: "C", CostType: "per-quantity", Qty: dec("50"), Rate: dec("1.5")}}
	res, err := AllocateCost(dec("10000"), MethodTCA, main, co, by)
	if err != nil {
		t.Fatal(err)
	}
	if !res.Burdens[0].Amount.Equal(dec("75")) {
		t.Fatalf("burden = %s", res.Burdens[0].Amount)
	}
	if !res.Main.Amount.Equal(dec("7940")) || !res.CoProducts[0].Amount.Equal(dec("1985")) {
		t.Fatalf("main %s, co %s", res.Main.Amount, res.CoProducts[0].Amount)
	}
	if !res.Total.Equal(dec("10000")) {
		t.Fatalf("total = %s", res.Total)
	}
}

func TestAllocateCost_Methods(t *testing.T) {
	main := Product{ItemID: "A", StandardCost: dec("40"), RafQty: dec("200"), Percent: dec("70")}
	co := []Product{{ItemID: "B", StandardCost: dec("0"), RafQty: dec("100"), Percent: dec("30")}}
	tests := []struct {
		name     string
		method   AllocationMethod
		wantMain string
		wantB    string
	}{
		{"none puts all cost on main", MethodNone, "1000", "0"},
		{"manual uses percentages", MethodManual, "700", "300"},
		{"tca with zero co-product std cost", MethodTCA, "1000", "0"},
		{"quantity by raf qty", MethodQuantity, "666.6667", "333.3333"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			res, err := AllocateCost(dec("1000"), tc.method, main, co, nil)
			if err != nil {
				t.Fatal(err)
			}
			if !res.Main.Amount.Equal(dec(tc.wantMain)) || !res.CoProducts[0].Amount.Equal(dec(tc.wantB)) {
				t.Fatalf("main %s co %s", res.Main.Amount, res.CoProducts[0].Amount)
			}
			if !res.Total.Equal(dec("1000")) {
				t.Fatalf("total %s", res.Total)
			}
		})
	}
}

func TestAllocateCost_Rejects(t *testing.T) {
	main := Product{ItemID: "A", StandardCost: dec("0"), RafQty: dec("0")}
	if _, err := AllocateCost(dec("100"), MethodTCA, main, nil, nil); err == nil || err.(*Error).Code != CodeAllocation {
		t.Fatalf("zero weights: %v", err)
	}
	if _, err := AllocateCost(dec("100"), "bogus", main, nil, nil); err == nil {
		t.Fatal("unknown method accepted")
	}
	by := []ByProduct{{ItemID: "C", CostType: "per-quantity", Qty: dec("10"), Rate: dec("20")}}
	one := Product{ItemID: "A", RafQty: dec("1")}
	if _, err := AllocateCost(dec("100"), MethodQuantity, one, nil, by); err == nil || err.(*Error).Code != CodeAllocation {
		t.Fatalf("burden over cost: %v", err)
	}
	bad := []ByProduct{{ItemID: "C", CostType: "percent", Rate: dec("101")}}
	if _, err := AllocateCost(dec("100"), MethodQuantity, one, nil, bad); err == nil {
		t.Fatal("percent above 100 accepted")
	}
}

func TestAllocateCost_ByProductPercentAndRecycled(t *testing.T) {
	one := Product{ItemID: "A", RafQty: dec("1")}
	by := []ByProduct{
		{ItemID: "P", CostType: "percent", Rate: dec("10")},
		{ItemID: "R", CostType: "recycled", Qty: dec("4"), Rate: dec("2.5")},
	}
	res, err := AllocateCost(dec("1000"), MethodQuantity, one, nil, by)
	if err != nil {
		t.Fatal(err)
	}
	if !res.Burdens[0].Amount.Equal(dec("100")) || !res.Burdens[1].Amount.Equal(dec("10")) {
		t.Fatalf("burdens %+v", res.Burdens)
	}
	if !res.Main.Amount.Equal(dec("890")) || !res.Total.Equal(dec("1000")) {
		t.Fatalf("main %s total %s", res.Main.Amount, res.Total)
	}
}

// drawProduct draws a random product for the property test. The label keeps
// each draw distinct in rapid's shrink output.
func drawProduct(t *rapid.T, itemID, label string) Product {
	std := decimal.New(rapid.Int64Range(1, 10000).Draw(t, label+"-std"), -2)
	qty := decimal.NewFromInt(rapid.Int64Range(1, 500).Draw(t, label+"-qty"))
	pct := decimal.NewFromInt(rapid.Int64Range(1, 100).Draw(t, label+"-pct"))
	return Product{ItemID: itemID, StandardCost: std, RafQty: qty, Percent: pct}
}

// TestAllocateCost_SumsToBatchCost is the property test from the PRD test plan:
// Σ allocated + by-product burden = batch cost, exactly, for every method.
func TestAllocateCost_SumsToBatchCost(t *testing.T) {
	methods := []AllocationMethod{MethodNone, MethodManual, MethodTCA, MethodQuantity}
	rapid.Check(t, func(t *rapid.T) {
		cost := decimal.New(rapid.Int64Range(100000, 1_000_000_000).Draw(t, "cents"), -2)
		method := rapid.SampledFrom(methods).Draw(t, "method")
		n := rapid.IntRange(0, 3).Draw(t, "coproducts")
		main := drawProduct(t, "M", "main")
		var co []Product
		for i := 0; i < n; i++ {
			co = append(co, drawProduct(t, "C", "co"))
		}
		var by []ByProduct
		for i := rapid.IntRange(0, 2).Draw(t, "byproducts"); i > 0; i-- {
			qty := decimal.NewFromInt(rapid.Int64Range(0, 10).Draw(t, "bqty"))
			rate := decimal.New(rapid.Int64Range(0, 100).Draw(t, "brate"), -2)
			by = append(by, ByProduct{ItemID: "B", CostType: "per-quantity", Qty: qty, Rate: rate})
		}
		res, err := AllocateCost(cost, method, main, co, by)
		if err != nil {
			t.Fatal(err)
		}
		if !res.Total.Equal(cost) {
			t.Fatalf("total %s != cost %s (method %s)", res.Total, cost, method)
		}
		sum := res.Main.Amount
		for _, c := range res.CoProducts {
			sum = sum.Add(c.Amount)
		}
		for _, b := range res.Burdens {
			sum = sum.Add(b.Amount)
		}
		if !sum.Equal(cost) {
			t.Fatalf("parts %s != cost %s", sum, cost)
		}
	})
}

// pmf-coby-12 (PRD 4.24.9): allocation methods × by-product cost types on the
// BR-24.3 batch (cost 10,000; main A std 40 × 200, 70%; co-product B std 20 ×
// 100, 30%). Every row also checks main + co-product + burden = batch cost.
func TestAllocateCost_CobyFixture(t *testing.T) {
	rows := readFixture(t, "pmf-coby-12.csv", 7)
	if len(rows) != 12 {
		t.Fatalf("pmf-coby-12 has %d rows", len(rows))
	}
	cost := dec("10000")
	main := Product{ItemID: "A", StandardCost: dec("40"), RafQty: dec("200"), Percent: dec("70")}
	co := []Product{{ItemID: "B", StandardCost: dec("20"), RafQty: dec("100"), Percent: dec("30")}}
	for _, r := range rows {
		by := []ByProduct{{ItemID: "C", CostType: r[1], Qty: dec(r[2]), Rate: dec(r[3])}}
		res, err := AllocateCost(cost, AllocationMethod(r[0]), main, co, by)
		if err != nil {
			t.Fatalf("%s/%s: %v", r[0], r[1], err)
		}
		if !res.Burdens[0].Amount.Equal(dec(r[4])) || !res.Main.Amount.Equal(dec(r[5])) || !res.CoProducts[0].Amount.Equal(dec(r[6])) {
			t.Errorf("%s/%s: burden %s main %s co %s, want %s %s %s", r[0], r[1], res.Burdens[0].Amount, res.Main.Amount, res.CoProducts[0].Amount, r[4], r[5], r[6])
		}
		if sum := res.Main.Amount.Add(res.CoProducts[0].Amount).Add(res.Burdens[0].Amount); !sum.Equal(cost) || !res.Total.Equal(cost) {
			t.Errorf("%s/%s: parts sum to %s (total %s), want %s", r[0], r[1], sum, res.Total, cost)
		}
	}
}
