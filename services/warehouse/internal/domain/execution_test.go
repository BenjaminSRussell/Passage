package domain

import (
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/shopspring/decimal"
	"pgregory.net/rapid"
)

func xd(s string) decimal.Decimal { return decimal.RequireFromString(s) }

func xPutDirective(zone string) Directive {
	return Directive{ID: "D1", Sequence: 10, WorkType: "PURCHASE_PUTAWAY", Direction: DirectionPut, Warehouse: "WH1",
		Bands: []Band{{Seq: 1, FromQty: decimal.Zero, Actions: []Action{{Seq: 1, Strategy: StrategyConsolidate, Zone: zone}}}}}
}

func xPickDirective() Directive {
	return Directive{ID: "P1", Sequence: 10, WorkType: "SALES_PICK", Direction: DirectionPick, Warehouse: "WH1",
		Bands: []Band{{Seq: 1, FromQty: decimal.Zero, Actions: []Action{{Seq: 1, Strategy: StrategyFIFO, Zone: "PICK"}}}}}
}

func xLoc(id, zone string, seq int, stock map[string]decimal.Decimal) Location {
	return Location{ID: id, Name: id, Zone: zone, PickSequence: seq, Status: StatusActive,
		Capacity: Capacity{AllowMixedItems: true}, Stock: stock}
}

func xAssertCode(t *testing.T, err error, code string) {
	t.Helper()
	var de *Error
	if !errors.As(err, &de) || de.Code != code {
		t.Fatalf("err = %v, want code %s", err, code)
	}
}

func xOpenPick(qty string) Work {
	at := time.Unix(0, 0)
	return Work{ID: "W1", WarehouseID: "WH1", WorkType: "SALES_PICK", Status: WorkAssigned, Priority: 50,
		AssignedUser: "u1", AssignedAt: &at, Lines: []WorkLine{{LineNo: 1, ItemID: "BOLT", Direction: DirectionPick,
			Qty: xd(qty), ConfirmedQty: decimal.Zero, Weight: xd("10"), Volume: xd("0.5"), LPs: 0, FromLocationID: "P1", Status: LineOpen}}}
}

func TestPlan_ResolvesLocationsOrBlocks(t *testing.T) {
	cases := []struct {
		name        string
		workType    string
		dirs        []Directive
		locs        []Location
		direction   Direction
		wantStatus  WorkStatus
		wantTo      string
		wantFrom    string
		wantBlocked bool
	}{
		{"put to directed zone", "PURCHASE_PUTAWAY", []Directive{xPutDirective("A")}, []Location{xLoc("L1", "A", 1, nil)},
			DirectionPut, WorkOpen, "L1", "", false},
		{"put with no zone blocks", "PURCHASE_PUTAWAY", []Directive{xPutDirective("B")}, []Location{xLoc("L1", "A", 1, nil)},
			DirectionPut, WorkBlockedNoLocation, "", "", true},
		{"pick from the holding location", "SALES_PICK", []Directive{xPickDirective()},
			[]Location{xLoc("P1", "PICK", 1, map[string]decimal.Decimal{"BOLT": xd("10")})},
			DirectionPick, WorkOpen, "", "P1", false},
		{"pick without stock blocks", "SALES_PICK", []Directive{xPickDirective()},
			[]Location{xLoc("P1", "PICK", 1, nil)}, DirectionPick, WorkBlockedNoLocation, "", "", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := NewLine{ItemID: "BOLT", Direction: tc.direction, Qty: xd("5"), Weight: xd("1")}
			w, res, err := Plan("W1", "WH1", tc.workType, 0, []NewLine{req}, tc.dirs, tc.locs, Compliance{})
			if err != nil {
				t.Fatalf("Plan: %v", err)
			}
			if w.Status != tc.wantStatus {
				t.Fatalf("status = %s, want %s", w.Status, tc.wantStatus)
			}
			if got := res[0].Blocked; got != tc.wantBlocked {
				t.Fatalf("blocked = %v, want %v", got, tc.wantBlocked)
			}
			if w.Lines[0].ToLocationID != tc.wantTo || w.Lines[0].FromLocationID != tc.wantFrom {
				t.Fatalf("to/from = %q/%q, want %q/%q", w.Lines[0].ToLocationID, w.Lines[0].FromLocationID, tc.wantTo, tc.wantFrom)
			}
			if w.Priority != DefaultPriority {
				t.Fatalf("priority = %d, want default %d", w.Priority, DefaultPriority)
			}
		})
	}
}

