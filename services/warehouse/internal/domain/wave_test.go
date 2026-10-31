package domain

import (
	"fmt"
	"strconv"
	"testing"

	"github.com/shopspring/decimal"
	"pgregory.net/rapid"
)

const waveWarehouse = "WH1"

func wavePickDirective() []Directive {
	return []Directive{{ID: "PICK-FWD", Sequence: 10, WorkType: "SALES_PICK", Direction: DirectionPick, Warehouse: waveWarehouse,
		Bands: []Band{{Seq: 1, FromQty: dec("0"), Actions: []Action{{Seq: 1, Strategy: StrategyConsolidate, Zone: "FWD"}}}}}}
}

func idSource() func() string {
	n := 0
	return func() string { n++; return "W" + strconv.Itoa(n) }
}

func stocked(id, zone string, pick int, item string, qty string) Location {
	l := loc(id, id, zone, pick)
	l.Stock = map[string]decimal.Decimal{item: dec(qty)}
	return l
}

// waveWarehouseFixture has 50 items; each has a forward location with 120 EA
// and two reserve locations (15 EA, then 5,000 EA).
func waveWarehouseFixture() []Location {
	var locs []Location
	for s := range 50 {
		item := fmt.Sprintf("SKU-%02d", s)
		locs = append(locs,
			stocked(fmt.Sprintf("F-%02d", s), "FWD", s, item, "120"),
			stocked(fmt.Sprintf("RA-%02d", s), "RES", 100+s, item, "15"),
			stocked(fmt.Sprintf("RB-%02d", s), "RES", 200+s, item, "5000"))
	}
	return locs
}

func waveRules() []ReplenishRule {
	var rules []ReplenishRule
	for s := range 50 {
		rules = append(rules, ReplenishRule{ItemID: fmt.Sprintf("SKU-%02d", s), PickZone: "FWD", ReserveZone: "RES", MinQty: dec("40"), MaxQty: dec("150")})
	}
	return rules
}

// TestReleaseWave_500Orders is Proof AC4: 500 orders create 500 pick works that
// each take the whole line from the item's forward location, and exactly the
// items whose forward stock the picks leave below the minimum are replenished
// up to the maximum, drawing the small reserve location before the large one.
func TestReleaseWave_500Orders(t *testing.T) {
	demand := map[string]int{}
	orders := make([]WaveOrder, 0, 500)
	for i := range 500 {
		item := fmt.Sprintf("SKU-%02d", i%50)
		qty := 1 + (i*7)%10
		demand[item] += qty
		orders = append(orders, WaveOrder{ID: fmt.Sprintf("SO-%03d", i), Lines: []NewLine{{ItemID: item, Direction: DirectionPick, Qty: decimal.NewFromInt(int64(qty))}}})
	}
	rel, err := ReleaseWave(waveWarehouse, idSource(), orders, waveRules(), wavePickDirective(), waveWarehouseFixture())
	if err != nil {
		t.Fatal(err)
	}
	if len(rel.Picks) != 500 {
		t.Fatalf("picks = %d, want 500", len(rel.Picks))
	}
	for i, w := range rel.Picks {
		l := w.Lines[0]
		want := fmt.Sprintf("F-%02d", i%50)
		if w.Status != WorkOpen || w.WorkType != "SALES_PICK" || l.FromLocationID != want || l.Direction != DirectionPick || !l.Qty.Equal(decimal.NewFromInt(int64(1+(i*7)%10))) {
			t.Fatalf("pick %d = %+v, want OPEN pick from %s", i, w, want)
		}
	}

	wantRepl := map[string]int{}
	for item, d := range demand {
		if left := 120 - d; left < 40 {
			wantRepl[item] = 150 - left
		}
	}
	if len(wantRepl) == 0 || len(wantRepl) == 50 {
		t.Fatalf("fixture must replenish some but not all items, got %d", len(wantRepl))
	}
	if len(rel.Replenishment) != len(wantRepl) {
		t.Fatalf("replenishments = %d, want %d", len(rel.Replenishment), len(wantRepl))
	}
	for _, w := range rel.Replenishment {
		item := w.Lines[0].ItemID
		need, ok := wantRepl[item]
		if !ok || w.WorkType != "REPLENISHMENT" || w.Priority != ReplenishPriority || w.Status != WorkOpen {
			t.Fatalf("unexpected replenishment %+v", w)
		}
		delete(wantRepl, item)
		sku := item[len("SKU-"):]
		fromSmall := dec("15")
		fromLarge := decimal.NewFromInt(int64(need)).Sub(fromSmall)
		got := []struct {
			dir  Direction
			at   string
			qty  decimal.Decimal
			desc string
		}{
			{DirectionPick, "RA-" + sku, fromSmall, "pick small reserve"}, {DirectionPut, "F-" + sku, fromSmall, "put"},
			{DirectionPick, "RB-" + sku, fromLarge, "pick large reserve"}, {DirectionPut, "F-" + sku, fromLarge, "put"}}
		if len(w.Lines) != len(got) {
			t.Fatalf("%s: %d lines, want %d", item, len(w.Lines), len(got))
		}
		for i, g := range got {
			l := w.Lines[i]
			if l.Direction != g.dir || l.LocationOf() != g.at || !l.Qty.Equal(g.qty) {
				t.Errorf("%s line %d (%s) = %s %s %s, want %s %s %s", item, i+1, g.desc, l.Direction, l.LocationOf(), l.Qty, g.dir, g.at, g.qty)
			}
		}
	}
	if len(wantRepl) != 0 {
		t.Fatalf("items never replenished: %v", wantRepl)
	}
}

