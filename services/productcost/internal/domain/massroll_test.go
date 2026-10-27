package domain

import (
	"fmt"
	"testing"

	"github.com/shopspring/decimal"
	"pgregory.net/rapid"
)

// cents turns an integer count of hundredths into a decimal amount.
func cents(c int64) decimal.Decimal { return decimal.New(c, -2) }

// genDAG draws a random acyclic catalog: item i may use only items j < i, so
// every draw rolls without errors and exercises shared subassemblies.
func genDAG(t *rapid.T) Catalog {
	n := rapid.IntRange(1, 25).Draw(t, "items")
	cat := Catalog{}
	for i := 0; i < n; i++ {
		id := fmt.Sprintf("I%02d", i)
		if i == 0 || rapid.IntRange(0, 2).Draw(t, "kind") == 0 {
			price := cents(rapid.Int64Range(1, 100000).Draw(t, "price"))
			cat[id] = Item{ID: id, Kind: KindPurchased, Price: &price}
			continue
		}
		it := Item{ID: id, Kind: KindManufactured, CostingLot: d("5")}
		for k := rapid.IntRange(1, 3).Draw(t, "children"); k > 0; k-- {
			child := rapid.IntRange(0, i-1).Draw(t, "child")
			it.BOM = append(it.BOM, BOMLine{
				ChildID:  fmt.Sprintf("I%02d", child),
				Qty:      cents(rapid.Int64Range(1, 900).Draw(t, "qty")),
				ScrapPct: cents(rapid.Int64Range(0, 2500).Draw(t, "scrap")),
			})
		}
		it.Operations = []Operation{{
			SetupHours: cents(rapid.Int64Range(0, 300).Draw(t, "setup")),
			RunHours:   cents(rapid.Int64Range(0, 300).Draw(t, "run")),
			Rate:       cents(rapid.Int64Range(1, 9000).Draw(t, "rate")),
		}}
		it.Sheet = []SheetNode{{
			ID: "OH", Type: NodeSurcharge, Component: CompIndMat, Basis: []string{CompDM},
			Percentage:    cents(rapid.Int64Range(0, 5000).Draw(t, "pct")),
			IncludeRolled: rapid.Bool().Draw(t, "rolled"),
		}}
		cat[id] = it
	}
	return cat
}

// TestProperty_SplitConservesUnitCost: for every rolled item, the components
// sum to the unit cost exactly and no component is negative.
func TestProperty_SplitConservesUnitCost(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		run := MassRoll(genDAG(t), RollOptions{})
		if run.ErrorCount != 0 {
			t.Fatalf("acyclic catalog produced %d errors: %+v", run.ErrorCount, run.Results)
		}
		for _, r := range run.Results {
			combined := r.Combined()
			if !r.Unit.Equal(combined.Total()) {
				t.Fatalf("%s: unit %s != split total %s", r.ItemID, r.Unit, combined.Total())
			}
			for c, v := range combined {
				if v.IsNegative() {
					t.Fatalf("%s: negative %s", r.ItemID, c)
				}
			}
		}
	})
}

// TestProperty_ParallelRollMatchesSequential: the 4-worker mass roll gives the
// same units and levels as a single worker.
func TestProperty_ParallelRollMatchesSequential(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		cat := genDAG(t)
		seq := MassRoll(cat, RollOptions{Workers: 1})
		par := MassRoll(cat, RollOptions{Workers: 4})
		if len(seq.Results) != len(par.Results) {
			t.Fatal("result counts differ")
		}
		for i := range seq.Results {
			a, b := seq.Results[i], par.Results[i]
			if a.ItemID != b.ItemID || a.Unit.String() != b.Unit.String() || a.Level != b.Level {
				t.Fatalf("%s differs: %s vs %s", a.ItemID, a.Unit, b.Unit)
			}
		}
	})
}

// TestMassRoll_FiftyThousandItemsEightLevels is the AC-39.3 shape: 50,000
// items over 8 levels, every item rolled, the run far inside 15 minutes.
func TestMassRoll_FiftyThousandItemsEightLevels(t *testing.T) {
	const perLevel, levels = 6250, 8
	id := func(l, i int) string { return fmt.Sprintf("L%d-%05d", l, i) }
	cat := make(Catalog, perLevel*levels)
	price := d("1.25")
	for i := 0; i < perLevel; i++ {
		cat[id(0, i)] = Item{ID: id(0, i), Kind: KindPurchased, Price: &price}
	}
	for l := 1; l < levels; l++ {
		for i := 0; i < perLevel; i++ {
			it := Item{ID: id(l, i), Kind: KindManufactured, CostingLot: d("10"),
				Operations: []Operation{{SetupHours: d("0.5"), RunHours: d("0.1"), Rate: d("40")}},
				Sheet:      []SheetNode{{ID: "OH", Type: NodeSurcharge, Component: CompIndMat, Basis: []string{CompDM}, Percentage: d("10")}}}
			for k := 0; k < 3; k++ {
				it.BOM = append(it.BOM, BOMLine{ChildID: id(l-1, (i+k)%perLevel), Qty: d("2")})
			}
			cat[id(l, i)] = it
		}
	}
	run := MassRoll(cat, RollOptions{MaxLevels: 20, Workers: 4})
	if run.ItemCount != perLevel*levels || run.ErrorCount != 0 {
		t.Fatalf("items %d errors %d", run.ItemCount, run.ErrorCount)
	}
	if run.DurationMs >= 15*60*1000 {
		t.Fatalf("mass roll took %d ms", run.DurationMs)
	}
	for _, r := range run.Results {
		var want int
		if _, err := fmt.Sscanf(r.ItemID, "L%d-", &want); err != nil || r.Level != want {
			t.Fatalf("%s level %d, want %d", r.ItemID, r.Level, want)
		}
	}
}