func TestPlan_RejectsEmptyAndNonPositive(t *testing.T) {
	if _, _, err := Plan("W", "WH1", "MOVEMENT", 0, nil, nil, nil, Compliance{}); err == nil {
		t.Fatal("empty work accepted")
	}
	bad := []NewLine{{ItemID: "X", Direction: DirectionPut, Qty: decimal.Zero}}
	if _, _, err := Plan("W", "WH1", "MOVEMENT", 0, bad, nil, nil, Compliance{}); err == nil {
		t.Fatal("zero quantity accepted")
	}
}

func TestConfirmLine_Transitions(t *testing.T) {
	t.Run("full confirm completes the work", func(t *testing.T) {
		w := xOpenPick("10")
		replayed, err := w.ConfirmLine(1, xd("10"), "k1", "u1")
		if err != nil || replayed {
			t.Fatalf("confirm: replayed=%v err=%v", replayed, err)
		}
		if w.Status != WorkCompleted || w.Lines[0].Status != LineCompleted {
			t.Fatalf("status=%s line=%s", w.Status, w.Lines[0].Status)
		}
	})
	t.Run("same key replays without change", func(t *testing.T) {
		w := xOpenPick("10")
		_, _ = w.ConfirmLine(1, xd("10"), "k1", "u1")
		replayed, err := w.ConfirmLine(1, xd("10"), "k1", "u1")
		if err != nil || !replayed {
			t.Fatalf("replay: replayed=%v err=%v", replayed, err)
		}
	})
	t.Run("different key on a confirmed line is WMS-0105", func(t *testing.T) {
		w := xOpenPick("10")
		_, _ = w.ConfirmLine(1, xd("10"), "k1", "u1")
		_, err := w.ConfirmLine(1, xd("10"), "k2", "u1")
		xAssertCode(t, err, CodeLineConfirmed)
	})
	t.Run("over-pick is WMS-0106", func(t *testing.T) {
		w := xOpenPick("10")
		_, err := w.ConfirmLine(1, xd("11"), "k1", "u1")
		xAssertCode(t, err, CodeOverPick)
	})
	t.Run("short quantity needs a short pick", func(t *testing.T) {
		w := xOpenPick("10")
		_, err := w.ConfirmLine(1, xd("6"), "k1", "u1")
		xAssertCode(t, err, CodeIllegalTransition)
	})
	t.Run("unknown line", func(t *testing.T) {
		w := xOpenPick("10")
		if _, err := w.ConfirmLine(2, xd("10"), "k1", "u1"); !errors.Is(err, ErrLineNotFound) {
			t.Fatalf("err = %v, want ErrLineNotFound", err)
		}
	})
	t.Run("only the assignee confirms", func(t *testing.T) {
		w := xOpenPick("10")
		if _, err := w.ConfirmLine(1, xd("10"), "k1", "u2"); !errors.Is(err, ErrNotAssignee) {
			t.Fatalf("err = %v, want ErrNotAssignee", err)
		}
		if w.Status != WorkAssigned || w.Lines[0].Status != LineOpen {
			t.Fatalf("refused confirmation changed the work: %s/%s", w.Status, w.Lines[0].Status)
		}
	})
	t.Run("OPEN work cannot be confirmed", func(t *testing.T) {
		w := xOpenPick("10")
		w.Status = WorkOpen
		_, err := w.ConfirmLine(1, xd("10"), "k1", "u1")
		xAssertCode(t, err, CodeIllegalTransition)
	})
}

