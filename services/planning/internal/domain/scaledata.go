package domain

import (
	"fmt"
	"math/rand/v2"

	"github.com/shopspring/decimal"
)

// ScaleSnapshot generates the mrp-50k dataset of the PRD test plan (issue #22,
// AC2 and AC-22.3) at n items (a multiple of 20): a 5-level BOM holding 10%,
// 20%, 20%, 25% and 25% of the items, 4n sales lines over 12 monthly periods,
// open purchase orders, and a mix of coverage codes and lot rules; n = 50,000
// gives 200,000 sales lines. The generator is seeded, so every call with the
// same n returns the same snapshot. It is test data: the engine scale test
// and the opt-in API scale test over Postgres (api/scale_pg_test.go) plan it.
func ScaleSnapshot(n int) []Item {
	rng := rand.New(rand.NewPCG(22, 2026))
	levels := []int{n / 10, n / 5, n / 5, n / 4, n / 4}
	ids := make([][]string, len(levels))
	var items []Item
	for lvl, n := range levels {
		for i := 0; i < n; i++ {
			ids[lvl] = append(ids[lvl], fmt.Sprintf("L%d-%05d", lvl, i))
		}
	}
	qty := func(lo, hi int64) decimal.Decimal { return decimal.NewFromInt(lo + rng.Int64N(hi-lo+1)) }
	for lvl := range levels {
		for i, id := range ids[lvl] {
			it := Item{ID: id, OrderType: OrderPurchase, LeadTimeDays: rng.IntN(21), OnHand: qty(0, 400)}
			switch r := rng.IntN(100); {
			case r < 55:
				it.Coverage = CoverageRequirement
			case r < 80:
				it.Coverage, it.PeriodDays = CoveragePeriod, 7+rng.IntN(24)
			default:
				it.Coverage = CoverageMinMax
				it.ReorderMin = qty(10, 200)
				it.ReorderMax = it.ReorderMin.Add(qty(50, 800))
			}
			if rng.IntN(4) == 0 && it.Coverage != CoverageMinMax {
				it.SafetyStock = qty(5, 100)
			}
			switch rng.IntN(5) {
			case 0:
				it.MinOrderQty = qty(10, 300)
			case 1:
				it.MultipleQty = qty(5, 50)
			case 2:
				it.MaxOrderQty = qty(2000, 20000)
			}
			if lvl+1 < len(levels) {
				it.OrderType, it.LeadTimeDays = OrderProduction, 1+rng.IntN(10)
				used := map[string]bool{}
				add := func(c string) {
					if !used[c] {
						used[c] = true
						it.Components = append(it.Components, BOMLine{ItemID: c, Qty: qty(1, 3)})
					}
				}
				// The items of the next level are dealt out over this level in
				// order, so every one of them has a parent and its low-level
				// code is its level; the other components are random, some two
				// levels down.
				next := ids[lvl+1]
				for j := (i*len(next) + len(ids[lvl]) - 1) / len(ids[lvl]); j < len(next) && j*len(ids[lvl])/len(next) == i; j++ {
					add(next[j])
				}
				for n := 2 + rng.IntN(3); len(it.Components) < n; {
					c := next[rng.IntN(len(next))]
					if lvl+2 < len(levels) && rng.IntN(5) == 0 {
						c = ids[lvl+2][rng.IntN(len(ids[lvl+2]))]
					}
					add(c)
				}
			}
			if rng.IntN(5) == 0 {
				it.Receipts = append(it.Receipts, Requirement{Ref: "PO-" + id, RefType: "purchase-line", Date: rng.IntN(90), Qty: qty(10, 500)})
			}
			items = append(items, it)
		}
	}
	index := make(map[string]int, len(items))
	for i := range items {
		index[items[i].ID] = i
	}
	for k := 0; k < 4*n; k++ {
		var id string
		if rng.IntN(10) < 7 {
			id = ids[0][rng.IntN(levels[0])]
		} else {
			lvl := 1 + rng.IntN(len(levels)-1)
			id = ids[lvl][rng.IntN(levels[lvl])]
		}
		it := &items[index[id]]
		it.Demand = append(it.Demand, Requirement{Ref: fmt.Sprintf("SO-%06d", k), RefType: RefSales, Date: 30*rng.IntN(12) + rng.IntN(30), Qty: qty(1, 100), Priority: PrioritySales})
	}
	return items
}
