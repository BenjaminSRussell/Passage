package domain

import (
	"reflect"
	"sort"
	"testing"
)

// BR-22.6: a dynamic run replans the changed item and everything below it.
func TestReachSet(t *testing.T) {
	bom := func(id string, comps ...string) Item {
		it := Item{ID: id}
		for _, c := range comps {
			it.Components = append(it.Components, BOMLine{ItemID: c, Qty: d("1")})
		}
		return it
	}
	chain := []Item{bom("FG", "SA", "RM"), bom("SA", "RM"), bom("RM"), bom("OTHER", "RM2"), bom("RM2")}
	tests := []struct {
		name    string
		changed []string
		want    []string
	}{
		{"changed leaf replans only itself", []string{"RM"}, []string{"RM"}},
		{"changed middle replans its components, not its parent", []string{"SA"}, []string{"RM", "SA"}},
		{"changed top reaches the whole chain", []string{"FG"}, []string{"FG", "RM", "SA"}},
		{"two changes union their reach", []string{"SA", "OTHER"}, []string{"OTHER", "RM", "RM2", "SA"}},
		{"no change replans nothing", nil, nil},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var keys []string
			for k := range ReachSet(chain, tc.changed) {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			if !reflect.DeepEqual(keys, tc.want) {
				t.Fatalf("ReachSet(%v) = %v, want %v", tc.changed, keys, tc.want)
			}
		})
	}
}

// Every item the reach adds to a changed item lies below it: its low-level
// code is greater (BR-22.6 "all items whose LLC is greater").
func TestReachSet_OnlyLowerLevels(t *testing.T) {
	items := []Item{
		{ID: "FG", Components: []BOMLine{{ItemID: "SA", Qty: d("2")}, {ItemID: "RM", Qty: d("1")}}},
		{ID: "SA", Components: []BOMLine{{ItemID: "RM", Qty: d("3")}}},
		{ID: "RM"},
	}
	index := map[string]*Item{}
	for i := range items {
		index[items[i].ID] = &items[i]
	}
	llc, err := lowLevelCodes(items, index)
	if err != nil {
		t.Fatal(err)
	}
	for id := range ReachSet(items, []string{"SA"}) {
		if id != "SA" && llc[id] <= llc["SA"] {
			t.Fatalf("%s (LLC %d) is not below SA (LLC %d)", id, llc[id], llc["SA"])
		}
	}
}
