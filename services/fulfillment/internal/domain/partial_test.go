package domain

import (
	"errors"
	"testing"

	"github.com/shopspring/decimal"
)

func partialQ(s string) decimal.Decimal {
	v, err := decimal.NewFromString(s)
	if err != nil {
		panic(err)
	}
	return v
}

// TestConfirmPickShortLeavesBackorder covers AC-28.4: 100 planned, 90 picked.
func TestConfirmPickShortLeavesBackorder(t *testing.T) {
	line := LineQty{Planned: partialQ("100")}
	got, backorder, err := ConfirmPick(line, partialQ("90"))
	if err != nil {
		t.Fatalf("ConfirmPick: %v", err)
	}
	if !got.Picked.Equal(partialQ("90")) || !got.Confirmed {
		t.Fatalf("picked = %s confirmed = %v, want 90 true", got.Picked, got.Confirmed)
	}
	if !backorder.Equal(partialQ("10")) {
		t.Fatalf("backorder = %s, want 10", backorder)
	}
	// The slip ships the 90; the shipment stays partially shipped for the 10.
	got.Packed, got.Shipped = got.Picked, got.Picked
	if PostTarget([]LineQty{got}) != StatusPartiallyShipped {
		t.Fatalf("PostTarget after a short pick = %s, want PARTIALLY_SHIPPED", PostTarget([]LineQty{got}))
	}
}

func TestConfirmPickRejectsBelowPackedAndOverPick(t *testing.T) {
	cases := []struct {
		name    string
		line    LineQty
		picked  string
		wantErr error
	}{
		{"over-pick", LineQty{Planned: partialQ("10")}, "11", ErrOverPick},
		{"below shipped", LineQty{Planned: partialQ("10"), Packed: partialQ("6"), Shipped: partialQ("6")}, "5", ErrPickBelowPacked},
		{"below packed", LineQty{Planned: partialQ("10"), Packed: partialQ("4")}, "3", ErrPickBelowPacked},
		{"negative", LineQty{Planned: partialQ("10")}, "-1", ErrNegativeQty},
		{"re-pick equal to shipped", LineQty{Planned: partialQ("10"), Packed: partialQ("6"), Shipped: partialQ("6")}, "6", nil},
		{"re-pick the remainder", LineQty{Planned: partialQ("10"), Packed: partialQ("6"), Shipped: partialQ("6")}, "10", nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, _, err := ConfirmPick(c.line, partialQ(c.picked))
			if c.wantErr == nil && err != nil || c.wantErr != nil && !errors.Is(err, c.wantErr) {
				t.Fatalf("err = %v, want %v", err, c.wantErr)
			}
		})
	}
}

func TestPostTargetPartialUntilEveryScheduleLineShips(t *testing.T) {
	cases := []struct {
		name  string
		lines []LineQty
		want  ShipmentStatus
	}{
		{"all shipped", []LineQty{{Planned: partialQ("10"), Shipped: partialQ("10")}}, StatusShipped},
		{"one line short", []LineQty{
			{Planned: partialQ("10"), Shipped: partialQ("10")},
			{Planned: partialQ("10"), Shipped: partialQ("6")},
		}, StatusPartiallyShipped},
		{"second round completes", []LineQty{
			{Planned: partialQ("10"), Shipped: partialQ("10")},
			{Planned: partialQ("10"), Shipped: partialQ("10")},
		}, StatusShipped},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := PostTarget(c.lines); got != c.want {
				t.Fatalf("PostTarget = %s, want %s", got, c.want)
			}
		})
	}
}

func TestPartialShipmentStateMachine(t *testing.T) {
	cases := []struct {
		from, to ShipmentStatus
		want     bool
	}{
		{StatusPacked, StatusPartiallyShipped, true},
		{StatusPartiallyShipped, StatusPicking, true}, // re-pick the remainder
		{StatusPartiallyShipped, StatusPacked, true},  // its slip is cancelled
		{StatusPartiallyShipped, StatusCancelled, false},
		{StatusPartiallyShipped, StatusShipped, false}, // only a posted slip ships
		{StatusPartiallyShipped, StatusDelivered, false},
	}
	for _, c := range cases {
		if got := CanTransition(c.from, c.to); got != c.want {
			t.Errorf("CanTransition(%s, %s) = %v, want %v", c.from, c.to, got, c.want)
		}
	}
}

func TestCancelShipmentRules(t *testing.T) {
	open := []LineQty{{Planned: partialQ("5")}}
	shipped := []LineQty{{Planned: partialQ("5"), Shipped: partialQ("1")}}
	cases := []struct {
		name       string
		from       ShipmentStatus
		lines      []LineQty
		containers int
		want       ShipmentStatus
		wantErr    bool
	}{
		{"released cancels", StatusReleased, open, 0, StatusCancelled, false},
		{"packed without containers cancels", StatusPacked, open, 0, StatusCancelled, false},
		{"packed container blocks", StatusPacked, open, 1, "", true},
		{"shipped quantity blocks", StatusPartiallyShipped, shipped, 0, "", true},
		{"already cancelled", StatusCancelled, open, 0, "", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := CancelShipment(c.from, c.lines, c.containers)
			if (err != nil) != c.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, c.wantErr)
			}
			if !c.wantErr && got != c.want {
				t.Fatalf("status = %s, want %s", got, c.want)
			}
		})
	}
}
