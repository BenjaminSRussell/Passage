package domain

import (
	"sort"
	"strings"
	"testing"

	"github.com/shopspring/decimal"
)

const (
	itemA = "00000000-0000-0000-0000-00000000000a"
	itemB = "00000000-0000-0000-0000-00000000000b"
	itemC = "00000000-0000-0000-0000-00000000000c"
)

// sortedByItem returns the lines in item order, the order of the cells.
func sortedByItem(lines []AllocationLine) []AllocationLine {
	out := append([]AllocationLine(nil), lines...)
	sort.Slice(out, func(i, j int) bool { return out[i].ItemID < out[j].ItemID })
	return out
}

func line(item, share, factor string) AllocationLine {
	return AllocationLine{ItemID: item, Share: dec(share), UomFactor: dec(factor)}
}

func TestAllocateForecast_SplitsByShareAndConverts(t *testing.T) {
	// 100 cases split 50/30/20; item B holds 12 inventory units per case.
	cells, ok := AllocateForecast(dec("100"), []AllocationLine{line(itemC, "20", "1"), line(itemA, "50", "1"), line(itemB, "30", "12")})
	if !ok {
		t.Fatal("not ok")
	}
	want := []struct{ item, qty, inv string }{{itemA, "50", "50"}, {itemB, "30", "360"}, {itemC, "20", "20"}}
	for i, w := range want {
		if cells[i].ItemID != w.item || !cells[i].Qty.Equal(dec(w.qty)) || !cells[i].InventoryQty.Equal(dec(w.inv)) {
			t.Errorf("cell %d = %+v, want %+v", i, cells[i], w)
		}
	}
}

func TestAllocateForecast_RemainderGoesToLargestShare(t *testing.T) {
	// 100 split three ways of 33.333333% leaves 0.000001 after cutting to 6 decimals.
	lines := []AllocationLine{line(itemA, "33.333333", "1"), line(itemB, "33.333334", "1"), line(itemC, "33.333333", "1")}
	cells, _ := AllocateForecast(dec("100"), lines)
	if !cells[1].Qty.Equal(dec("33.333334")) {
		t.Errorf("largest share got %s", cells[1].Qty)
	}
	sum := decimal.Zero
	for _, c := range cells {
		sum = sum.Add(c.Qty)
	}
	if !sum.Equal(dec("100")) {
		t.Errorf("parts add up to %s", sum)
	}
}

func TestAllocateForecast_TieGoesToLowestItem(t *testing.T) {
	cells, _ := AllocateForecast(dec("0.000001"), []AllocationLine{line(itemB, "50", "1"), line(itemA, "50", "1")})
	if !cells[0].Qty.Equal(dec("0.000001")) || !cells[1].Qty.IsZero() {
		t.Errorf("cells = %+v", cells)
	}
}

func TestAllocateForecast_PartsAlwaysAddUp(t *testing.T) {
	// A property over odd quantities and share sets: the parts reconcile to the aggregate.
	qtys := []string{"0", "1", "0.000001", "7", "999.999999", "123456789.123457"}
	sets := [][]AllocationLine{
		{line(itemA, "100", "1")},
		{line(itemA, "12.5", "2"), line(itemB, "87.5", "0.5")},
		{line(itemA, "33.333333", "1"), line(itemB, "33.333333", "1"), line(itemC, "33.333334", "1")},
		{line(itemA, "0.000001", "1"), line(itemB, "99.999999", "1")},
		{line(itemC, "33.333334", "0.5"), line(itemB, "33.333333", "0.5"), line(itemA, "33.333333", "0.5")},
		{line(itemA, "20", "0.333333333"), line(itemB, "30", "1"), line(itemC, "50", "0.333333333")},
	}
	for _, q := range qtys {
		for _, set := range sets {
			cells, ok := AllocateForecast(dec(q), set)
			if !ok {
				t.Fatalf("%s not ok", q)
			}
			sum := decimal.Zero
			for _, c := range cells {
				if c.Qty.IsNegative() {
					t.Fatalf("negative part %s", c.Qty)
				}
				sum = sum.Add(c.Qty)
			}
			if !sum.Equal(dec(q)) {
				t.Errorf("qty %s: parts add up to %s", q, sum)
			}
			// Per factor, the converted quantities add up to the group's parts times the
			// factor rounded to 6 decimals, and each stays within 0.000001 per line of its
			// part times the factor; a factor of 1 converts each part to itself.
			byFactor := map[string][2]decimal.Decimal{}
			for i, c := range cells {
				l := sortedByItem(set)[i]
				exact := c.Qty.Mul(l.UomFactor)
				if c.InventoryQty.Sub(exact).Abs().GreaterThan(dec("0.000001").Mul(decimal.NewFromInt(int64(len(set))))) {
					t.Errorf("qty %s item %s: inventory %s far from %s", q, c.ItemID, c.InventoryQty, exact)
				}
				if l.UomFactor.Equal(decimal.NewFromInt(1)) && !c.InventoryQty.Equal(c.Qty) {
					t.Errorf("qty %s item %s: factor 1 converts %s to %s", q, c.ItemID, c.Qty, c.InventoryQty)
				}
				g := byFactor[l.UomFactor.String()]
				byFactor[l.UomFactor.String()] = [2]decimal.Decimal{g[0].Add(c.InventoryQty), g[1].Add(exact)}
			}
			for f, g := range byFactor {
				if !g[0].Equal(g[1].Round(6)) {
					t.Errorf("qty %s factor %s: converted parts add up to %s, exact total %s", q, f, g[0], g[1])
				}
			}
		}
	}
}

