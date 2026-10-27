package domain

import (
	"testing"
	"time"
)

var today = time.Date(2026, 3, 10, 9, 0, 0, 0, time.UTC)

func rl(line, order, wh string, day int, mod func(*ReleaseLine)) ReleaseLine {
	l := ReleaseLine{
		OrderID: order, OrderLineID: line, WarehouseID: wh, CustomerID: "c1", LegalEntityID: "le1",
		Qty: d("1"), ConfirmedShipDate: time.Date(2026, 3, day, 0, 0, 0, 0, time.UTC), ATPOK: true,
	}
	if mod != nil {
		mod(&l)
	}
	return l
}

func TestRelease(t *testing.T) {
	rule := ReleaseRule{Today: today, HorizonDays: 2, RequireFullATP: true}
	lines := []ReleaseLine{
		rl("l1", "o1", "w1", 10, nil),
		rl("l2", "o1", "w1", 11, nil),                                      // within horizon, same order, new date
		rl("l3", "o1", "w1", 10, nil),                                      // groups with l1
		rl("l4", "o2", "w1", 10, func(l *ReleaseLine) { l.Held = true }),   // held
		rl("l5", "o2", "w1", 13, nil),                                      // beyond horizon
		rl("l6", "o2", "w1", 10, func(l *ReleaseLine) { l.ATPOK = false }), // atp short
		rl("l7", "o2", "w1", 10, func(l *ReleaseLine) { l.Qty = d("0") }),  // non-positive
	}
	groups, skipped := Release(lines, rule)
	if len(groups) != 2 {
		t.Fatalf("groups = %d, want 2", len(groups))
	}
	if len(groups[0].Lines) != 2 || groups[0].Lines[0].OrderLineID != "l1" || groups[0].Lines[1].OrderLineID != "l3" {
		t.Fatalf("first group lines = %+v", groups[0].Lines)
	}
	want := map[string]string{"l4": SkipHeld, "l5": SkipBeyondHorizon, "l6": SkipATPShort, "l7": SkipNonPositive}
	if len(skipped) != len(want) {
		t.Fatalf("skipped = %+v", skipped)
	}
	for _, s := range skipped {
		if want[s.OrderLineID] != s.Reason {
			t.Errorf("line %s skipped for %s, want %s", s.OrderLineID, s.Reason, want[s.OrderLineID])
		}
	}
}

func TestReleaseATPOptional(t *testing.T) {
	rule := ReleaseRule{Today: today, HorizonDays: 2, RequireFullATP: false}
	groups, skipped := Release([]ReleaseLine{rl("l1", "o1", "w1", 10, func(l *ReleaseLine) { l.ATPOK = false })}, rule)
	if len(groups) != 1 || len(skipped) != 0 {
		t.Fatalf("partial release should be allowed: groups=%d skipped=%d", len(groups), len(skipped))
	}
}

func TestReleaseSplitsByWarehouseAndDate(t *testing.T) {
	rule := ReleaseRule{Today: today, HorizonDays: 5}
	groups, _ := Release([]ReleaseLine{
		rl("l1", "o1", "w1", 10, nil),
		rl("l2", "o1", "w2", 10, nil),
		rl("l3", "o1", "w1", 12, nil),
	}, rule)
	if len(groups) != 3 {
		t.Fatalf("groups = %d, want one per (order, warehouse, date)", len(groups))
	}
}

func TestReleaseIsDeterministic(t *testing.T) {
	rule := ReleaseRule{Today: today, HorizonDays: 2}
	lines := []ReleaseLine{rl("l1", "o2", "w1", 10, nil), rl("l2", "o1", "w1", 10, nil)}
	a, _ := Release(lines, rule)
	b, _ := Release([]ReleaseLine{lines[1], lines[0]}, rule)
	if a[0].OrderID != b[0].OrderID || a[1].OrderID != b[1].OrderID || a[0].OrderID != "o1" {
		t.Fatalf("order differs: %s %s", a[0].OrderID, b[0].OrderID)
	}
}
