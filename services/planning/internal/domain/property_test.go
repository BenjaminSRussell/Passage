package domain

import (
	"fmt"
	"testing"

	"github.com/shopspring/decimal"
	"pgregory.net/rapid"
)

// genItems draws a random single-level snapshot: a few items, each with demand
// spread over 30 days and a random coverage code and lot rule.
func genItems(t *rapid.T) []Item {
	n := rapid.IntRange(1, 6).Draw(t, "items")
	items := make([]Item, 0, n)
	for i := 0; i < n; i++ {
		id := fmt.Sprintf("I-%02d", i)
		minQty := rapid.Int64Range(0, 50).Draw(t, "min")
		it := Item{
			ID:           id,
			Coverage:     rapid.SampledFrom([]string{CoverageRequirement, CoveragePeriod, CoverageMinMax}).Draw(t, "coverage"),
			PeriodDays:   rapid.IntRange(0, 10).Draw(t, "periodDays"),
			MarginDays:   rapid.IntRange(0, 3).Draw(t, "marginDays"),
			PositiveDays: rapid.IntRange(0, 5).Draw(t, "positiveDays"),
			NegativeDays: rapid.IntRange(0, 5).Draw(t, "negativeDays"),
			ReorderMin:   decimal.NewFromInt(minQty),
			ReorderMax:   decimal.NewFromInt(minQty + rapid.Int64Range(0, 100).Draw(t, "extra")),
			OnHand:       decimal.NewFromInt(rapid.Int64Range(0, 200).Draw(t, "onHand")),
			SafetyStock:  decimal.NewFromInt(rapid.Int64Range(0, 30).Draw(t, "safety")),
			OrderType:    OrderPurchase,
			LeadTimeDays: rapid.IntRange(0, 10).Draw(t, "leadTime"),
			MinOrderQty:  decimal.NewFromInt(rapid.Int64Range(0, 40).Draw(t, "minOrder")),
			MultipleQty:  decimal.NewFromInt(rapid.Int64Range(0, 25).Draw(t, "multiple")),
			MaxOrderQty:  decimal.NewFromInt(rapid.Int64Range(0, 200).Draw(t, "maxOrder")),
		}
		ndem := rapid.IntRange(0, 4).Draw(t, "demands")
		for k := 0; k < ndem; k++ {
			it.Demand = append(it.Demand, Requirement{
				Ref:      fmt.Sprintf("SO-%s-%d", id, k),
				RefType:  RefSales,
				Date:     rapid.IntRange(0, 30).Draw(t, "demandDate"),
				Qty:      decimal.NewFromInt(rapid.Int64Range(1, 50).Draw(t, "demandQty")),
				Priority: PrioritySales,
			})
		}
		nrec := rapid.IntRange(0, 2).Draw(t, "receipts")
		for k := 0; k < nrec; k++ {
			it.Receipts = append(it.Receipts, Requirement{
				Ref:     fmt.Sprintf("PO-%s-%d", id, k),
				RefType: "purchase-line",
				Date:    rapid.IntRange(0, 30).Draw(t, "receiptDate"),
				Qty:     decimal.NewFromInt(rapid.Int64Range(1, 50).Draw(t, "receiptQty")),
			})
		}
		items = append(items, it)
	}
	return items
}

// TestRun_PropertyDeterministic: identical input gives an identical plan hash
// and identical orders on every run (FR-22.14).
func TestRun_PropertyDeterministic(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		items := genItems(t)
		a, err := Run(0, items)
		if err != nil {
			t.Skip(err)
		}
		b, err := Run(0, items)
		if err != nil {
			t.Fatal(err)
		}
		if a.Hash != b.Hash || len(a.Orders) != len(b.Orders) {
			t.Fatalf("hash %s != %s", a.Hash, b.Hash)
		}
	})
}

// TestRun_PropertyPeggingExplainsSupply: every planned order is fully pegged
// (AC3 / AC-22.2) and no demand is pegged beyond its quantity (invariant of 4.22.4).
func TestRun_PropertyPeggingExplainsSupply(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		items := genItems(t)
		r, err := Run(0, items)
		if err != nil {
			t.Skip(err)
		}
		supplied := map[string]decimal.Decimal{}
		demanded := map[string]decimal.Decimal{}
		for _, p := range r.Pegging {
			supplied[p.SupplyRef] = supplied[p.SupplyRef].Add(p.Qty)
			if p.DemandRefType != RefSurplus {
				demanded[p.DemandRef] = demanded[p.DemandRef].Add(p.Qty)
			}
		}
		for _, o := range r.Orders {
			if !supplied[o.Number].Equal(o.Qty) {
				t.Fatalf("order %s qty %s pegged %s", o.Number, o.Qty, supplied[o.Number])
			}
		}
		for _, it := range items {
			for _, d := range it.Demand {
				if demanded[d.Ref].GreaterThan(d.Qty) {
					t.Fatalf("demand %s qty %s pegged %s", d.Ref, d.Qty, demanded[d.Ref])
				}
			}
		}
	})
}