func TestReleaseWave_SkipsItemsAtOrAboveMinimumAndRefillsLessWhenReserveShort(t *testing.T) {
	locs := []Location{stocked("F", "FWD", 1, "A", "50"), stocked("R", "RES", 2, "A", "30")}
	rules := []ReplenishRule{{ItemID: "A", PickZone: "FWD", ReserveZone: "RES", MinQty: dec("40"), MaxQty: dec("100")}}
	order := func(qty string) []WaveOrder {
		return []WaveOrder{{ID: "SO", Lines: []NewLine{{ItemID: "A", Direction: DirectionPick, Qty: dec(qty)}}}}
	}
	rel, err := ReleaseWave(waveWarehouse, idSource(), order("10"), rules, wavePickDirective(), locs)
	if err != nil || len(rel.Replenishment) != 0 {
		t.Fatalf("40 left is at the minimum: %+v err=%v", rel.Replenishment, err)
	}
	rel, err = ReleaseWave(waveWarehouse, idSource(), order("11"), rules, wavePickDirective(), locs)
	if err != nil || len(rel.Replenishment) != 1 {
		t.Fatalf("39 left must replenish: %+v err=%v", rel.Replenishment, err)
	}
	if got := rel.Replenishment[0].Lines[0].Qty; !got.Equal(dec("30")) {
		t.Fatalf("reserve holds 30 of the 61 wanted, refill = %s", got)
	}
}

func TestReleaseWave_RejectsBadInput(t *testing.T) {
	order := []WaveOrder{{ID: "SO", Lines: []NewLine{{ItemID: "A", Direction: DirectionPick, Qty: dec("1")}}}}
	bad := []ReplenishRule{{ItemID: "A", PickZone: "FWD", ReserveZone: "RES", MinQty: dec("10"), MaxQty: dec("10")}}
	if _, err := ReleaseWave(waveWarehouse, idSource(), order, bad, nil, nil); err == nil {
		t.Error("max equal to min must be refused")
	}
	if _, err := ReleaseWave(waveWarehouse, idSource(), nil, nil, nil, nil); err == nil {
		t.Error("an empty wave must be refused")
	}
}

// TestReleaseWave_NeverOverdrawsAnyLocation is a property over random waves:
// across the pick and replenishment work together, no location is asked for
// more than it holds, and a forward location never ends above the maximum.
func TestReleaseWave_NeverOverdrawsAnyLocation(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		items := rapid.IntRange(1, 6).Draw(t, "items")
		var locs []Location
		var rules []ReplenishRule
		for s := range items {
			item := fmt.Sprintf("I%d", s)
			locs = append(locs,
				stocked(fmt.Sprintf("F%d", s), "FWD", s, item, strconv.Itoa(rapid.IntRange(1, 200).Draw(t, "fwd"))),
				stocked(fmt.Sprintf("R%d", s), "RES", 100+s, item, strconv.Itoa(rapid.IntRange(1, 300).Draw(t, "res"))))
			rules = append(rules, ReplenishRule{ItemID: item, PickZone: "FWD", ReserveZone: "RES", MinQty: dec("20"), MaxQty: dec("120")})
		}
		var orders []WaveOrder
		for i := range rapid.IntRange(1, 60).Draw(t, "orders") {
			var lines []NewLine
			for range rapid.IntRange(1, 3).Draw(t, "lines") {
				lines = append(lines, NewLine{ItemID: fmt.Sprintf("I%d", rapid.IntRange(0, items-1).Draw(t, "item")), Direction: DirectionPick,
					Qty: decimal.NewFromInt(rapid.Int64Range(1, 40).Draw(t, "qty"))})
			}
			orders = append(orders, WaveOrder{ID: strconv.Itoa(i), Lines: lines})
		}
		rel, err := ReleaseWave(waveWarehouse, idSource(), orders, rules, wavePickDirective(), locs)
		if err != nil {
			t.Fatal(err)
		}
		taken := map[string]decimal.Decimal{}
		put := map[string]decimal.Decimal{}
		for _, w := range append(append([]Work{}, rel.Picks...), rel.Replenishment...) {
			for _, l := range w.Lines {
				if l.Direction == DirectionPick && l.FromLocationID != "" {
					taken[l.FromLocationID] = taken[l.FromLocationID].Add(l.Qty)
				}
				if l.Direction == DirectionPut {
					put[l.ToLocationID] = put[l.ToLocationID].Add(l.Qty)
				}
			}
		}
		for _, l := range locs {
			item := ""
			for k := range l.Stock {
				item = k
			}
			if taken[l.ID].GreaterThan(l.Stock[item]) {
				t.Fatalf("%s asked for %s of %s", l.ID, taken[l.ID], l.Stock[item])
			}
			if l.Zone == "FWD" && l.Stock[item].Sub(taken[l.ID]).Add(put[l.ID]).GreaterThan(dec("120")) && put[l.ID].IsPositive() {
				t.Fatalf("%s ends above the maximum: %s", l.ID, l.Stock[item].Sub(taken[l.ID]).Add(put[l.ID]))
			}
		}
	})
}
