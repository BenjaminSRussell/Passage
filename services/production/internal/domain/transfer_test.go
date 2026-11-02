package domain

import (
	"testing"

	"github.com/shopspring/decimal"
	"pgregory.net/rapid"
)

// AC-21.11: 100 units with 305 kg consumed and 20.00 h posted, 40 split off.
func TestPlanTransfer_AC2111(t *testing.T) {
	tr := PlanTransfer(d("100"), d("40"), "USD",
		[]TransferLineIn{{LineNo: "10", ItemID: "i", Qty: d("305"), Amount: d("3111")}},
		[]TransferOpIn{{OperNumber: 20, Hours: d("20"), Good: d("100"), Labor: d("600"), Overhead: d("100.05")}})
	if len(tr.Lines) != 2 {
		t.Fatalf("lines = %d", len(tr.Lines))
	}
	if got := tr.Lines[0].Qty.StringFixed(6); got != "122.000000" {
		t.Errorf("moved kg = %s", got)
	}
	if got := tr.Lines[1].Qty.StringFixed(2); got != "8.00" {
		t.Errorf("moved hours = %s", got)
	}
	if !tr.Material.Equal(d("1244.40")) || !tr.Labor.Equal(d("240")) || !tr.Overhead.Equal(d("40.02")) {
		t.Errorf("totals = %s %s %s", tr.Material, tr.Labor, tr.Overhead)
	}
}

func TestPlanTransfer_LeavesOutWhatRoundsToNothing(t *testing.T) {
	tr := PlanTransfer(d("1000000"), d("1"), "USD", []TransferLineIn{{LineNo: "10", Qty: d("0.1"), Amount: d("0.01")}}, []TransferOpIn{{OperNumber: 10}})
	if !tr.Empty() {
		t.Errorf("nothing should move, got %+v", tr.Lines)
	}
}

// BR-21.11 rounds an amount's share once, to the minor unit: 1.00 × 1 ÷ 200.000001
// is 0.0049999999750, which is 0.00 (rounding to 8 places first would make it 0.01).
func TestPlanTransfer_RoundsAmountsOnce(t *testing.T) {
	tr := PlanTransfer(d("200.000001"), d("1"), "USD", []TransferLineIn{{LineNo: "10", Amount: d("1.00")}},
		[]TransferOpIn{{OperNumber: 10, Labor: d("1.00"), Overhead: d("1.00")}})
	if !tr.Empty() {
		t.Errorf("a share below half a cent moved: %+v", tr.Lines)
	}
}

func TestTransferPostings(t *testing.T) {
	p := Profile{WIP: "1400"}
	tr := Transfer{Material: d("10.50"), Labor: d("3"), Overhead: d("0")}
	for _, out := range []bool{true, false} {
		ps := TransferPostings(p, tr, out)
		if len(ps) != 4 || !Balanced(ps) {
			t.Fatalf("out=%v postings = %+v", out, ps)
		}
		want := d("13.50")
		if out {
			want = want.Neg()
		}
		if got := Net(ps, true); !got.Equal(want) {
			t.Errorf("out=%v WIP net = %s, want %s", out, got, want)
		}
	}
	if got := TransferPostings(p, Transfer{}, true); len(got) != 0 {
		t.Errorf("an empty transfer posts %d rows", len(got))
	}
}

// BR-21.11 property: the moved share never exceeds what was posted, the residual
// stays on the original, and the two sides' WIP moves cancel.
func TestPlanTransfer_Property(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		order := decimal.New(rapid.Int64Range(2, 1_000_000).Draw(t, "order"), -2)
		split := decimal.New(rapid.Int64Range(1, order.Shift(2).IntPart()-1).Draw(t, "split"), -2)
		qty := decimal.New(rapid.Int64Range(0, 10_000_000_000).Draw(t, "qty"), -6)
		amount := decimal.New(rapid.Int64Range(0, 100_000_000).Draw(t, "amount"), -2)
		hours := decimal.New(rapid.Int64Range(0, 100_000).Draw(t, "hours"), -2)
		tr := PlanTransfer(order, split, "USD", []TransferLineIn{{LineNo: "10", Qty: qty, Amount: amount}},
			[]TransferOpIn{{OperNumber: 10, Hours: hours, Labor: amount, Overhead: amount}})
		for _, l := range tr.Lines {
			if l.Qty.IsNegative() || l.Qty.GreaterThan(qty.Add(hours)) || l.Amount.GreaterThan(amount) || l.Overhead.GreaterThan(amount) {
				t.Fatalf("moved more than posted: %+v", l)
			}
		}
		p := Profile{WIP: "1400"}
		out, in := TransferPostings(p, tr, true), TransferPostings(p, tr, false)
		if !Balanced(out) || !Balanced(in) || !Net(out, true).Add(Net(in, true)).IsZero() {
			t.Fatalf("transfer does not cancel: %v %v", out, in)
		}
		// Within a cent of the exact proportion.
		exact := amount.Mul(split).Div(order)
		if tr.Material.Sub(exact).Abs().GreaterThan(d("0.01")) {
			t.Fatalf("material %s too far from %s", tr.Material, exact)
		}
	})
}

// The setup and process jobs of a job-card operation move exactly the hours the
// operation moves, never more than either holds: 2.25 h of setup and 7.75 h of
// process, 1 of 3 units split off, moves 3.33 h as 0.75 + 2.58.
func TestSplitJobHours(t *testing.T) {
	moved := PlanTransfer(d("3"), d("1"), "USD", nil, []TransferOpIn{{OperNumber: 10, Hours: d("10")}}).Lines[0].Qty
	setup, process := SplitJobHours(d("2.25"), d("7.75"), moved, d("1"), d("3"))
	if !setup.Equal(d("0.75")) || !process.Equal(d("2.58")) || !moved.Equal(d("3.33")) {
		t.Fatalf("moved %s as setup %s + process %s", moved, setup, process)
	}
	rapid.Check(t, func(t *rapid.T) {
		s := decimal.New(int64(rapid.IntRange(0, 100000).Draw(t, "setup")), -2)
		p := decimal.New(int64(rapid.IntRange(0, 100000).Draw(t, "process")), -2)
		qty := decimal.NewFromInt(int64(rapid.IntRange(2, 1000).Draw(t, "qty")))
		split := decimal.NewFromInt(int64(rapid.IntRange(1, int(qty.IntPart())-1).Draw(t, "split")))
		tr := PlanTransfer(qty, split, "USD", nil, []TransferOpIn{{OperNumber: 10, Hours: s.Add(p)}})
		moved := decimal.Zero
		if len(tr.Lines) == 1 {
			moved = tr.Lines[0].Qty
		}
		ms, mp := SplitJobHours(s, p, moved, split, qty)
		if !ms.Add(mp).Equal(moved) || ms.GreaterThan(s) || mp.GreaterThan(p) || ms.IsNegative() || mp.IsNegative() {
			t.Fatalf("S %s P %s moved %s as %s + %s", s, p, moved, ms, mp)
		}
	})
}
