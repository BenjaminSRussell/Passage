package domain

import (
	"errors"
	"testing"

	"github.com/shopspring/decimal"
	"pgregory.net/rapid"
)

func children(basis ...string) []HierarchyChild {
	out := make([]HierarchyChild, len(basis))
	for i, b := range basis {
		out[i] = HierarchyChild{NodeID: string(rune('a' + i)), Basis: dec(b)}
	}
	return out
}

func TestSplitHierarchy_Cases(t *testing.T) {
	tests := []struct {
		name     string
		amount   string
		basis    string
		children []HierarchyChild
		shares   []string
		residual string
	}{
		{"actual output proportional, rounding cent to the first largest", "100.00", BasisActualOutput,
			children("1", "1", "1"), []string{"33.34", "33.33", "33.33"}, "0.00"},
		{"standard cost of output, worked example", "12000.00", BasisStandardCost,
			children("300000", "150000", "50000"), []string{"7200.00", "3600.00", "1200.00"}, "0.00"},
		{"a zero-output child receives nothing, the rest is fully distributed", "90.00", BasisActualOutput,
			children("0", "2", "1"), []string{"0.00", "60.00", "30.00"}, "0.00"},
		{"every output zero keeps the whole amount on the node", "50.00", BasisActualOutput,
			children("0", "0"), []string{"0.00", "0.00"}, "50.00"},
		{"fixed percent totalling 100 leaves no residual", "1000.00", BasisFixedPercent,
			children("33.3333", "33.3333", "33.3334"), []string{"333.33", "333.33", "333.34"}, "0.00"},
		{"fixed percent truncation residue goes to the largest share", "10.00", BasisFixedPercent,
			children("33.33", "33.33", "33.34"), []string{"3.33", "3.33", "3.34"}, "0.00"},
		{"fixed percent under 100 keeps the remainder on the node", "1000.00", BasisFixedPercent,
			children("20", "30"), []string{"200.00", "300.00"}, "500.00"},
		{"fixed percent under 100 keeps truncated cents too", "0.10", BasisFixedPercent,
			children("15", "15"), []string{"0.01", "0.01"}, "0.08"},
		{"zero-decimal currency", "1000", BasisActualOutput,
			children("1", "2"), []string{"333", "667"}, "0"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			scale := int32(2)
			if tc.name == "zero-decimal currency" {
				scale = 0
			}
			got, err := SplitHierarchy(dec(tc.amount), tc.basis, tc.children, scale)
			if err != nil {
				t.Fatal(err)
			}
			if len(got.Shares) != len(tc.shares) {
				t.Fatalf("shares %+v", got.Shares)
			}
			for i, want := range tc.shares {
				if got.Shares[i].NodeID != tc.children[i].NodeID || !got.Shares[i].Amount.Equal(dec(want)) {
					t.Errorf("share %d = %s %s, want %s", i, got.Shares[i].NodeID, got.Shares[i].Amount, want)
				}
			}
			if !got.Residual.Equal(dec(tc.residual)) {
				t.Errorf("residual %s, want %s", got.Residual, tc.residual)
			}
			if !got.Distributed.Add(got.Residual).Equal(dec(tc.amount)) {
				t.Errorf("distributed %s + residual %s != %s", got.Distributed, got.Residual, tc.amount)
			}
		})
	}
}

func TestSplitHierarchy_Rejects(t *testing.T) {
	tests := []struct {
		name     string
		amount   string
		basis    string
		children []HierarchyChild
		want     error
	}{
		{"percentages over 100", "10.00", BasisFixedPercent, children("60", "50"), ErrPercentOverflow},
		{"one percentage over 100", "10.00", BasisFixedPercent, children("101"), ErrInvalidBasisValue},
		{"negative basis", "10.00", BasisStandardCost, children("1", "-1"), nil},
		{"unknown basis", "10.00", "Magic", children("1"), nil},
		{"zero amount", "0", BasisActualOutput, children("1"), nil},
		{"negative amount", "-5.00", BasisActualOutput, children("1"), nil},
		{"amount finer than the minor unit", "1.005", BasisActualOutput, children("1"), nil},
		{"no children", "10.00", BasisActualOutput, nil, nil},
		{"duplicate child", "10.00", BasisActualOutput, []HierarchyChild{{NodeID: "a", Basis: dec("1")}, {NodeID: "a", Basis: dec("1")}}, nil},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := SplitHierarchy(dec(tc.amount), tc.basis, tc.children, 2)
			if err == nil {
				t.Fatal("expected an error")
			}
			if tc.want != nil && !errors.Is(err, tc.want) {
				t.Errorf("error %v, want %v", err, tc.want)
			}
		})
	}
}

// TestSplitHierarchy_Conserves checks that no distribution creates or loses a
// cent: Σ shares + residual = amount, every share is non-negative and at the
// minor unit, and proportional bases leave no residual unless all are zero.
func TestSplitHierarchy_Conserves(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		amount := decimal.New(rapid.Int64Range(1, 1_000_000_000).Draw(t, "cents"), -2)
		n := rapid.IntRange(1, 12).Draw(t, "n")
		basis := rapid.SampledFrom([]string{BasisActualOutput, BasisStandardCost, BasisFixedPercent}).Draw(t, "basis")
		kids := make([]HierarchyChild, n)
		left := int64(10000) // percent in hundredths
		anyPositive := false
		for i := range kids {
			var b decimal.Decimal
			if basis == BasisFixedPercent {
				v := rapid.Int64Range(0, left).Draw(t, "pct")
				left -= v
				b = decimal.New(v, -2)
			} else {
				b = decimal.New(rapid.Int64Range(0, 1_000_000).Draw(t, "qty"), -3)
			}
			anyPositive = anyPositive || b.IsPositive()
			kids[i] = HierarchyChild{NodeID: string(rune('a' + i)), Basis: b}
		}
		got, err := SplitHierarchy(amount, basis, kids, 2)
		if err != nil {
			t.Fatal(err)
		}
		sum := decimal.Zero
		for _, s := range got.Shares {
			if s.Amount.IsNegative() || !s.Amount.Equal(s.Amount.Round(2)) {
				t.Fatalf("share %s", s.Amount)
			}
			sum = sum.Add(s.Amount)
		}
		if !sum.Equal(got.Distributed) || !sum.Add(got.Residual).Equal(amount) || got.Residual.IsNegative() {
			t.Fatalf("sum %s distributed %s residual %s amount %s", sum, got.Distributed, got.Residual, amount)
		}
		if basis != BasisFixedPercent && anyPositive && !got.Residual.IsZero() {
			t.Fatalf("proportional basis left residual %s", got.Residual)
		}
		if basis == BasisFixedPercent && left == 0 && !got.Residual.IsZero() {
			t.Fatalf("percentages totalling 100 left residual %s", got.Residual)
		}
	})
}

func TestValidHierarchyTypeAndBasis(t *testing.T) {
	if !ValidHierarchyType(HierarchyCollective) || !ValidHierarchyType(HierarchyReporting) || ValidHierarchyType("collective") {
		t.Error("hierarchy types")
	}
	if !ValidDistributionBasis(BasisFixedPercent) || ValidDistributionBasis("") {
		t.Error("distribution bases")
	}
}