func TestAllocateForecast_ConversionKeepsTheSum(t *testing.T) {
	// A third each of 1 case at 0.5 units a case: rounding each part on its own gives
	// 0.166667 three times (0.500001); the converted parts must add up to 0.5.
	lines := []AllocationLine{line(itemA, "33.333333", "0.5"), line(itemB, "33.333333", "0.5"), line(itemC, "33.333334", "0.5")}
	cells, ok := AllocateForecast(dec("1"), lines)
	if !ok {
		t.Fatal("not ok")
	}
	want := []string{"0.166666", "0.166666", "0.166668"}
	sum := decimal.Zero
	for i, c := range cells {
		sum = sum.Add(c.InventoryQty)
		if !c.InventoryQty.Equal(dec(want[i])) {
			t.Errorf("cell %d inventory qty %s, want %s", i, c.InventoryQty, want[i])
		}
	}
	if !sum.Equal(dec("0.5")) {
		t.Errorf("converted parts add up to %s, want 0.5", sum)
	}
}

func TestAllocateForecast_InventoryTieGoesToLowestItemOfTheFactor(t *testing.T) {
	// Items B and C share the factor 0.5 and an equal share; A has the factor 1 and the
	// largest share. 0.000004 cases split 0.000002/0.000001/0.000001 exactly; B and C
	// convert to 0.0000005 units each, cut to 0, and their group's total 0.000001 goes
	// to B, the lower item of the group, not to A.
	lines := []AllocationLine{line(itemC, "25", "0.5"), line(itemA, "50", "1"), line(itemB, "25", "0.5")}
	cells, _ := AllocateForecast(dec("0.000004"), lines)
	if !cells[0].Qty.Equal(dec("0.000002")) || !cells[1].Qty.Equal(dec("0.000001")) || !cells[2].Qty.Equal(dec("0.000001")) {
		t.Fatalf("parts = %+v", cells)
	}
	if !cells[0].InventoryQty.Equal(dec("0.000002")) || !cells[1].InventoryQty.Equal(dec("0.000001")) || !cells[2].InventoryQty.IsZero() {
		t.Errorf("inventory = %s %s %s", cells[0].InventoryQty, cells[1].InventoryQty, cells[2].InventoryQty)
	}
}

func TestAllocateForecast_OverflowIsNotOK(t *testing.T) {
	if _, ok := AllocateForecast(dec("999999999999"), []AllocationLine{line(itemA, "100", "2")}); ok {
		t.Error("a converted quantity above the largest storable one must not be ok")
	}
}

func TestAllocationKeySpec_Validate(t *testing.T) {
	good := AllocationKeySpec{Code: "FAM-A", Name: "Family A", ForecastUom: "CASE", Lines: []AllocationLine{line(itemA, "60", "1"), line(itemB, "40", "6")}}
	if errs := good.Validate(); len(errs) != 0 {
		t.Fatalf("valid key refused: %v", errs)
	}
	cases := map[string]func(k *AllocationKeySpec){
		"code":                 func(k *AllocationKeySpec) { k.Code = "a" },
		"name":                 func(k *AllocationKeySpec) { k.Name = "" },
		"forecastUom":          func(k *AllocationKeySpec) { k.ForecastUom = "too long uom!" },
		"lines":                func(k *AllocationKeySpec) { k.Lines = nil },
		"lines[0].itemId":      func(k *AllocationKeySpec) { k.Lines[0].ItemID = "x" },
		"lines[1].itemId":      func(k *AllocationKeySpec) { k.Lines[1].ItemID = itemA },
		"lines[0].share":       func(k *AllocationKeySpec) { k.Lines[0].Share = dec("0") },
		"lines[1].uomFactor":   func(k *AllocationKeySpec) { k.Lines[1].UomFactor = dec("0.0000000001") },
		"lines[0].uomFactor":   func(k *AllocationKeySpec) { k.Lines[0].UomFactor = dec("-1") },
		"lines[0].share (cap)": func(k *AllocationKeySpec) { k.Lines[0].Share = dec("100.5") },
	}
	for field, mutate := range cases {
		k := good
		k.Lines = append([]AllocationLine(nil), good.Lines...)
		mutate(&k)
		errs := k.Validate()
		if _, ok := errs[strings.Fields(field)[0]]; !ok {
			t.Errorf("%s: no error in %v", field, errs)
		}
	}
	short := good
	short.Lines = []AllocationLine{line(itemA, "60", "1"), line(itemB, "39.999999", "1")}
	if msg := short.Validate()["lines"]; !strings.Contains(msg, "add up to 100") {
		t.Errorf("shares not summing to 100: %q", msg)
	}
}