// TestRun_PropertyDemandIsFullyCovered: with any coverage code except manual,
// the plan leaves no demand short: every demand (and the safety stock) is
// pegged to supply for its whole quantity (FR-22.3, invariant of 4.22.4).
func TestRun_PropertyDemandIsFullyCovered(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		items := genItems(t)
		r, err := Run(0, items)
		if err != nil {
			t.Skip(err)
		}
		covered := map[string]decimal.Decimal{}
		for _, p := range r.Pegging {
			covered[p.DemandRef] = covered[p.DemandRef].Add(p.Qty)
		}
		for _, it := range items {
			for _, d := range it.Demand {
				if !covered[d.Ref].Equal(d.Qty) {
					t.Fatalf("%s coverage %s: demand %s qty %s covered %s", it.ID, it.Coverage, d.Ref, d.Qty, covered[d.Ref])
				}
			}
			if it.SafetyStock.IsPositive() {
				ref := RefSafety + ":" + it.ID
				if !covered[ref].Equal(it.SafetyStock) {
					t.Fatalf("%s safety stock %s covered %s", it.ID, it.SafetyStock, covered[ref])
				}
			}
		}
	})
}

// genBOM draws a random multi-level snapshot: item i may use items with a
// higher index as components, so the BOM is acyclic and up to 4 levels deep.
func genBOM(t *rapid.T) []Item {
	n := rapid.IntRange(2, 5).Draw(t, "bomItems")
	items := make([]Item, n)
	for i := range items {
		id := fmt.Sprintf("B-%d", i)
		it := Item{
			ID:           id,
			Coverage:     rapid.SampledFrom([]string{CoverageRequirement, CoveragePeriod}).Draw(t, "coverage"),
			PeriodDays:   rapid.IntRange(0, 7).Draw(t, "periodDays"),
			MarginDays:   rapid.IntRange(0, 2).Draw(t, "marginDays"),
			OnHand:       decimal.NewFromInt(rapid.Int64Range(0, 60).Draw(t, "onHand")),
			OrderType:    OrderPurchase,
			LeadTimeDays: rapid.IntRange(0, 12).Draw(t, "leadTime"),
			MinOrderQty:  decimal.NewFromInt(rapid.Int64Range(0, 40).Draw(t, "minOrder")),
			MultipleQty:  decimal.NewFromInt(rapid.Int64Range(0, 10).Draw(t, "multiple")),
		}
		for j := i + 1; j < n; j++ {
			if rapid.Bool().Draw(t, "uses") {
				it.OrderType = OrderProduction
				it.Components = append(it.Components, BOMLine{ItemID: fmt.Sprintf("B-%d", j), Qty: decimal.NewFromInt(rapid.Int64Range(1, 3).Draw(t, "per"))})
			}
		}
		for k := rapid.IntRange(0, 3).Draw(t, "sales"); k > 0; k-- {
			it.Demand = append(it.Demand, Requirement{Ref: fmt.Sprintf("SO-%s-%d", id, k), RefType: RefSales,
				Date: rapid.IntRange(0, 30).Draw(t, "date"), Qty: decimal.NewFromInt(rapid.Int64Range(1, 50).Draw(t, "qty")), Priority: PrioritySales})
		}
		items[i] = it
	}
	return items
}

// TestRun_PropertyMultiLevel: through any acyclic BOM, every planned order is
// fully pegged, all demand (dependent demand included) is covered, the hash is
// stable, and a parent never finishes before a late component it is pegged to
// allows (BR-22.2 propagation).
func TestRun_PropertyMultiLevel(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		items := genBOM(t)
		today := rapid.IntRange(0, 10).Draw(t, "today")
		r, err := Run(today, items)
		if err != nil {
			t.Fatal(err)
		}
		again, err := Run(today, items)
		if err != nil || again.Hash != r.Hash {
			t.Fatalf("hash not stable: %v", err)
		}
		index := map[string]Item{}
		for _, it := range items {
			index[it.ID] = it
		}
		orders := map[string]PlannedOrder{}
		for _, o := range r.Orders {
			orders[o.Number] = o
		}
		supplied := map[string]decimal.Decimal{}
		demanded := map[string]decimal.Decimal{}
		for _, p := range r.Pegging {
			supplied[p.SupplyRef] = supplied[p.SupplyRef].Add(p.Qty)
			if p.DemandRefType != RefSurplus {
				demanded[p.DemandRef] = demanded[p.DemandRef].Add(p.Qty)
			}
		}
		for _, o := range r.Orders {
			if !supplied[o.Number].Equal(o.Qty) {
				t.Fatalf("order %s qty %s pegged %s", o.Number, o.Qty, supplied[o.Number])
			}
			if o.OrderType != OrderProduction {
				continue
			}
			for _, c := range index[o.ItemID].Components {
				ref := o.Number + "/" + c.ItemID
				if want := c.Qty.Mul(o.Qty); !demanded[ref].Equal(want) {
					t.Fatalf("dependent demand %s qty %s covered %s", ref, want, demanded[ref])
				}
			}
		}
		for _, it := range items {
			for _, d := range it.Demand {
				if !demanded[d.Ref].Equal(d.Qty) {
					t.Fatalf("demand %s qty %s covered %s", d.Ref, d.Qty, demanded[d.Ref])
				}
			}
		}
		effective := func(o PlannedOrder) int {
			if o.FuturesDate != nil {
				return *o.FuturesDate
			}
			return o.DeliveryDate
		}
		for _, p := range r.Pegging {
			if p.DemandRefType != RefBOM {
				continue
			}
			child, okChild := orders[p.SupplyRef]
			parent, okParent := orders[driverOf(p.DemandRef)]
			if !okChild || !okParent || child.FuturesDate == nil {
				continue
			}
			ready := *child.FuturesDate + index[child.ItemID].MarginDays
			if earliest := ready + index[parent.ItemID].LeadTimeDays; ready > p.DemandDate && effective(parent) < earliest {
				t.Fatalf("parent %s finishes day %d before its late component %s allows (%d)", parent.Number, effective(parent), child.Number, earliest)
			}
		}
	})
}
