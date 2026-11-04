package domain

import (
	"errors"
	"testing"
)

func TestConfirmPickWithinTolerance(t *testing.T) {
	line := LineQty{Planned: d("100")}
	cases := []struct {
		name   string
		line   LineQty
		picked string
		pct    string
		want   error
		back   string
	}{
		{"exact, no tolerance", line, "100", "0", nil, "0"},
		{"short, tolerance irrelevant", line, "90", "5", nil, "10"},
		{"over, no tolerance is FUL-0004", line, "101", "0", ErrOverPick, ""},
		{"over within 5%", line, "105", "5", nil, "0"},
		{"over beyond 5% is FUL-0005", line, "105.000001", "5", ErrOverShip, ""},
		{"fractional tolerance", line, "102.5", "2.5", nil, "0"},
		{"fractional tolerance beyond", line, "102.6", "2.5", ErrOverShip, ""},
		{"over but below packed", LineQty{Planned: d("100"), Packed: d("106"), Shipped: d("106")}, "105", "10", ErrPickBelowPacked, ""},
		{"negative", line, "-1", "5", ErrNegativeQty, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, back, err := ConfirmPickWithin(c.line, d(c.picked), d(c.pct))
			if c.want != nil {
				if !errors.Is(err, c.want) {
					t.Fatalf("err = %v, want %v", err, c.want)
				}
				return
			}
			if err != nil || !back.Equal(d(c.back)) || !got.Picked.Equal(d(c.picked)) {
				t.Fatalf("got %+v back %s err %v", got, back, err)
			}
		})
	}
}

func TestMaxPickRoundsDownToSixDecimals(t *testing.T) {
	if got := MaxPick(d("3"), d("0.3333")); !got.Equal(d("3.009999")) {
		t.Fatalf("MaxPick = %s, want 3.009999", got)
	}
	if got := MaxPick(d("10"), d("0")); !got.Equal(d("10")) {
		t.Fatalf("MaxPick with no tolerance = %s", got)
	}
}

// A line picked and shipped above its plan completes the shipment, closes
// nothing and is not a remainder.
func TestOverShippedLineCompletesTheShipment(t *testing.T) {
	over := LineQty{Planned: d("100"), Picked: d("105"), Packed: d("105"), Shipped: d("105")}
	if got := PostTarget([]LineQty{over}); got != StatusShipped {
		t.Fatalf("PostTarget = %s, want SHIPPED", got)
	}
	short := LineQty{Planned: d("10"), Picked: d("4"), Packed: d("4"), Shipped: d("4")}
	if got := PostTarget([]LineQty{over, short}); got != StatusPartiallyShipped {
		t.Fatalf("PostTarget with a short line = %s, want PARTIALLY_SHIPPED", got)
	}
	_, closed, err := CloseRemainder(StatusPartiallyShipped, []LineQty{over, short})
	if err != nil || !closed[0].Closed.IsZero() || !closed[1].Closed.Equal(d("6")) {
		t.Fatalf("CloseRemainder = %+v, %v; want 0 and 6 closed", closed, err)
	}
}
