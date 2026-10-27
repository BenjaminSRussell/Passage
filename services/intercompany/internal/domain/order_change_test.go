package domain

import (
	"strings"
	"testing"

	"pgregory.net/rapid"
)

func TestDecideOrderChange_DefaultTolerance(t *testing.T) {
	tol := DefaultTolerance() // qty ±0, price ±0.5 %, date ±2 days
	cases := []struct {
		name       string
		ch         OrderChange
		wantAction string
		wantReason []string
	}{
		{name: "unchanged", ch: OrderChange{OldQty: lpDec("100"), NewQty: lpDec("100"), OldPrice: lpDec("25"), NewPrice: lpDec("25")},
			wantAction: ChangeUpdateSilently},
		{name: "price up 0.4 percent is silent", ch: OrderChange{OldQty: lpDec("100"), NewQty: lpDec("100"), OldPrice: lpDec("25"), NewPrice: lpDec("25.10")},
			wantAction: ChangeUpdateSilently},
		{name: "price down exactly 0.5 percent is silent", ch: OrderChange{OldQty: lpDec("100"), NewQty: lpDec("100"), OldPrice: lpDec("100"), NewPrice: lpDec("99.5")},
			wantAction: ChangeUpdateSilently},
		{name: "price up 0.6 percent re-approves", ch: OrderChange{OldQty: lpDec("100"), NewQty: lpDec("100"), OldPrice: lpDec("100"), NewPrice: lpDec("100.6")},
			wantAction: ChangeReApprove, wantReason: []string{"price moved 0.60% against a tolerance of 0.5%"}},
		{name: "date two days earlier is silent", ch: OrderChange{OldQty: lpDec("1"), NewQty: lpDec("1"), OldPrice: lpDec("1"), NewPrice: lpDec("1"), DateShiftDays: -2},
			wantAction: ChangeUpdateSilently},
		{name: "date three days later re-approves", ch: OrderChange{OldQty: lpDec("1"), NewQty: lpDec("1"), OldPrice: lpDec("1"), NewPrice: lpDec("1"), DateShiftDays: 3},
			wantAction: ChangeReApprove, wantReason: []string{"date moved 3 days against a tolerance of 2 days"}},
		{name: "any quantity change re-approves", ch: OrderChange{OldQty: lpDec("100"), NewQty: lpDec("101"), OldPrice: lpDec("25"), NewPrice: lpDec("25")},
			wantAction: ChangeReApprove, wantReason: []string{"quantity changed from 100 to 101"}},
		{name: "price from zero re-approves", ch: OrderChange{OldQty: lpDec("1"), NewQty: lpDec("1"), OldPrice: lpDec("0"), NewPrice: lpDec("5")},
			wantAction: ChangeReApprove, wantReason: []string{"price changed from 0 to 5"}},
		{name: "every breach is listed", ch: OrderChange{OldQty: lpDec("100"), NewQty: lpDec("90"), OldPrice: lpDec("25"), NewPrice: lpDec("30"), DateShiftDays: -9},
			wantAction: ChangeReApprove, wantReason: []string{"quantity changed", "price moved 20.00%", "date moved 9 days"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := DecideOrderChange(tol, tc.ch)
			if got.Action != tc.wantAction {
				t.Fatalf("action = %s, want %s (%v)", got.Action, tc.wantAction, got.Reasons)
			}
			if len(got.Reasons) != len(tc.wantReason) {
				t.Fatalf("reasons = %q, want %d", got.Reasons, len(tc.wantReason))
			}
			for i, want := range tc.wantReason {
				if !strings.HasPrefix(got.Reasons[i], want) {
					t.Errorf("reason %d = %q, want prefix %q", i, got.Reasons[i], want)
				}
			}
			switch got.Action {
			case ChangeReApprove:
				if got.Code != CodeChangeTolerance {
					t.Errorf("code = %q, want %s", got.Code, CodeChangeTolerance)
				}
			case ChangeUpdateSilently:
				if got.Code != "" {
					t.Errorf("silent change carries code %s", got.Code)
				}
			}
		})
	}
}

// TestDecideOrderChange_WiderTolerance checks the pairing's own tolerance is
// used, not the default.
func TestDecideOrderChange_WiderTolerance(t *testing.T) {
	tol := Tolerance{PricePct: lpDec("2"), DateDays: 5}
	ch := OrderChange{OldQty: lpDec("10"), NewQty: lpDec("10"), OldPrice: lpDec("100"), NewPrice: lpDec("102"), DateShiftDays: 5}
	if got := DecideOrderChange(tol, ch); got.Action != ChangeUpdateSilently {
		t.Fatalf("2 %% and 5 days within a 2 %% / 5 day tolerance: %+v", got)
	}
	ch.NewPrice = lpDec("102.01")
	if got := DecideOrderChange(tol, ch); got.Action != ChangeReApprove {
		t.Fatalf("2.01 %% beyond a 2 %% tolerance: %+v", got)
	}
}

// TestDecideOrderChange_Properties: the decision is symmetric in the date
// direction, and a change is silent exactly when no reason is listed.
func TestDecideOrderChange_Properties(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		tol := Tolerance{PricePct: lpDec(rapid.SampledFrom([]string{"0", "0.5", "2", "10"}).Draw(t, "pct")), DateDays: rapid.IntRange(0, 30).Draw(t, "days")}
		price := func(label string) string {
			return rapid.SampledFrom([]string{"0", "10", "10.05", "10.2", "11", "25"}).Draw(t, label)
		}
		ch := OrderChange{
			OldQty: lpDec(rapid.SampledFrom([]string{"5", "6"}).Draw(t, "oldQty")), NewQty: lpDec(rapid.SampledFrom([]string{"5", "6"}).Draw(t, "newQty")),
			OldPrice: lpDec(price("oldPrice")), NewPrice: lpDec(price("newPrice")), DateShiftDays: rapid.IntRange(-40, 40).Draw(t, "shift"),
		}
		got := DecideOrderChange(tol, ch)
		mirrored := ch
		mirrored.DateShiftDays = -ch.DateShiftDays
		if DecideOrderChange(tol, mirrored).Action != got.Action {
			t.Fatalf("earlier and later shifts decide differently for %+v", ch)
		}
		if (got.Action == ChangeUpdateSilently) != (len(got.Reasons) == 0) {
			t.Fatalf("action %s with reasons %q", got.Action, got.Reasons)
		}
		if !ch.OldQty.Equal(ch.NewQty) && got.Action != ChangeReApprove {
			t.Fatalf("quantity change %s -> %s was silent", ch.OldQty, ch.NewQty)
		}
	})
}
