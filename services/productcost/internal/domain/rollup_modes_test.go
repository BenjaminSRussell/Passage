package domain

import (
	"testing"

	"github.com/shopspring/decimal"
)

func sptr(v decimal.Decimal) *decimal.Decimal { return &v }

// TestRollUp_ExplosionModes checks which BOM lines are exploded per mode.
// P = M (manufactured, flagged make-to-order, 12.00 of DL when exploded, active
// standard 11.00) + X (purchased, version price 2.00, active price 2.10).
func TestRollUp_ExplosionModes(t *testing.T) {
	cat := Catalog{
		"P": {ID: "P", Kind: KindManufactured, BOM: []BOMLine{
			{ChildID: "M", Qty: d("1"), MakeToOrder: true},
			{ChildID: "X", Qty: d("1")},
		}},
		"M": {ID: "M", Kind: KindManufactured, CostingLot: d("1"),
			Operations:  []Operation{{RunHours: d("1"), Rate: d("12")}},
			ActiveSplit: Split{CompDL: d("11")}},
		"X": {ID: "X", Kind: KindPurchased, Price: sptr(d("2")), ActivePrice: sptr(d("2.10"))},
	}
	cases := []struct {
		mode ExplosionMode
		want string
	}{
		{ModeMultilevel, "14"},    // M rolled from its operations, X at its version price
		{ModeMakeToOrder, "14"},   // M is flagged make-to-order, so it is exploded
		{ModeByLineType, "13"},    // only phantoms explode; M takes its standard, X its version price
		{ModeSingleLevel, "13.1"}, // every child at its active standard
	}
	for _, tc := range cases {
		r, ok := RollOne(cat, "P", RollOptions{Mode: tc.mode})
		if !ok || r.Status != StatusPending {
			t.Fatalf("%s: roll failed: %+v", tc.mode, r.Diagnostics)
		}
		if !r.Unit.Equal(d(tc.want)) {
			t.Errorf("%s: unit = %s, want %s", tc.mode, r.Unit, tc.want)
		}
	}
}

// TestRollUp_PhantomExplodesInByLineType checks that a phantom line is
// exploded even when the child carries a different active standard.
func TestRollUp_PhantomExplodesInByLineType(t *testing.T) {
	cat := Catalog{
		"P": {ID: "P", Kind: KindManufactured, BOM: []BOMLine{{ChildID: "H", Qty: d("2"), Phantom: true}}},
		"H": {ID: "H", Kind: KindManufactured, CostingLot: d("1"),
			BOM:         []BOMLine{{ChildID: "X", Qty: d("1")}},
			ActiveSplit: Split{CompDL: d("99")}},
		"X": {ID: "X", Kind: KindPurchased, Price: sptr(d("3"))},
	}
	r, ok := RollOne(cat, "P", RollOptions{Mode: ModeByLineType})
	if !ok || !r.Unit.Equal(d("6")) {
		t.Fatalf("phantom not exploded in place: ok=%v unit=%s diags=%v", ok, r.Unit, r.Diagnostics)
	}
}

// TestRollUp_ScrapCompoundsBackward checks BR-39.6: an operation's scrap
// multiplies the quantity of every earlier operation in the route.
func TestRollUp_ScrapCompoundsBackward(t *testing.T) {
	cases := []struct {
		name string
		ops  []Operation
		want string
		hrs  string
	}{
		{
			name: "two ten percent operations",
			ops: []Operation{
				{RunHours: d("1"), Rate: d("10"), ScrapPct: d("10")},
				{RunHours: d("1"), Rate: d("10"), ScrapPct: d("10")},
			},
			want: "23.10", // 1.21 × 10 + 1.10 × 10
			hrs:  "2.31",
		},
		{
			name: "scrap on the first operation only",
			ops: []Operation{
				{RunHours: d("1"), Rate: d("10"), ScrapPct: d("10")},
				{RunHours: d("2"), Rate: d("10")},
			},
			want: "31.00", // 1.1 × 10 + 2 × 10
			hrs:  "3.10",
		},
		{
			name: "setup is per lot and is not scaled by scrap",
			ops:  []Operation{{SetupHours: d("1"), Rate: d("10"), LotSize: d("10"), ScrapPct: d("10")}},
			want: "1.00",
			hrs:  "0.1", // setup hours over the lot count as routing hours (BR-39.5)
		},
	}
	for _, tc := range cases {
		cat := Catalog{"S": {ID: "S", Kind: KindManufactured, CostingLot: d("1"), Operations: tc.ops,
			Sheet: []SheetNode{{ID: "R", Type: NodeRate, Component: CompIndLab, Rate: d("1"), RateUnit: "Hour"}}}}
		r, ok := RollOne(cat, "S", RollOptions{})
		if !ok || r.Status != StatusPending {
			t.Fatalf("%s: roll failed: %+v", tc.name, r.Diagnostics)
		}
		if !r.Own[CompIndLab].Equal(d(tc.hrs)) {
			t.Errorf("%s: hour driver = %s, want %s", tc.name, r.Own[CompIndLab], tc.hrs)
		}
		if !r.Unit.Equal(d(tc.want).Add(d(tc.hrs))) {
			t.Errorf("%s: unit = %s, want %s", tc.name, r.Unit, d(tc.want).Add(d(tc.hrs)))
		}
	}
}