// AC-31.5: 10 EA pick, short 6 with reason EMPTY -> parent PARTIALLY_COMPLETED,
// child OPEN for 4 EA with parent reference, and only 6 EA moves.
func TestShortPick_SplitsRemainder(t *testing.T) {
	w := xOpenPick("10")
	child, err := w.ShortPick(1, xd("6"), "EMPTY", "W2", "u1")
	if err != nil {
		t.Fatalf("ShortPick: %v", err)
	}
	if w.Status != WorkPartiallyComplete || w.Lines[0].Status != LineShort {
		t.Fatalf("parent status=%s line=%s", w.Status, w.Lines[0].Status)
	}
	if child.Status != WorkOpen || child.ParentID != "W1" || !child.Lines[0].Qty.Equal(xd("4")) {
		t.Fatalf("child = %+v", child)
	}
	if got := w.Lines[0].ConfirmedUsage(); !got.Weight.Equal(xd("6")) {
		t.Fatalf("confirmed weight = %s, want 6", got.Weight)
	}
	if !child.Lines[0].Weight.Equal(xd("4")) {
		t.Fatalf("remainder weight = %s, want 4", child.Lines[0].Weight)
	}
}

func TestShortPick_Refusals(t *testing.T) {
	w := xOpenPick("10")
	if _, err := w.ShortPick(1, xd("6"), " ", "W2", "u1"); !errors.Is(err, ErrReasonRequired) {
		t.Fatalf("blank reason: %v", err)
	}
	if _, err := w.ShortPick(1, xd("10"), "EMPTY", "W2", "u1"); err == nil {
		t.Fatal("short pick of the full quantity accepted")
	}
	if _, err := w.ShortPick(1, xd("-1"), "EMPTY", "W2", "u1"); err == nil {
		t.Fatal("negative short pick accepted")
	}
	if _, err := w.ShortPick(1, xd("6"), "EMPTY", "W2", "u2"); !errors.Is(err, ErrNotAssignee) {
		t.Fatalf("short pick by another user: %v", err)
	}
	put := xOpenPick("10")
	put.Lines[0].Direction, put.Lines[0].ToLocationID, put.Lines[0].FromLocationID = DirectionPut, "L1", ""
	_, err := put.ShortPick(1, xd("6"), "EMPTY", "W2", "u1")
	xAssertCode(t, err, CodeIllegalTransition)
	if w.Lines[0].Status != LineOpen || w.Status != WorkAssigned {
		t.Fatalf("refused short picks changed the work: %s/%s", w.Status, w.Lines[0].Status)
	}
}

// A short pick of zero (the location was empty) moves nothing and re-queues everything.
func TestShortPick_Zero(t *testing.T) {
	w := xOpenPick("10")
	child, err := w.ShortPick(1, decimal.Zero, "EMPTY", "W2", "u1")
	if err != nil || !child.Lines[0].Qty.Equal(xd("10")) || w.Status != WorkPartiallyComplete {
		t.Fatalf("child=%+v status=%s err=%v", child, w.Status, err)
	}
	if u := w.Lines[0].ConfirmedUsage(); !u.Weight.IsZero() || u.LPs != 0 {
		t.Fatalf("zero pick moved %+v", u)
	}
}

func TestCancel(t *testing.T) {
	w := xOpenPick("10")
	if err := w.Cancel(" ", "u1", false); !errors.Is(err, ErrReasonRequired) {
		t.Fatalf("blank reason: %v", err)
	}
	if err := w.Cancel("DAMAGED", "u2", false); !errors.Is(err, ErrNotAssignee) {
		t.Fatalf("another user without supervision: %v", err)
	}
	if err := w.Cancel("DAMAGED", "u1", false); err != nil {
		t.Fatalf("assignee cancels: %v", err)
	}
	if w.Status != WorkCancelled || w.Lines[0].Status != LineCancelled || w.Lines[0].Reason != "DAMAGED" || w.AssignedUser != "" {
		t.Fatalf("cancelled work = %+v", w)
	}
	open := Work{ID: "W3", Status: WorkOpen, Lines: []WorkLine{{LineNo: 1, Status: LineOpen}}}
	if err := open.Cancel("WAVE_DROPPED", "boss", true); err != nil || open.Status != WorkCancelled {
		t.Fatalf("supervisor cancels open work: %v %s", err, open.Status)
	}
	w2 := xOpenPick("10")
	_, _ = w2.ConfirmLine(1, xd("10"), "k", "u1")
	xAssertCode(t, w2.Cancel("DAMAGED", "u1", false), CodeIllegalTransition)
}

