package domain

import (
	"errors"
	"testing"

	"pgregory.net/rapid"
)

var allStatuses = []Status{StatusOnOrder, StatusReservedOrdered, StatusReservedPhysical, StatusPicked, StatusDeducted, StatusSold,
	StatusOrdered, StatusArrived, StatusRegistered, StatusReceived, StatusPurchased}

func TestCanStep_table(t *testing.T) {
	tests := []struct {
		from, to Status
		want     bool
	}{
		{StatusOrdered, StatusReceived, true},  // skip forward
		{StatusReceived, StatusOrdered, false}, // backward
		{StatusReceived, StatusReceived, false},
		{StatusOnOrder, StatusSold, true},
		{StatusDeducted, StatusReceived, false}, // cross direction
		{"BOGUS", StatusSold, false},
	}
	for _, tt := range tests {
		if got := CanStep(tt.from, tt.to); got != tt.want {
			t.Errorf("CanStep(%s,%s) = %v, want %v", tt.from, tt.to, got, tt.want)
		}
	}
}

func TestStep_errors(t *testing.T) {
	if err := Step(StatusReceived, StatusArrived); !errors.Is(err, ErrBackwardStep) {
		t.Fatalf("backward step: got %v", err)
	}
	if err := Step(StatusReceived, "NOPE"); !errors.Is(err, ErrUnknownStatus) {
		t.Fatalf("unknown target: got %v", err)
	}
	if err := Step(StatusArrived, StatusPurchased); err != nil {
		t.Fatalf("forward step rejected: %v", err)
	}
}

// Property: no sequence of forward steps ever goes backward or changes
// direction, and every accepted step strictly increases rank.
func TestStatusMonotonicityProperty(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		cur := rapid.SampledFrom(allStatuses).Draw(t, "start")
		for i := 0; i < 20; i++ {
			next := rapid.SampledFrom(allStatuses).Draw(t, "next")
			if !CanStep(cur, next) {
				continue
			}
			fd, fr, _ := Lookup(cur)
			nd, nr, _ := Lookup(next)
			if fd != nd || nr <= fr {
				t.Fatalf("accepted backward or cross-direction step %s -> %s", cur, next)
			}
			cur = next
		}
	})
}

func TestIsFinancialAndDirection(t *testing.T) {
	if !IsFinancial(StatusSold) || !IsFinancial(StatusPurchased) || IsFinancial(StatusDeducted) {
		t.Fatal("only SOLD and PURCHASED are financial")
	}
	if d, err := DirectionFor(StatusPicked); err != nil || d != IssueDirection {
		t.Fatalf("PICKED direction = %v, %v", d, err)
	}
	if _, err := DirectionFor("X"); err == nil {
		t.Fatal("unknown status must error")
	}
}
