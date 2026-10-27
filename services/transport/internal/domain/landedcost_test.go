package domain

import (
	"fmt"
	"testing"

	"github.com/shopspring/decimal"
	"pgregory.net/rapid"
)

// TestAllocateByWeight_AC634 is AC-34.6: freight $1,000.00 over three equal-weight
// lines gives 333.33 / 333.33 / 333.34 when line 3 has the highest value.
func TestAllocateByWeight_AC634(t *testing.T) {
	lines := []ReceiptLine{
		{Line: 1, Value: dec("100"), Weight: dec("1")},
		{Line: 2, Value: dec("100"), Weight: dec("1")},
		{Line: 3, Value: dec("250"), Weight: dec("1")},
	}
	got, err := AllocateByWeight(dec("1000.00"), lines)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"333.33", "333.33", "333.34"}
	sum := decimal.Zero
	for i, a := range got.Shares {
		if !a.Amount.Equal(dec(want[i])) {
			t.Fatalf("line %d = %s, want %s", a.Line, a.Amount, want[i])
		}
		sum = sum.Add(a.Amount)
	}
	if !sum.Equal(dec("1000.00")) || got.ResidualLine != 3 || !got.Residual.Equal(dec("0.01")) {
		t.Fatalf("sum=%s residual=%s on line %d", sum, got.Residual, got.ResidualLine)
	}
}

func TestAllocateByWeight_TieGoesToLowestLine(t *testing.T) {
	lines := []ReceiptLine{{Line: 7, Value: dec("5"), Weight: dec("1")}, {Line: 4, Value: dec("5"), Weight: dec("1")}, {Line: 9, Value: dec("5"), Weight: dec("1")}}
	got, err := AllocateByWeight(dec("1000.00"), lines)
	if err != nil {
		t.Fatal(err)
	}
	if got.ResidualLine != 4 {
		t.Fatalf("residual line = %d, want 4", got.ResidualLine)
	}
}

func TestAllocateByWeight_Rejects(t *testing.T) {
	tests := []struct {
		name  string
		cost  string
		lines []ReceiptLine
	}{
		{"no receipt lines", "10", nil},
		{"negative cost", "-1", []ReceiptLine{{Line: 1, Weight: dec("1")}}},
		{"zero total weight", "10", []ReceiptLine{{Line: 1, Weight: dec("0")}}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := AllocateByWeight(dec(tc.cost), tc.lines); err == nil {
				t.Fatal("expected a rejection")
			}
		})
	}
}

// TestAllocateByWeight_PropertySumsToCost checks that shares always sum to the
// cost to the cent (FR-34.9 invariant).
func TestAllocateByWeight_PropertySumsToCost(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		cents := rapid.Int64Range(0, 100_000_000).Draw(t, "costCents")
		n := rapid.IntRange(1, 20).Draw(t, "lines")
		lines := make([]ReceiptLine, n)
		for i := range lines {
			lines[i] = ReceiptLine{
				Line:   i + 1,
				Value:  decimal.NewFromInt(rapid.Int64Range(0, 1000).Draw(t, fmt.Sprintf("value%d", i))),
				Weight: decimal.NewFromInt(rapid.Int64Range(1, 1000).Draw(t, fmt.Sprintf("weight%d", i))),
			}
		}
		cost := decimal.New(cents, -2)
		got, err := AllocateByWeight(cost, lines)
		if err != nil {
			t.Fatal(err)
		}
		sum := decimal.Zero
		for _, a := range got.Shares {
			sum = sum.Add(a.Amount)
		}
		if !sum.Equal(cost) {
			t.Fatalf("sum %s != cost %s", sum, cost)
		}
	})
}

func TestAllocate_Methods(t *testing.T) {
	lines := []ReceiptLine{
		{Line: 1, Value: dec("600"), Weight: dec("1"), Volume: dec("3"), Quantity: dec("10")},
		{Line: 2, Value: dec("300"), Weight: dec("3"), Volume: dec("1"), Quantity: dec("10")},
		{Line: 3, Value: dec("100"), Weight: dec("0"), Volume: dec("0"), Quantity: dec("20")},
	}
	tests := []struct {
		method string
		want   []string
	}{
		{MethodWeight, []string{"25.00", "75.00", "0.00"}},
		{MethodVolume, []string{"75.00", "25.00", "0.00"}},
		{MethodValue, []string{"60.00", "30.00", "10.00"}},
		{MethodQuantity, []string{"25.00", "25.00", "50.00"}},
		{MethodEqual, []string{"33.34", "33.33", "33.33"}},
	}
	for _, tc := range tests {
		t.Run(tc.method, func(t *testing.T) {
			got, err := Allocate(dec("100.00"), tc.method, lines)
			if err != nil {
				t.Fatal(err)
			}
			for i, share := range got.Shares {
				if !share.Amount.Equal(dec(tc.want[i])) {
					t.Fatalf("line %d = %s, want %s", share.Line, share.Amount, tc.want[i])
				}
			}
			if got.ResidualLine != 1 {
				t.Fatalf("residual line = %d, want the highest-value line 1", got.ResidualLine)
			}
		})
	}
}

func TestAllocate_RejectsUnknownMethodAndZeroBasis(t *testing.T) {
	lines := []ReceiptLine{{Line: 1, Value: dec("10"), Weight: dec("1")}}
	if _, err := Allocate(dec("10"), "BY_MOOD", lines); err == nil || err.Code != "COM-0001" {
		t.Fatalf("unknown method error = %v", err)
	}
	if _, err := Allocate(dec("10"), MethodVolume, lines); err == nil {
		t.Fatal("zero total volume must be rejected")
	}
}