// BR-31.2: within one work, a second line cannot claim room or stock the
// first line already took, and Plan leaves the caller's locations untouched.
func TestPlan_ReservesWithinTheWork(t *testing.T) {
	one := 1
	locs := []Location{
		{ID: "R1", Name: "R1", Zone: "R", PickSequence: 1, Status: StatusActive, Capacity: Capacity{MaxLPs: &one}},
		{ID: "R2", Name: "R2", Zone: "R", PickSequence: 2, Status: StatusActive, Capacity: Capacity{MaxLPs: &one}},
	}
	dir := xPutDirective("R")
	dir.Bands[0].Actions[0].Strategy = StrategyEmptyLocation
	pallet := NewLine{ItemID: "BOLT", Direction: DirectionPut, Qty: xd("1"), LPs: 1}
	w, _, err := Plan("W", "WH1", "PURCHASE_PUTAWAY", 0, []NewLine{pallet, pallet, pallet}, []Directive{dir}, locs, Compliance{})
	if err != nil {
		t.Fatal(err)
	}
	if w.Lines[0].ToLocationID != "R1" || w.Lines[1].ToLocationID != "R2" || w.Lines[2].ToLocationID != "" || w.Status != WorkBlockedNoLocation {
		t.Fatalf("lines = %+v status %s", w.Lines, w.Status)
	}
	if locs[0].Used.LPs != 0 || locs[0].inbound != nil {
		t.Fatalf("Plan changed its input: %+v", locs[0])
	}

	picks := []Location{xLoc("P1", "PICK", 1, map[string]decimal.Decimal{"BOLT": xd("10")})}
	six := NewLine{ItemID: "BOLT", Direction: DirectionPick, Qty: xd("6")}
	w, _, err = Plan("W", "WH1", "SALES_PICK", 0, []NewLine{six, six}, []Directive{xPickDirective()}, picks, Compliance{})
	if err != nil || w.Lines[0].FromLocationID != "P1" || w.Lines[1].FromLocationID != "" {
		t.Fatalf("second pick of 6 from 10 must block: %+v err=%v", w.Lines, err)
	}
	if !picks[0].Stock["BOLT"].Equal(xd("10")) {
		t.Fatalf("Plan changed the caller's stock: %s", picks[0].Stock["BOLT"])
	}
}

// An inbound item counts for mixing and emptiness: a location promised to
// BOLT refuses NUT when mixing is off and is not empty for empty_location.
func TestReserve_InboundItemsCountForMixingAndEmptiness(t *testing.T) {
	locs := []Location{{ID: "L1", Name: "L1", Zone: "A", Status: StatusActive}}
	Reserve(locs, []WorkLine{{LineNo: 1, ItemID: "BOLT", Direction: DirectionPut, Qty: xd("1"), ToLocationID: "L1", Status: LineOpen}})
	if reason, ok := locs[0].admitPut("NUT", Usage{}); ok || reason != "mixing: holds BOLT" {
		t.Fatalf("admitPut NUT = %q %v", reason, ok)
	}
	if _, ok := locs[0].admitPut("BOLT", Usage{}); !ok {
		t.Fatal("the inbound item itself must still be admitted")
	}
	if locs[0].isEmpty() {
		t.Fatal("a location with inbound stock is not empty")
	}
	Reserve(locs, []WorkLine{{ItemID: "X", Direction: DirectionPut, ToLocationID: "L1", Status: LineCompleted}})
	if locs[0].inbound["X"] {
		t.Fatal("a completed line reserved room")
	}
}

