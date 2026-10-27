package domain

import "testing"

func boltSpecs() []PackagingSpec {
	levels := []PackLevel{{Name: "each", QtyPerLevel: dec("1")}, {Name: "case", QtyPerLevel: dec("12")}, {Name: "pallet", QtyPerLevel: dec("144")}}
	return []PackagingSpec{
		{ID: "PS-01", ItemID: "BOLT-M8", CustomerID: "C1", Levels: levels},
		{ID: "PS-00", ItemID: "BOLT-M8", CustomerID: "C1", Levels: levels},
		{ID: "PS-02", ItemID: "BOLT-M8", Levels: levels},
		{ID: "PS-03", ItemID: "", ItemGroup: "FASTENERS", CustomerID: "C1", Levels: levels},
		{ID: "PS-04", ItemGroup: "FASTENERS", Levels: levels},
	}
}

func TestDetermineSpec_MostSpecificMatch(t *testing.T) {
	specs := boltSpecs()
	tests := []struct {
		name, item, group, customer, want string
	}{
		{"item and customer, lowest id wins the tie", "BOLT-M8", "FASTENERS", "C1", "PS-00"},
		{"item without the customer falls to the item rung", "BOLT-M8", "", "C2", "PS-02"},
		{"group and customer when the item has no spec", "NUT-M8", "FASTENERS", "C1", "PS-03"},
		{"group alone when the customer has no spec", "NUT-M8", "FASTENERS", "C2", "PS-04"},
		{"no item or group match is none", "NUT-M8", "SEALS", "C1", ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := DetermineSpec(specs, tc.item, tc.group, tc.customer)
			if (tc.want == "") == ok || got.ID != tc.want {
				t.Fatalf("DetermineSpec = (%q, %v), want %q", got.ID, ok, tc.want)
			}
		})
	}
}

func TestPackagingSpec_Validate(t *testing.T) {
	ok := []PackLevel{{Name: "each", QtyPerLevel: dec("1")}, {Name: "case", QtyPerLevel: dec("12")}}
	tests := []struct {
		name    string
		spec    PackagingSpec
		wantErr bool
	}{
		{"valid item spec", PackagingSpec{ID: "P", ItemID: "X", Levels: ok}, false},
		{"valid group spec with material", PackagingSpec{ID: "P", ItemGroup: "G", Levels: ok, ConsumeMaterial: true, MaterialItem: "CARTON-S"}, false},
		{"neither item nor group", PackagingSpec{ID: "P", Levels: ok}, true},
		{"both item and group", PackagingSpec{ID: "P", ItemID: "X", ItemGroup: "G", Levels: ok}, true},
		{"no levels", PackagingSpec{ID: "P", ItemID: "X"}, true},
		{"level smaller than the level below", PackagingSpec{ID: "P", ItemID: "X", Levels: []PackLevel{{Name: "case", QtyPerLevel: dec("12")}, {Name: "each", QtyPerLevel: dec("1")}}}, true},
		{"consumes material without naming it", PackagingSpec{ID: "P", ItemID: "X", Levels: ok, ConsumeMaterial: true}, true},
		{"zero quantity level", PackagingSpec{ID: "P", ItemID: "X", Levels: []PackLevel{{Name: "each", QtyPerLevel: dec("0")}}}, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if err := tc.spec.Validate(); (err != nil) != tc.wantErr {
				t.Fatalf("Validate() = %v, wantErr %v", err, tc.wantErr)
			}
		})
	}
}

func TestRoundUpToLP(t *testing.T) {
	spec := boltSpecs()[2]
	tests := []struct {
		qty, level, want string
		wantErr          bool
	}{
		{"1200", "case", "1200", false},
		{"1205", "case", "1212", false},
		{"0", "pallet", "0", false},
		{"1", "pallet", "144", false},
		{"5", "box", "", true},
		{"-1", "case", "", true},
	}
	for _, tc := range tests {
		got, err := RoundUpToLP(dec(tc.qty), spec, tc.level)
		if (err != nil) != tc.wantErr {
			t.Errorf("RoundUpToLP(%s, %s) error = %v", tc.qty, tc.level, err)
			continue
		}
		if !tc.wantErr && !got.Equal(dec(tc.want)) {
			t.Errorf("RoundUpToLP(%s, %s) = %s, want %s", tc.qty, tc.level, got, tc.want)
		}
	}
}
