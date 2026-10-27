package domain

import (
	"errors"
	"fmt"
	"testing"

	"github.com/shopspring/decimal"
	"pgregory.net/rapid"
)

// AC-38.11: freight of 12,000.00 on revenue 300,000 / 150,000 / 50,000 gives
// 7,200.00 / 3,600.00 / 1,200.00 and Σ = 12,000.00.
func TestTopDown_WorkedExample(t *testing.T) {
	res, err := TopDown(dec("12000.00"), []Segment{
		{ID: "north", Basis: dec("300000")}, {ID: "south", Basis: dec("150000")}, {ID: "west", Basis: dec("50000")},
	}, 2)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"7200", "3600", "1200"}
	for i, sh := range res.Shares {
		if !sh.Amount.Equal(dec(want[i])) {
			t.Errorf("%s = %s, want %s", sh.SegmentID, sh.Amount, want[i])
		}
	}
	if !res.Total.Equal(dec("12000")) || len(res.Excluded) != 0 {
		t.Fatalf("total %s excluded %v", res.Total, res.Excluded)
	}
}

// BR-38.13: the rounding residual goes to the largest basis, the first on a tie.
func TestTopDown_ResidualToLargestBasis(t *testing.T) {
	tests := []struct {
		name     string
		amount   string
		segments []Segment
		want     []string
	}{
		{"three equal shares, first takes the cent", "100.00",
			[]Segment{{"a", dec("1")}, {"b", dec("1")}, {"c", dec("1")}}, []string{"33.34", "33.33", "33.33"}},
		{"largest basis last", "10.00",
			[]Segment{{"a", dec("1")}, {"b", dec("1")}, {"c", dec("4")}}, []string{"1.67", "1.67", "6.66"}},
		{"zero decimals currency", "100",
			[]Segment{{"a", dec("2")}, {"b", dec("1")}}, []string{"67", "33"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			scale := int32(2)
			if tc.name == "zero decimals currency" {
				scale = 0
			}
			res, err := TopDown(dec(tc.amount), tc.segments, scale)
			if err != nil {
				t.Fatal(err)
			}
			for i, sh := range res.Shares {
				if !sh.Amount.Equal(dec(tc.want[i])) {
					t.Errorf("%s = %s, want %s", sh.SegmentID, sh.Amount, tc.want[i])
				}
			}
			if !res.Total.Equal(dec(tc.amount)) {
				t.Errorf("total %s, want %s", res.Total, tc.amount)
			}
		})
	}
}

func TestTopDown_ZeroBasisExcluded(t *testing.T) {
	res, err := TopDown(dec("90"), []Segment{{"a", dec("0")}, {"b", dec("2")}, {"c", dec("1")}}, 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Shares) != 2 || res.Shares[0].SegmentID != "b" || !res.Shares[0].Amount.Equal(dec("60")) {
		t.Fatalf("shares = %+v", res.Shares)
	}
	if len(res.Excluded) != 1 || res.Excluded[0] != "a" {
		t.Fatalf("excluded = %v, want [a]", res.Excluded)
	}
}

func TestTopDown_Rejections(t *testing.T) {
	tests := []struct {
		name     string
		amount   string
		segments []Segment
		scale    int32
		target   error
	}{
		{"all zero basis", "10", []Segment{{"a", dec("0")}}, 2, ErrNoTopDownBasis},
		{"negative basis", "10", []Segment{{"a", dec("-1")}, {"b", dec("2")}}, 2, nil},
		{"duplicate segment", "10", []Segment{{"a", dec("1")}, {"a", dec("2")}}, 2, nil},
		{"no segments", "10", nil, 2, nil},
		{"amount finer than the currency", "10.005", []Segment{{"a", dec("1")}}, 2, nil},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := TopDown(dec(tc.amount), tc.segments, tc.scale)
			if err == nil {
				t.Fatal("accepted")
			}
			if tc.target != nil && !errors.Is(err, tc.target) {
				t.Fatalf("err = %v, want %v", err, tc.target)
			}
		})
	}
}

// Property: Σ shares = amount exactly, every share is within one minor unit per
// segment of its exact proportion, and no share is negative for a positive amount.
func TestTopDown_ConservationProperty(t *testing.T) {
	rapid.Check(t, func(rt *rapid.T) {
		amount := decimal.New(rapid.Int64Range(1, 1_000_000_000).Draw(rt, "cents"), -2)
		n := rapid.IntRange(1, 30).Draw(rt, "n")
		segs := make([]Segment, n)
		total := decimal.Zero
		for i := range segs {
			segs[i] = Segment{ID: fmt.Sprintf("s%d", i), Basis: decimal.New(rapid.Int64Range(0, 10_000_000).Draw(rt, "basis"), -2)}
			total = total.Add(segs[i].Basis)
		}
		res, err := TopDown(amount, segs, 2)
		if total.IsZero() {
			if !errors.Is(err, ErrNoTopDownBasis) {
				rt.Fatalf("zero total basis: err = %v", err)
			}
			return
		}
		if err != nil {
			rt.Fatal(err)
		}
		sum := decimal.Zero
		tolerance := decimal.New(int64(n), -2)
		for _, sh := range res.Shares {
			sum = sum.Add(sh.Amount)
			exact := amount.Mul(sh.Basis).Div(total)
			if sh.Amount.Sub(exact).Abs().GreaterThan(tolerance) || sh.Amount.IsNegative() {
				rt.Fatalf("share %s of %s: %s, exact %s", sh.SegmentID, amount, sh.Amount, exact)
			}
		}
		if !sum.Equal(amount) {
			rt.Fatalf("Σ shares %s != amount %s", sum, amount)
		}
	})
}

func TestCheckSettlement(t *testing.T) {
	if err := CheckSettlement(dec("500"), dec("500")); err != nil {
		t.Fatalf("settling exactly the collected cost: %v", err)
	}
	if err := CheckSettlement(dec("500.01"), dec("500")); !errors.Is(err, ErrOverSettlement) {
		t.Fatalf("over-settlement: %v, want ErrOverSettlement", err)
	}
	if err := CheckSettlement(dec("0"), dec("500")); err == nil {
		t.Fatal("zero settlement accepted")
	}
}
