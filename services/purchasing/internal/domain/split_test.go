package domain

import (
	"testing"

	"github.com/shopspring/decimal"
	"pgregory.net/rapid"
)

func TestSplitRequisitionLine_Example(t *testing.T) {
	res, err := SplitRequisitionLine(d("100"), d("1000.00"), []decimal.Decimal{d("40"), d("35")}, 2)
	if err != nil {
		t.Fatal(err)
	}
	if !res.Pieces[0].Qty.Equal(d("40")) || !res.Pieces[0].Amount.Equal(d("400")) ||
		!res.Pieces[1].Amount.Equal(d("350")) || !res.Remaining.Qty.Equal(d("25")) || !res.Remaining.Amount.Equal(d("250")) {
		t.Fatalf("split = %+v", res)
	}
}

func TestSplitRequisitionLine_Refusals(t *testing.T) {
	for name, parts := range map[string][]decimal.Decimal{
		"none":         nil,
		"zero":         {d("0")},
		"negative":     {d("-1")},
		"all of it":    {d("10")},
		"more":         {d("6"), d("4")},
		"seven places": {d("1.1234567")},
	} {
		if _, err := SplitRequisitionLine(d("10"), d("100"), parts, 2); err == nil {
			t.Errorf("%s: want an error", name)
		}
	}
	many := make([]decimal.Decimal, MaxSplitParts+1)
	for i := range many {
		many[i] = d("0.5")
	}
	if _, err := SplitRequisitionLine(d("1000"), d("100"), many, 2); err == nil {
		t.Error("more than the maximum parts: want an error")
	}
}

// The ties that rounded shares would overshoot (0.05 over 10 units cut into threes) still add up.
func TestSplitRequisitionLine_RoundingNeverOvershoots(t *testing.T) {
	res, err := SplitRequisitionLine(d("10"), d("0.05"), []decimal.Decimal{d("3"), d("3"), d("3")}, 2)
	if err != nil {
		t.Fatal(err)
	}
	sum := res.Remaining.Amount
	for _, p := range res.Pieces {
		if p.Amount.Sign() < 0 {
			t.Fatalf("negative share %s", p.Amount)
		}
		sum = sum.Add(p.Amount)
	}
	if !sum.Equal(d("0.05")) || res.Remaining.Amount.Sign() < 0 {
		t.Fatalf("amounts add to %s, remaining %s", sum, res.Remaining.Amount)
	}
}

// Quantities and amounts always add up to the line, every share is not negative and the original keeps a quantity.
func TestSplitRequisitionLine_SumsAreExact(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		minor := int32(rapid.IntRange(0, 3).Draw(t, "minor"))
		qty := decimal.New(rapid.Int64Range(2, 5_000_000).Draw(t, "qty"), -3)
		amount := decimal.New(rapid.Int64Range(0, 90_000_000).Draw(t, "amount"), -minor)
		n := rapid.IntRange(1, 6).Draw(t, "n")
		parts := make([]decimal.Decimal, n)
		for i := range parts {
			parts[i] = decimal.New(rapid.Int64Range(1, 400_000).Draw(t, "part"), -3)
		}
		res, err := SplitRequisitionLine(qty, amount, parts, minor)
		if err != nil {
			return // parts that do not leave a remainder are refused; the example test covers it
		}
		sumQty, sumAmount := res.Remaining.Qty, res.Remaining.Amount
		if !res.Remaining.Qty.IsPositive() || res.Remaining.Amount.Sign() < 0 {
			t.Fatalf("remaining %+v", res.Remaining)
		}
		for _, p := range res.Pieces {
			if p.Amount.Sign() < 0 {
				t.Fatalf("negative share %+v", p)
			}
			sumQty, sumAmount = sumQty.Add(p.Qty), sumAmount.Add(p.Amount)
		}
		if !sumQty.Equal(qty) || !sumAmount.Equal(amount) {
			t.Fatalf("qty %s vs %s, amount %s vs %s", sumQty, qty, sumAmount, amount)
		}
	})
}
