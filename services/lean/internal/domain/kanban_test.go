package domain

import (
	"fmt"
	"testing"

	"github.com/shopspring/decimal"
	"pgregory.net/rapid"
)

func TestCheckMove_Table(t *testing.T) {
	tests := []struct {
		from, to State
		ok       bool
	}{
		{NotPlanned, Planned, true},
		{Planned, Prepared, true},
		{Prepared, InProgress, true},
		{InProgress, Completed, true},
		{Completed, Emptied, true},
		{Planned, NotPlanned, true},
		{Emptied, NotPlanned, true},
		{Planned, Completed, false}, // AC-24.7: drag from Planned to Completed
		{Prepared, Planned, false},
		{Completed, InProgress, false},
		{Emptied, Planned, false},
		{NotPlanned, Emptied, false},
		{Emptied, Completed, false},
		{State("bogus"), Planned, false},
	}
	for _, tc := range tests {
		err := CheckMove(tc.from, tc.to)
		if (err == nil) != tc.ok {
			t.Fatalf("CheckMove(%s, %s) = %v, want ok=%v", tc.from, tc.to, err, tc.ok)
		}
		if err != nil {
			want := CodeMove
			if !ValidState(tc.from) {
				want = CodeInvalid
			}
			if err.(*Error).Code != want {
				t.Fatalf("CheckMove(%s, %s) code %s, want %s", tc.from, tc.to, err.(*Error).Code, want)
			}
		}
	}
}

// oracle restates BR-24.6 independently of the implementation.
func oracle(from, to State) bool {
	order := []State{NotPlanned, Planned, Prepared, InProgress, Completed, Emptied}
	fi, ti := -1, -1
	for i, s := range order {
		if s == from {
			fi = i
		}
		if s == to {
			ti = i
		}
	}
	if fi < 0 || ti < 0 {
		return false
	}
	if ti == 0 && (from == Planned || from == Emptied) {
		return true
	}
	return ti == fi+1
}

// TestCheckMove_PropertyIllegalRejected: for every pair of states the
// implementation agrees with the oracle, so illegal moves never pass.
func TestCheckMove_PropertyIllegalRejected(t *testing.T) {
	all := []State{NotPlanned, Planned, Prepared, InProgress, Completed, Emptied}
	rapid.Check(t, func(t *rapid.T) {
		from := rapid.SampledFrom(all).Draw(t, "from")
		to := rapid.SampledFrom(all).Draw(t, "to")
		if got := CheckMove(from, to) == nil; got != oracle(from, to) {
			t.Fatalf("move %s → %s: implementation %v, oracle %v", from, to, got, oracle(from, to))
		}
	})
}

// TestKanban_RandomWalkStaysOnRules: a random walk of attempted moves only ever
// changes state along allowed edges, and a rejected move leaves the card where it was.
func TestKanban_RandomWalkStaysOnRules(t *testing.T) {
	all := []State{NotPlanned, Planned, Prepared, InProgress, Completed, Emptied}
	rapid.Check(t, func(t *rapid.T) {
		k := &Kanban{ID: "k", State: NotPlanned}
		for step := rapid.IntRange(1, 40).Draw(t, "steps"); step > 0; step-- {
			to := rapid.SampledFrom(all).Draw(t, "to")
			before := k.State
			err := k.Move(to)
			if err == nil && !oracle(before, to) {
				t.Fatalf("accepted %s → %s", before, to)
			}
			if err != nil && k.State != before {
				t.Fatalf("rejected move changed state %s → %s", before, k.State)
			}
		}
	})
}

// TestEventKanbanSimulation is the lean-kanban-sim fixture (AC-24.6, AC-24.7):
// a sales line for 50 with an event rule of fixed qty 20 yields three cards that
// complete through the board and post 60 of receipts.
func TestEventKanbanSimulation(t *testing.T) {
	n := 0
	newID := func() string { n++; return fmt.Sprintf("card-%d", n) }
	cards, err := GenerateEventKanbans("rule-1", decimal.NewFromInt(50), decimal.NewFromInt(20), false, newID)
	if err != nil {
		t.Fatal(err)
	}
	if len(cards) != 3 {
		t.Fatalf("cards = %d, want 3", len(cards))
	}
	if err := cards[0].Move(Completed); err == nil {
		t.Fatal("Planned → Completed accepted; the board must revert (AC-24.7)")
	}
	if cards[0].State != Planned {
		t.Fatalf("state changed to %s after rejected drag", cards[0].State)
	}
	received := decimal.Zero
	for i := range cards {
		for _, to := range []State{Prepared, InProgress, Completed} {
			if err := cards[i].Move(to); err != nil {
				t.Fatalf("card %s: %v", cards[i].ID, err)
			}
		}
		received = received.Add(cards[i].Qty)
	}
	if !received.Equal(decimal.NewFromInt(60)) {
		t.Fatalf("RAF total = %s, want 60", received)
	}
}

func TestGenerateEventKanbans_PropagatesErrors(t *testing.T) {
	if _, err := GenerateEventKanbans("r", decimal.Zero, decimal.NewFromInt(20), false, func() string { return "" }); err == nil {
		t.Fatal("zero demand accepted")
	}
}

// BR-24.6: a fixed-rule card returns to Not planned when emptied; an
// event-rule card is spent and may not.
func TestCheckRuleMove_EmptiedCardsReuseOnlyForFixedRules(t *testing.T) {
	if err := CheckRuleMove(ReplenishFixed, Emptied, NotPlanned); err != nil {
		t.Fatalf("fixed card reuse refused: %v", err)
	}
	for _, r := range []Replenishment{ReplenishEvent, ReplenishScheduled} {
		if err := CheckRuleMove(r, Emptied, NotPlanned); err == nil || err.(*Error).Code != CodeMove {
			t.Fatalf("%s card reused: %v", r, err)
		}
	}
	if err := CheckRuleMove(ReplenishEvent, Planned, Completed); err == nil || err.(*Error).Code != CodeMove {
		t.Fatalf("illegal move passed: %v", err)
	}
	if err := CheckRuleMove(ReplenishEvent, Completed, Emptied); err != nil {
		t.Fatalf("legal move refused: %v", err)
	}
}