func TestAssign(t *testing.T) {
	w := Work{ID: "W", Status: WorkOpen}
	at := time.Unix(1000, 0)
	if err := w.Assign("u1", at); err != nil || w.Status != WorkAssigned || w.AssignedUser != "u1" || !w.AssignedAt.Equal(at) {
		t.Fatalf("assign: %v %+v", err, w)
	}
	xAssertCode(t, w.Assign("u2", at), CodeIllegalTransition)
	xAssertCode(t, (&Work{Status: WorkCompleted}).Assign("u", at), CodeIllegalTransition)
}

func TestIssue_RefusesOverdraw(t *testing.T) {
	l := xLoc("P1", "PICK", 1, map[string]decimal.Decimal{"BOLT": xd("4")})
	l.Used = Usage{Weight: xd("4"), LPs: 1}
	if reason, ok := l.Issue("BOLT", xd("5"), Usage{}); ok || reason == "" {
		t.Fatalf("overdraw accepted: %q", reason)
	}
	if _, ok := l.Issue("BOLT", xd("4"), Usage{Weight: xd("4"), LPs: 1}); !ok {
		t.Fatal("exact issue refused")
	}
	if _, held := l.Stock["BOLT"]; held {
		t.Fatal("stock not removed when emptied")
	}
	if _, ok := l.Issue("BOLT", xd("1"), Usage{}); ok {
		t.Fatal("issue from empty location accepted")
	}
}

func TestMove_RefusesBlockedStatus(t *testing.T) {
	l := xLoc("L1", "A", 1, nil)
	l.Status = StatusBlockedIn
	if _, ok := l.Move(DirectionPut, "BOLT", xd("1"), Usage{}); ok {
		t.Fatal("put into BLOCKED_IN accepted")
	}
	xAssertCode(t, MoveError("status BLOCKED_IN blocks PUT"), CodeIllegalTransition)
	xAssertCode(t, MoveError("capacity: lps 1/1"), CodeCapacity)
	xAssertCode(t, MoveError("mixing: holds X"), CodeMixedItems)
}

// Capacity is an invariant under any interleaving of receipts and issues (AC-31.9):
// a refused move leaves the location as it was, and no accepted state exceeds the limit.
func TestProperty_CapacityNeverExceeded(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		maxLPs := rapid.IntRange(1, 5).Draw(t, "maxLps")
		maxW := xd(rapid.SampledFrom([]string{"10", "25.5", "100"}).Draw(t, "maxWeight"))
		l := Location{ID: "L", Name: "L", Zone: "A", Status: StatusActive,
			Capacity: Capacity{MaxLPs: &maxLPs, MaxWeight: &maxW, AllowMixedItems: true}}
		ops := rapid.IntRange(1, 60).Draw(t, "ops")
		for i := 0; i < ops; i++ {
			qty := xd(rapid.SampledFrom([]string{"1", "2.5", "3"}).Draw(t, "qty"))
			u := Usage{Weight: qty, LPs: rapid.IntRange(0, 2).Draw(t, "lps")}
			before := fmt.Sprintf("%+v", l.Used)
			dir := rapid.SampledFrom([]Direction{DirectionPut, DirectionPick}).Draw(t, "dir")
			if _, ok := l.Move(dir, "BOLT", qty, u); !ok && fmt.Sprintf("%+v", l.Used) != before {
				t.Fatalf("refused move changed usage: %s -> %+v", before, l.Used)
			}
			if l.Used.LPs > maxLPs || l.Used.Weight.GreaterThan(maxW) || l.Used.Weight.IsNegative() {
				t.Fatalf("capacity exceeded: used=%+v max lps=%d weight=%s", l.Used, maxLPs, maxW)
			}
		}
	})
}
