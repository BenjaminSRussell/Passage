package domain

import "testing"

// icCatalog is FG-IC built from RM-IC, bought from IC partner B at 120.00
// whose Legal-view cost in B is 100.00 (AC-39.14).
// An empty legal string means the supplier has no Legal-view standard.
func icCatalog(legal string) Catalog {
	rm := Item{ID: "RM-IC", Kind: KindPurchased, Price: ptr("120.00"), Intercompany: true}
	if legal != "" {
		rm.LegalCost = ptr(legal)
	}
	return Catalog{
		"RM-IC": rm,
		"FG-IC": {
			ID: "FG-IC", Kind: KindManufactured, CostingLot: d("1"),
			BOM: []BOMLine{{ChildID: "RM-IC", Qty: d("1")}},
		},
	}
}

func TestValuationView_GroupUsesSupplierLegalCost(t *testing.T) {
	cases := []struct {
		name string
		opts RollOptions
		unit string
	}{
		{"legal view keeps the IC price", RollOptions{View: ViewLegal}, "120.00"},
		{"group view uses supplier legal cost", RollOptions{View: ViewGroup}, "100.00"},
		{"group view in single-level mode", RollOptions{View: ViewGroup, Mode: ModeSingleLevel}, "100.00"},
		{"default view is legal", RollOptions{}, "120.00"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := rolledOf(t, icCatalog("100.00"), tc.opts, "FG-IC")
			if r.Status != StatusPending {
				t.Fatalf("status %s, diagnostics %+v", r.Status, r.Diagnostics)
			}
			if got := r.Unit.StringFixed(2); got != tc.unit {
				t.Errorf("unit = %s, want %s", got, tc.unit)
			}
			if got := r.Combined()[CompDM].StringFixed(2); got != tc.unit {
				t.Errorf("DM = %s, want %s (split conserves unit cost)", got, tc.unit)
			}
		})
	}
}

func TestValuationView_GroupCostUnknownWarns(t *testing.T) {
	// The warning belongs to the purchased item; its parent rolls on the IC price.
	purchased := rolledOf(t, icCatalog(""), RollOptions{View: ViewGroup}, "RM-IC")
	if purchased.Status != StatusPending {
		t.Fatalf("status %s, want Pending (IC price is used)", purchased.Status)
	}
	r := purchased
	if got := r.Unit.StringFixed(2); got != "120.00" {
		t.Errorf("unit = %s, want 120.00", got)
	}
	if parent := rolledOf(t, icCatalog(""), RollOptions{View: ViewGroup}, "FG-IC"); parent.Unit.StringFixed(2) != "120.00" {
		t.Errorf("parent unit = %s, want 120.00", parent.Unit)
	}
	found := false
	for _, dg := range r.Diagnostics {
		if dg.Code == CodeGroupCostUnknown && dg.Severity == "warning" && dg.Ref == "RM-IC" {
			found = true
		}
	}
	if !found {
		t.Errorf("diagnostics %+v, want %s warning on RM-IC", r.Diagnostics, CodeGroupCostUnknown)
	}
}

func TestValuationView_NonInterCompanyItemsAreUnaffected(t *testing.T) {
	cat := Catalog{
		"RM-EXT": {ID: "RM-EXT", Kind: KindPurchased, Price: ptr("7.00")},
		"FG-EXT": {ID: "FG-EXT", Kind: KindManufactured, CostingLot: d("1"),
			BOM: []BOMLine{{ChildID: "RM-EXT", Qty: d("2")}}},
	}
	r := rolledOf(t, cat, RollOptions{View: ViewGroup}, "FG-EXT")
	if r.Unit.StringFixed(2) != "14.00" {
		t.Errorf("unit = %s, want 14.00", r.Unit)
	}
	for _, dg := range r.Diagnostics {
		if dg.Code == CodeGroupCostUnknown {
			t.Errorf("unexpected group warning for an external item: %+v", dg)
		}
	}
}

func TestValuationView_OptionValidation(t *testing.T) {
	if err := (RollOptions{View: ViewProfitCenter}).Validate(); err != nil {
		t.Errorf("ProfitCenter view rejected: %v", err)
	}
	if err := (RollOptions{View: "Tax"}).Validate(); err == nil {
		t.Error("unknown view must be rejected")
	}
	if err := (RollOptions{View: ViewGroup}).Validate(); err != nil {
		t.Errorf("Group view rejected: %v", err)
	}
	if got := (RollOptions{}).normalized().View; got != ViewLegal {
		t.Errorf("default view = %s, want Legal", got)
	}
}

func TestValuationView_SingleLevelWithoutLegalCostWarnsOnTheParent(t *testing.T) {
	cat := icCatalog("")
	rm := cat["RM-IC"]
	rm.ActivePrice = ptr("118.00")
	cat["RM-IC"] = rm
	r := rolledOf(t, cat, RollOptions{View: ViewGroup, Mode: ModeSingleLevel}, "FG-IC")
	if r.Status != StatusPending || r.Unit.StringFixed(2) != "118.00" {
		t.Fatalf("status %s unit %s, want Pending at the active price 118.00", r.Status, r.Unit)
	}
	if !hasCode(r.Diagnostics, CodeGroupCostUnknown, "warning") || r.Diagnostics[0].Ref != "FG-IC" {
		t.Errorf("diagnostics %+v, want %s on FG-IC", r.Diagnostics, CodeGroupCostUnknown)
	}
	// The Legal view never warns about group cost.
	if legal := rolledOf(t, cat, RollOptions{Mode: ModeSingleLevel}, "FG-IC"); hasCode(legal.Diagnostics, CodeGroupCostUnknown, "warning") {
		t.Errorf("legal view warned: %+v", legal.Diagnostics)
	}
}
