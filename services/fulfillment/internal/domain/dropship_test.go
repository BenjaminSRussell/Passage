package domain

import (
	"errors"
	"testing"

	"github.com/shopspring/decimal"
)

// AC-28.5: a confirmed drop-ship line books Dr 5000 / Cr 2110 at PO cost and
// nothing to 1400 or 1405.
func TestDropShipPosting_CostOfGoodsSoldAgainstGRIR(t *testing.T) {
	j, err := DropShipPosting("ol-1", decimal.Zero, decimal.RequireFromString("4"), decimal.RequireFromString("12.505"), "USD")
	if err != nil || len(j) != 2 {
		t.Fatalf("journal = %+v, %v", j, err)
	}
	if j[0].Account != "5000" || j[1].Account != "2110" || !j[0].Amount.Value.Equal(decimal.RequireFromString("50.02")) || !j[1].Amount.Value.Equal(decimal.RequireFromString("-50.02")) {
		t.Fatalf("journal = %+v, want Dr 5000 / Cr 2110 50.02", j)
	}
	for _, l := range j {
		if l.Account == AccountInventory || l.Account == AccountDeliveredNotInvoiced {
			t.Fatalf("drop-ship touched %s", l.Account)
		}
	}
}

func TestDropShipPosting_ZeroCostPostsNothingAndZeroQtyIsRefused(t *testing.T) {
	if j, err := DropShipPosting("ol", decimal.Zero, decimal.NewFromInt(3), decimal.Zero, "USD"); err != nil || len(j) != 0 {
		t.Fatalf("zero cost = %+v, %v", j, err)
	}
	if _, err := DropShipPosting("ol", decimal.Zero, decimal.Zero, decimal.NewFromInt(1), "USD"); !errors.Is(err, ErrNegativeQty) {
		t.Fatalf("zero qty: %v", err)
	}
}

// A vendor that ships 3 units at 0.125 one at a time books 0.13, 0.12 and 0.13:
// the parts add up to the whole-line amount 0.38, where rounding each part on
// its own would book 0.39.
func TestDropShipPosting_PartsAddUpToTheWholeLine(t *testing.T) {
	d := decimal.RequireFromString
	cost := d("0.125")
	whole, err := DropShipPosting("ol", decimal.Zero, d("3"), cost, "USD")
	if err != nil || !whole[0].Amount.Value.Equal(d("0.38")) {
		t.Fatalf("whole = %+v, %v", whole, err)
	}
	var total decimal.Decimal
	for i, want := range []string{"0.13", "0.12", "0.13"} {
		j, err := DropShipPosting("ol", decimal.NewFromInt(int64(i)), d("1"), cost, "USD")
		if err != nil || len(j) != 2 || !j[0].Amount.Value.Equal(d(want)) || !j[1].Amount.Value.Equal(d(want).Neg()) {
			t.Fatalf("part %d = %+v, %v, want %s", i+1, j, err, want)
		}
		total = total.Add(j[0].Amount.Value)
	}
	if !total.Equal(whole[0].Amount.Value) {
		t.Fatalf("parts add up to %s, whole line is %s", total, whole[0].Amount.Value)
	}
	if _, err := DropShipPosting("ol", d("-1"), d("1"), cost, "USD"); !errors.Is(err, ErrNegativeQty) {
		t.Fatalf("negative confirmed before: %v", err)
	}
}

func TestDropShipOpenQtyAndStatusAfter(t *testing.T) {
	d := decimal.RequireFromString
	if got := DropShipOpenQty(d("10"), d("4"), d("1")); !got.Equal(d("5")) {
		t.Fatalf("open = %s, want 5", got)
	}
	for _, c := range []struct{ qty, confirmed, closed, want string }{
		{"10", "4", "0", DropShipPartial},
		{"10", "10", "0", DropShipConfirmed},
		{"10", "6", "4", DropShipConfirmed},
		{"10", "6", "3", DropShipPartial},
	} {
		if got := DropShipStatusAfter(d(c.qty), d(c.confirmed), d(c.closed)); got != c.want {
			t.Fatalf("%+v: status %s", c, got)
		}
	}
}
