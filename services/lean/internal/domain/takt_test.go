package domain

import "testing"

// FR-24.14: the bottleneck activity's cycle time is compared with the takt
// time; the flow keeps up when it is not longer.
func TestTaktVsCycle(t *testing.T) {
	acts := []ActivityCycle{{Name: "Mix", CycleTimeS: 50}, {Name: "Fill", CycleTimeS: 70}, {Name: "Pack", CycleTimeS: 70}}
	got, ok := TaktVsCycle(60, acts)
	if !ok || got.CycleTimeS != 70 || got.Bottleneck != "Fill" || got.WithinTakt {
		t.Fatalf("over takt: %+v %v", got, ok)
	}
	got, _ = TaktVsCycle(70, acts)
	if !got.WithinTakt {
		t.Fatalf("a cycle equal to takt keeps up: %+v", got)
	}
	if _, ok := TaktVsCycle(0, acts); ok {
		t.Fatal("no takt time gives no figure")
	}
	if _, ok := TaktVsCycle(60, nil); ok {
		t.Fatal("no activities gives no figure")
	}
}
