package domain

import "testing"

// rolledOf runs a mass roll and returns the result for one item id.
func rolledOf(t *testing.T, cat Catalog, opts RollOptions, id string) Result {
	t.Helper()
	for _, r := range MassRoll(cat, opts).Results {
		if r.ItemID == id {
			return r
		}
	}
	t.Fatalf("no result for %s", id)
	return Result{}
}

// basisCatalog is one manufactured item: 3 × RM-301 at 2.50 (DM 7.50) and
// 0.25 h at 60.00 (DL 15.00).
func basisCatalog(sheet ...SheetNode) Catalog {
	return Catalog{
		"RM-301": {ID: "RM-301", Kind: KindPurchased, Price: ptr("2.50")},
		"M-1": {
			ID: "M-1", Kind: KindManufactured, CostingLot: d("1"),
			BOM:        []BOMLine{{ChildID: "RM-301", Qty: d("3")}},
			Operations: []Operation{{RunHours: d("0.25"), Rate: d("60")}},
			Sheet:      sheet,
		},
	}
}

func TestSheet_BasisTotalAndBasisRefSurcharge(t *testing.T) {
	cat := basisCatalog(
		SheetNode{ID: "B-DM", Type: NodeBasis, Basis: []string{CompDM}},
		SheetNode{ID: "B-DL", Type: NodeBasis, Basis: []string{CompDL}},
		SheetNode{ID: "T-ALL", Type: NodeTotal, Refs: []string{"B-DM", "B-DL"}},
		SheetNode{ID: "S-GEN", Type: NodeSurcharge, Component: "IND-GEN", BasisRef: "T-ALL", Percentage: d("10")},
	)
	r := rolledOf(t, cat, RollOptions{}, "M-1")
	if r.Status != StatusPending {
		t.Fatalf("status %s, diagnostics %+v", r.Status, r.Diagnostics)
	}
	want := map[string]string{"B-DM": "7.50", "B-DL": "15.00", "T-ALL": "22.50"}
	for id, v := range want {
		if got := r.Subtotals[id].StringFixed(2); got != v {
			t.Errorf("subtotal %s = %s, want %s", id, got, v)
		}
	}
	if got := r.Own["IND-GEN"].StringFixed(2); got != "2.25" {
		t.Errorf("IND-GEN = %s, want 2.25", got)
	}
	if got := r.Unit.StringFixed(2); got != "24.75" {
		t.Errorf("unit = %s, want 24.75 (basis and total add nothing to the split)", got)
	}
}

func TestSheet_BasisAddsNoCostToSplit(t *testing.T) {
	plain := rolledOf(t, basisCatalog(), RollOptions{}, "M-1")
	withBasis := rolledOf(t, basisCatalog(SheetNode{ID: "B", Type: NodeBasis, Basis: []string{CompDM, CompDL}}), RollOptions{}, "M-1")
	if !plain.Unit.Equal(withBasis.Unit) {
		t.Errorf("basis changed unit cost: %s vs %s", plain.Unit, withBasis.Unit)
	}
	if got := withBasis.Subtotals["B"].StringFixed(2); got != "22.50" {
		t.Errorf("basis subtotal = %s, want 22.50", got)
	}
}

func TestSheet_InvalidNodesFailWithCST0708(t *testing.T) {
	cases := []struct {
		name  string
		sheet []SheetNode
	}{
		{"surcharge refers to a missing basis", []SheetNode{
			{ID: "S", Type: NodeSurcharge, Component: "X", BasisRef: "nope", Percentage: d("1")},
		}},
		{"surcharge refers to a basis that comes later", []SheetNode{
			{ID: "S", Type: NodeSurcharge, Component: "X", BasisRef: "B", Percentage: d("1")},
			{ID: "B", Type: NodeBasis, Basis: []string{CompDM}},
		}},
		{"total refers to an unknown node", []SheetNode{
			{ID: "T", Type: NodeTotal, Refs: []string{"ghost"}},
		}},
		{"unknown node type is not skipped", []SheetNode{
			{ID: "W", Type: NodeType("Weird")},
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := rolledOf(t, basisCatalog(tc.sheet...), RollOptions{}, "M-1")
			if r.Status != StatusError {
				t.Fatalf("status %s, want Error", r.Status)
			}
			if len(r.Diagnostics) == 0 || r.Diagnostics[len(r.Diagnostics)-1].Code != CodeSheetNode {
				t.Fatalf("diagnostics %+v, want %s", r.Diagnostics, CodeSheetNode)
			}
		})
	}
}

func TestSheet_BasisIncludeRolledUsesChildSplit(t *testing.T) {
	cat := workedCatalog()
	fg := cat["FG-100"]
	fg.Sheet = append(fg.Sheet, SheetNode{ID: "B-DM", Type: NodeBasis, Basis: []string{CompDM}, IncludeRolled: true})
	cat["FG-100"] = fg
	r := rolledOf(t, cat, RollOptions{}, "FG-100")
	// Own DM 4.00 from RM-300 plus rolled DM 17.20 from 2 × SA-200.
	if got := r.Subtotals["B-DM"].StringFixed(2); got != "21.20" {
		t.Errorf("basis with rolled subassemblies = %s, want 21.20", got)
	}
	if got := r.Unit.StringFixed(2); got != "104.32" {
		t.Errorf("unit = %s, want 104.32", got)
	}
}
