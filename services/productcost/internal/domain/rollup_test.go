package domain

import (
	"fmt"
	"strings"
	"testing"

	"github.com/shopspring/decimal"
)

func d(s string) decimal.Decimal { return decimal.RequireFromString(s) }

func ptr(s string) *decimal.Decimal {
	v := d(s)
	return &v
}

// workedCatalog is the FR-39.4 two-level example: SA-200 under FG-100.
func workedCatalog() Catalog {
	return Catalog{
		"RM-301": {ID: "RM-301", Kind: KindPurchased, Price: ptr("2.50")},
		"RM-302": {ID: "RM-302", Kind: KindPurchased, Price: ptr("1.00")},
		"RM-300": {ID: "RM-300", Kind: KindPurchased, Price: ptr("4.00")},
		"SA-200": {
			ID: "SA-200", Kind: KindManufactured, CostingLot: d("1"),
			BOM: []BOMLine{
				{ChildID: "RM-301", Qty: d("3")},
				{ChildID: "RM-302", Qty: d("1"), ScrapPct: d("10")},
			},
			Operations: []Operation{{RunHours: d("0.25"), Rate: d("60")}},
			Sheet: []SheetNode{
				{ID: "OH-MAT", Type: NodeSurcharge, Component: CompIndMat, Basis: []string{CompDM}, Percentage: d("10")},
				{ID: "OH-LAB", Type: NodeSurcharge, Component: CompIndLab, Basis: []string{CompDL}, Percentage: d("50")},
			},
		},
		"FG-100": {
			ID: "FG-100", Kind: KindManufactured, CostingLot: d("10"),
			BOM: []BOMLine{
				{ChildID: "SA-200", Qty: d("2")},
				{ChildID: "RM-300", Qty: d("1")},
			},
			Operations: []Operation{{RunHours: d("0.5"), SetupHours: d("1"), Rate: d("40")}},
			Sheet: []SheetNode{
				{ID: "OH-MAT", Type: NodeSurcharge, Component: CompIndMat, Basis: []string{CompDM}, Percentage: d("10")},
				{ID: "OH-LAB", Type: NodeSurcharge, Component: CompIndLab, Basis: []string{CompDL}, Percentage: d("50")},
			},
		},
	}
}

func TestRollUp_WorkedExampleMatchesSpec(t *testing.T) {
	cases := []struct {
		item  string
		unit  string
		split map[string]string
	}{
		{"SA-200", "31.96", map[string]string{CompDM: "8.60", CompDL: "15.00", CompIndMat: "0.86", CompIndLab: "7.50"}},
		{"FG-100", "104.32", map[string]string{CompDM: "21.20", CompDL: "54.00", CompIndMat: "2.12", CompIndLab: "27.00"}},
	}
	run := MassRoll(workedCatalog(), RollOptions{})
	byID := map[string]Result{}
	for _, r := range run.Results {
		byID[r.ItemID] = r
	}
	for _, tc := range cases {
		r := byID[tc.item]
		if r.Status != StatusPending {
			t.Fatalf("%s status %s diagnostics %+v", tc.item, r.Status, r.Diagnostics)
		}
		if got := r.Unit.StringFixed(2); got != tc.unit {
			t.Errorf("%s unit = %s, want %s", tc.item, got, tc.unit)
		}
		combined := r.Combined()
		for c, want := range tc.split {
			if got := combined[c].StringFixed(2); got != want {
				t.Errorf("%s %s = %s, want %s", tc.item, c, got, want)
			}
		}
	}
	if r := byID["FG-100"]; r.Level != 2 || byID["SA-200"].Level != 1 || byID["RM-301"].Level != 0 {
		t.Errorf("levels FG=%d SA=%d RM=%d", r.Level, byID["SA-200"].Level, byID["RM-301"].Level)
	}
}

func TestRollUp_SubassemblyStaysRolledNotOwn(t *testing.T) {
	r, ok := RollOne(workedCatalog(), "FG-100", RollOptions{})
	if !ok {
		t.Fatal("FG-100 not rolled")
	}
	if got := r.Rolled[CompDM].StringFixed(2); got != "17.20" {
		t.Errorf("rolled DM = %s, want 17.20 carried from SA-200", got)
	}
	if got := r.Own[CompDM].StringFixed(2); got != "4.00" {
		t.Errorf("own DM = %s, want 4.00 (RM-300 only)", got)
	}
}

func TestRollUp_SingleLevelUsesActiveStandardOfChild(t *testing.T) {
	cat := workedCatalog()
	sa := cat["SA-200"]
	sa.ActiveSplit = Split{CompDM: d("9.00"), CompDL: d("16.00")}
	cat["SA-200"] = sa
	rm := cat["RM-300"]
	rm.ActiveSplit = Split{CompDM: d("4.00")}
	cat["RM-300"] = rm
	r, _ := RollOne(cat, "FG-100", RollOptions{Mode: ModeSingleLevel})
	if got := r.Rolled[CompDL].StringFixed(2); got != "32.00" {
		t.Errorf("single-level rolled DL = %s, want 2 x 16.00", got)
	}
}

