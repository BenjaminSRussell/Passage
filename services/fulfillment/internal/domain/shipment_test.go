package domain

import (
	"errors"
	"testing"

	"github.com/shopspring/decimal"
)

func d(s string) decimal.Decimal { return decimal.RequireFromString(s) }

func TestCanTransition(t *testing.T) {
	tests := []struct {
		from, to ShipmentStatus
		want     bool
	}{
		{StatusOpen, StatusReleased, true},
		{StatusReleased, StatusPicking, true},
		{StatusPicked, StatusPacked, true},
		{StatusPacked, StatusShipped, true},
		{StatusShipped, StatusPacked, true}, // packing-slip cancellation
		{StatusShipped, StatusDelivered, true},
		{StatusPacked, StatusCancelled, true},
		{StatusShipped, StatusCancelled, false},
		{StatusOpen, StatusShipped, false},
		{StatusDelivered, StatusCancelled, false},
		{StatusCancelled, StatusOpen, false},
	}
	for _, tt := range tests {
		if got := CanTransition(tt.from, tt.to); got != tt.want {
			t.Errorf("CanTransition(%s,%s) = %v, want %v", tt.from, tt.to, got, tt.want)
		}
	}
}

func TestAdvance(t *testing.T) {
	if got, err := Advance(StatusPicked, StatusPicked); err != nil || got != StatusPicked {
		t.Fatalf("same-state advance = %s, %v", got, err)
	}
	if _, err := Advance(StatusOpen, StatusShipped); !errors.Is(err, ErrInvalidMove) {
		t.Fatalf("err = %v, want ErrInvalidMove", err)
	}
}

// TestConfirmPick_AC28_4: a 100-unit line with 90 picked has picked 90 and 10 backordered.
func TestConfirmPick_AC28_4(t *testing.T) {
	line := LineQty{Planned: d("100")}
	got, backorder, err := ConfirmPick(line, d("90"))
	if err != nil {
		t.Fatal(err)
	}
	if !got.Picked.Equal(d("90")) || !backorder.Equal(d("10")) || !got.Confirmed {
		t.Fatalf("picked=%s backorder=%s confirmed=%v", got.Picked, backorder, got.Confirmed)
	}
}

func TestConfirmPickRules(t *testing.T) {
	line := LineQty{Planned: d("5")}
	tests := []struct {
		name    string
		picked  string
		wantErr error
	}{
		{"exact pick", "5", nil},
		{"zero pick is a full short", "0", nil},
		{"over-pick rejected", "6", ErrOverPick},
		{"negative rejected", "-1", ErrNegativeQty},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, _, err := ConfirmPick(line, d(tt.picked))
			if !errors.Is(err, tt.wantErr) && err != tt.wantErr {
				t.Fatalf("err = %v, want %v", err, tt.wantErr)
			}
		})
	}
}

func TestCanPack(t *testing.T) {
	line := LineQty{Planned: d("10"), Picked: d("9"), Packed: d("6")}
	if err := CanPack(line, d("3")); err != nil {
		t.Fatalf("pack within picked: %v", err)
	}
	if err := CanPack(line, d("4")); !errors.Is(err, ErrOverPack) {
		t.Fatalf("over-pack err = %v", err)
	}
	if err := CanPack(line, d("0")); !errors.Is(err, ErrNegativeQty) {
		t.Fatalf("zero pack err = %v", err)
	}
}

func TestAllConfirmedAndPacked(t *testing.T) {
	done := LineQty{Planned: d("2"), Picked: d("2"), Packed: d("2"), Confirmed: true}
	short := LineQty{Planned: d("2"), Picked: d("1"), Packed: d("1"), Confirmed: true}
	open := LineQty{Planned: d("2")}
	tests := []struct {
		name        string
		lines       []LineQty
		confirmed   bool
		fullyPacked bool
	}{
		{"empty", nil, false, false},
		{"all done incl. short", []LineQty{done, short}, true, true},
		{"one unconfirmed", []LineQty{done, open}, false, false},
		{"packed behind picked", []LineQty{done, {Planned: d("2"), Picked: d("1"), Packed: d("0"), Confirmed: true}}, true, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := AllConfirmed(tt.lines); got != tt.confirmed {
				t.Errorf("AllConfirmed = %v", got)
			}
			if got := AllPacked(tt.lines); got != tt.fullyPacked {
				t.Errorf("AllPacked = %v", got)
			}
		})
	}
}
