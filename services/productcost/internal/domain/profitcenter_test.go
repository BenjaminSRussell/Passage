package domain

import "testing"

// pcCatalog is FG-PC built from RM-PC, bought from another profit center of the
// same entity at a list price of 120.00. The transfer basis is set by the caller.
func pcCatalog(rm Item) Catalog {
	rm.ID = "RM-PC"
	rm.Kind = KindPurchased
	rm.Price = ptr("120.00")
	rm.ProfitCenterSource = true
	return Catalog{
		"RM-PC": rm,
		"FG-PC": {ID: "FG-PC", Kind: KindManufactured, CostingLot: d("1"),
			BOM: []BOMLine{{ChildID: "RM-PC", Qty: d("1")}}},
	}
}

func TestProfitCenter_TransferPriceWins(t *testing.T) {
	cat := pcCatalog(Item{TransferPrice: ptr("90.00"), LegalCost: ptr("80.00"), CostPlusPct: ptr("50")})
	r := rolledOf(t, cat, RollOptions{View: ViewProfitCenter}, "FG-PC")
	if r.Status != StatusPending {
		t.Fatalf("status %s, diagnostics %+v", r.Status, r.Diagnostics)
	}
	if got := r.Unit.StringFixed(2); got != "90.00" {
		t.Errorf("unit = %s, want 90.00 (transfer price)", got)
	}
}

func TestProfitCenter_CostPlusMarkup(t *testing.T) {
	cases := []struct {
		name string
		pct  string
		want string
	}{
		{"12.5 percent over 80.00", "12.5", "90.00"},
		{"zero markup keeps cost", "0", "80.00"},
		{"100 percent doubles cost", "100", "160.00"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cat := pcCatalog(Item{LegalCost: ptr("80.00"), CostPlusPct: ptr(tc.pct)})
			r := rolledOf(t, cat, RollOptions{View: ViewProfitCenter}, "FG-PC")
			if r.Status != StatusPending {
				t.Fatalf("status %s, diagnostics %+v", r.Status, r.Diagnostics)
			}
			if got := r.Unit.StringFixed(2); got != tc.want {
				t.Errorf("unit = %s, want %s", got, tc.want)
			}
		})
	}
}

func TestProfitCenter_MissingBasisFailsWithCST0762(t *testing.T) {
	cat := pcCatalog(Item{})
	r := rolledOf(t, cat, RollOptions{View: ViewProfitCenter}, "RM-PC")
	found := false
	for _, dg := range r.Diagnostics {
		if dg.Code == CodeNoTransferPrice && dg.Severity == "error" && dg.Ref == "RM-PC" {
			found = true
		}
	}
	if !found {
		t.Errorf("diagnostics %+v, want %s error on RM-PC", r.Diagnostics, CodeNoTransferPrice)
	}
}

func TestProfitCenter_OnlyAppliesToProfitCenterSources(t *testing.T) {
	cat := pcCatalog(Item{TransferPrice: ptr("90.00")})
	rm := cat["RM-PC"]
	rm.ProfitCenterSource = false
	cat["RM-PC"] = rm
	r := rolledOf(t, cat, RollOptions{View: ViewProfitCenter}, "FG-PC")
	if got := r.Unit.StringFixed(2); got != "120.00" {
		t.Errorf("unit = %s, want 120.00 (external purchase keeps list price)", got)
	}
}

func TestProfitCenter_LegalViewIgnoresTransfer(t *testing.T) {
	cat := pcCatalog(Item{TransferPrice: ptr("90.00")})
	r := rolledOf(t, cat, RollOptions{View: ViewLegal}, "FG-PC")
	if got := r.Unit.StringFixed(2); got != "120.00" {
		t.Errorf("legal unit = %s, want 120.00", got)
	}
}

func TestProfitCenter_SingleLevelChildTakesTheTransferPrice(t *testing.T) {
	// SingleLevel takes a purchased child at its active standard; in the
	// ProfitCenter view a profit-center child takes its transfer price instead.
	cat := pcCatalog(Item{ActivePrice: ptr("110.00"), TransferPrice: ptr("95.00")})
	r := rolledOf(t, cat, RollOptions{View: ViewProfitCenter, Mode: ModeSingleLevel}, "FG-PC")
	if r.Status != StatusPending || r.Unit.StringFixed(2) != "95.00" {
		t.Errorf("unit %s status %s, want 95.00", r.Unit, r.Status)
	}
	legal := rolledOf(t, cat, RollOptions{View: ViewLegal, Mode: ModeSingleLevel}, "FG-PC")
	if legal.Unit.StringFixed(2) != "110.00" {
		t.Errorf("legal unit %s, want the active standard 110.00", legal.Unit)
	}
}

func TestProfitCenter_MissingBasisFailsTheParent(t *testing.T) {
	cat := pcCatalog(Item{ActivePrice: ptr("110.00")})
	single := rolledOf(t, cat, RollOptions{View: ViewProfitCenter, Mode: ModeSingleLevel}, "FG-PC")
	if single.Status != StatusError || !hasDiag(single.Diagnostics, CodeNoTransferPrice) || single.Diagnostics[0].Ref != "FG-PC" {
		t.Errorf("single level: status %s diagnostics %+v, want CST-0762 on FG-PC", single.Status, single.Diagnostics)
	}
	multi := rolledOf(t, cat, RollOptions{View: ViewProfitCenter}, "FG-PC")
	if multi.Status != StatusError || !hasDiag(multi.Diagnostics, CodeBlocked) {
		t.Errorf("multilevel: status %s diagnostics %+v, want CST-0707 cascade", multi.Status, multi.Diagnostics)
	}
}