func TestRollUp_CycleReportsPathAndBlocksOnlyCycle(t *testing.T) {
	cat := Catalog{
		"A":   {ID: "A", Kind: KindManufactured, BOM: []BOMLine{{ChildID: "B", Qty: d("1")}}},
		"B":   {ID: "B", Kind: KindManufactured, BOM: []BOMLine{{ChildID: "A", Qty: d("1")}}},
		"OK":  {ID: "OK", Kind: KindPurchased, Price: ptr("1.00")},
		"TOP": {ID: "TOP", Kind: KindManufactured, BOM: []BOMLine{{ChildID: "A", Qty: d("1")}}},
	}
	run := MassRoll(cat, RollOptions{})
	byID := map[string]Result{}
	for _, r := range run.Results {
		byID[r.ItemID] = r
	}
	if byID["OK"].Status != StatusPending {
		t.Errorf("unrelated item blocked: %+v", byID["OK"])
	}
	for _, id := range []string{"A", "B", "TOP"} {
		if byID[id].Status != StatusError {
			t.Errorf("%s status = %s, want Error", id, byID[id].Status)
		}
	}
	found := false
	for _, r := range run.Results {
		for _, dg := range r.Diagnostics {
			if dg.Code == CodeCycle && strings.Contains(dg.Message, "A -> B -> A") {
				found = true
			}
		}
	}
	if !found {
		t.Errorf("cycle path A -> B -> A not reported: %+v", run.Results)
	}
	if run.ErrorCount != 3 {
		t.Errorf("error count = %d, want 3", run.ErrorCount)
	}
}

func TestRollUp_MissingPriceFallbackRules(t *testing.T) {
	cat := Catalog{
		"P": {ID: "P", Kind: KindPurchased},
		"Q": {ID: "Q", Kind: KindPurchased, ActivePrice: ptr("3.00")},
	}
	strict, _ := RollOne(cat, "P", RollOptions{})
	if strict.Status != StatusError || strict.Diagnostics[0].Code != CodeNoPrice {
		t.Errorf("no fallback: %+v", strict)
	}
	fb, _ := RollOne(cat, "Q", RollOptions{FallbackToActive: true})
	if fb.Status != StatusPending || fb.Unit.StringFixed(2) != "3.00" {
		t.Errorf("fallback active: %+v", fb)
	}
	if len(fb.Diagnostics) != 1 || fb.Diagnostics[0].Code != CodeFallback || fb.Diagnostics[0].Severity != "warning" {
		t.Errorf("fallback not recorded: %+v", fb.Diagnostics)
	}
}

func TestRollUp_DepthLimitIsEnforced(t *testing.T) {
	cat := Catalog{"L0": {ID: "L0", Kind: KindPurchased, Price: ptr("1.00")}}
	for i := 1; i <= 4; i++ {
		id := fmt.Sprintf("L%d", i)
		cat[id] = Item{ID: id, Kind: KindManufactured, BOM: []BOMLine{{ChildID: fmt.Sprintf("L%d", i-1), Qty: d("1")}}}
	}
	run := MassRoll(cat, RollOptions{MaxLevels: 2})
	for _, r := range run.Results {
		deep := r.ItemID == "L3" || r.ItemID == "L4"
		if deep && r.Status != StatusError {
			t.Errorf("%s beyond the limit rolled", r.ItemID)
		}
		if !deep && r.Status != StatusPending {
			t.Errorf("%s within the limit failed: %+v", r.ItemID, r.Diagnostics)
		}
	}
}

func TestRollUp_ScrapCompoundsOnQuantity(t *testing.T) {
	cat := Catalog{
		"C": {ID: "C", Kind: KindPurchased, Price: ptr("2.00")},
		"P": {ID: "P", Kind: KindManufactured, BOM: []BOMLine{{ChildID: "C", Qty: d("5"), ScrapPct: d("20")}}},
	}
	r, _ := RollOne(cat, "P", RollOptions{})
	if r.Unit.StringFixed(2) != "12.00" {
		t.Errorf("5 x 1.20 x 2.00 = %s, want 12.00", r.Unit.StringFixed(2))
	}
}

func TestRollOptions_Validate(t *testing.T) {
	cases := []struct {
		name string
		opts RollOptions
		bad  bool
	}{
		{"defaults", RollOptions{}, false},
		{"max levels too high", RollOptions{MaxLevels: 41}, true},
		{"workers too low", RollOptions{Workers: -1}, true},
		{"unknown mode", RollOptions{Mode: "MakeToOrderX"}, true},
		{"single level", RollOptions{Mode: ModeSingleLevel, Workers: 8}, false},
	}
	for _, tc := range cases {
		if err := tc.opts.Validate(); (err != nil) != tc.bad {
			t.Errorf("%s: err = %v", tc.name, err)
		}
	}
}

func TestRollUp_RateNodesChargePerRoutingHourOrPerUnit(t *testing.T) {
	// 0.5 h run + 2 h setup over a lot of 10 = 0.7 routing hours per unit.
	cat := Catalog{
		"P": {ID: "P", Kind: KindManufactured, CostingLot: d("10"),
			Operations: []Operation{{RunHours: d("0.5"), SetupHours: d("2"), Rate: d("30")}},
			Sheet: []SheetNode{
				{ID: "MACH", Type: NodeRate, Component: CompIndLab, Rate: d("12"), RateUnit: "Hour"},
				{ID: "PACK", Type: NodeRate, Component: CompIndMat, Rate: d("0.75")},
			}},
	}
	r, _ := RollOne(cat, "P", RollOptions{})
	split := r.Combined()
	if !split[CompDL].Equal(d("21")) || !split[CompIndLab].Equal(d("8.4")) || !split[CompIndMat].Equal(d("0.75")) {
		t.Errorf("split = %v, want DL 21 (15 run + 6 setup), IND-LAB 8.40 (0.7 h x 12), IND-MAT 0.75", split)
	}
	if !r.Unit.Equal(d("30.15")) {
		t.Errorf("unit = %s, want 30.15", r.Unit)
	}
}