func TestRollOptions_ValidateModes(t *testing.T) {
	for _, m := range []ExplosionMode{ModeMultilevel, ModeSingleLevel, ModeMakeToOrder, ModeByLineType} {
		if err := (RollOptions{Mode: m}).Validate(); err != nil {
			t.Errorf("%s rejected: %v", m, err)
		}
	}
	if err := (RollOptions{Mode: "Bogus"}).Validate(); err == nil {
		t.Error("unknown mode accepted")
	}
}

// TestRollUp_SingleLevelNeedsAnActiveStandard: a child without an active
// standard fails the parent with CST-0702 in single-level mode, and an active
// price is enough for a purchased child.
func TestRollUp_SingleLevelNeedsAnActiveStandard(t *testing.T) {
	cat := Catalog{
		"P": {ID: "P", Kind: KindManufactured, BOM: []BOMLine{{ChildID: "X", Qty: d("2")}}},
		"X": {ID: "X", Kind: KindPurchased, Price: sptr(d("2"))},
	}
	r, _ := RollOne(cat, "P", RollOptions{Mode: ModeSingleLevel})
	if r.Status != StatusError || len(r.Diagnostics) == 0 || r.Diagnostics[0].Code != CodeNoPrice || r.Diagnostics[0].Ref != "P" {
		t.Fatalf("want CST-0702 on P, got %s %+v", r.Status, r.Diagnostics)
	}
	x := cat["X"]
	x.ActivePrice = sptr(d("2.5"))
	cat["X"] = x
	r, _ = RollOne(cat, "P", RollOptions{Mode: ModeSingleLevel})
	if r.Status != StatusPending || !r.Own[CompDM].Equal(d("5")) {
		t.Fatalf("want DM 5.00 from the active price, got %s %v", r.Status, r.Own)
	}
}

// TestRollUp_PhantomIsCostedInPlace covers BR-39.3: a phantom's direct costs
// and routing hours join the parent's own level, so the parent's sheet applies
// to them and the phantom's own sheet does not. H (phantom) holds 1 × X at
// 3.00 and 0.5 h at 20.00; it has a 100% material surcharge that must not
// reach P. P's sheet charges 10% on DM and 2.00 per routing hour.
func TestRollUp_PhantomIsCostedInPlace(t *testing.T) {
	sheetP := []SheetNode{
		{ID: "OH-MAT", Type: NodeSurcharge, Component: CompIndMat, Basis: []string{CompDM}, Percentage: d("10")},
		{ID: "MACH", Type: NodeRate, Component: CompIndLab, Rate: d("2"), RateUnit: "Hour"},
	}
	cat := Catalog{
		"P": {ID: "P", Kind: KindManufactured, CostingLot: d("1"), Sheet: sheetP,
			BOM: []BOMLine{{ChildID: "H", Qty: d("2"), Phantom: true}}},
		"H": {ID: "H", Kind: KindManufactured, CostingLot: d("1"),
			BOM:        []BOMLine{{ChildID: "X", Qty: d("1")}},
			Operations: []Operation{{RunHours: d("0.5"), Rate: d("20")}},
			Sheet:      []SheetNode{{ID: "H-OH", Type: NodeSurcharge, Component: CompIndMat, Basis: []string{CompDM}, Percentage: d("100")}}},
		// The active price serves single-level mode, where H's own lines take
		// their children at the active standard.
		"X": {ID: "X", Kind: KindPurchased, Price: sptr(d("3")), ActivePrice: sptr(d("3"))},
	}
	for _, mode := range []ExplosionMode{ModeMultilevel, ModeSingleLevel, ModeMakeToOrder, ModeByLineType} {
		r, _ := RollOne(cat, "P", RollOptions{Mode: mode})
		if r.Status != StatusPending {
			t.Fatalf("%s: %+v", mode, r.Diagnostics)
		}
		// DM 2 × 3 = 6, DL 2 × 10 = 20, IND-MAT 10% × 6 = 0.60, IND-LAB 2 × 0.5 h × 2.00 = 2.00.
		want := Split{CompDM: d("6"), CompDL: d("20"), CompIndMat: d("0.6"), CompIndLab: d("2")}
		for c, v := range want {
			if !r.Own[c].Equal(v) {
				t.Errorf("%s: own %s = %s, want %s", mode, c, r.Own[c], v)
			}
		}
		if len(r.Rolled) != 0 || !r.Unit.Equal(d("28.6")) {
			t.Errorf("%s: rolled %v unit %s, want nothing rolled and 28.60", mode, r.Rolled, r.Unit)
		}
	}
}

// TestRollUp_RouteScrapScalesMaterial: material issued before the first
// operation is lost with every unit scrapped along the route (BR-39.6).
func TestRollUp_RouteScrapScalesMaterial(t *testing.T) {
	cat := Catalog{
		"P": {ID: "P", Kind: KindManufactured, CostingLot: d("1"),
			BOM: []BOMLine{{ChildID: "X", Qty: d("1"), ScrapPct: d("10")}},
			Operations: []Operation{
				{RunHours: d("0"), Rate: d("0")},
				{RunHours: d("0"), Rate: d("0"), ScrapPct: d("20")},
			}},
		"X": {ID: "X", Kind: KindPurchased, Price: sptr(d("10"))},
	}
	r, _ := RollOne(cat, "P", RollOptions{})
	// 1 × 1.10 line scrap × 1.20 route scrap × 10.00 = 13.20.
	if !r.Own[CompDM].Equal(d("13.2")) {
		t.Errorf("DM = %s, want 13.20", r.Own[CompDM])
	}
}
