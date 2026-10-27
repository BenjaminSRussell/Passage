package domain

import (
	"errors"
	"testing"
)

func TestPlanAdjustment(t *testing.T) {
	cases := []struct {
		name             string
		in               AdjustmentInput
		newValue, delta  string
		oldUnit, newUnit string // "" when no unit cost is reported
		code             string
	}{
		{
			name:     "unit cost revalues the on-hand quantity",
			in:       AdjustmentInput{Mode: ModeUnitCost, Qty: dec("50"), Value: dec("600"), UnitCost: dec("11"), Scale: 2},
			newValue: "550", delta: "-50", oldUnit: "12", newUnit: "11",
		},
		{
			name:     "unit cost rounds half-even at the minor unit",
			in:       AdjustmentInput{Mode: ModeUnitCost, Qty: dec("3"), Value: dec("30"), UnitCost: dec("10.005"), Scale: 2},
			newValue: "30.02", delta: "0.02", oldUnit: "10", newUnit: "10.005",
		},
		{
			name:     "half-even keeps the even cent",
			in:       AdjustmentInput{Mode: ModeUnitCost, Qty: dec("1"), Value: dec("10"), UnitCost: dec("10.125"), Scale: 2},
			newValue: "10.12", delta: "0.12", oldUnit: "10", newUnit: "10.125",
		},
		{
			name:     "amount adds to the value and reports the new average",
			in:       AdjustmentInput{Mode: ModeAmount, Qty: dec("3"), Value: dec("10"), Amount: dec("-1"), Scale: 2},
			newValue: "9", delta: "-1", oldUnit: "3.333333", newUnit: "3",
		},
		{
			name:     "amount clears a residual value on zero stock",
			in:       AdjustmentInput{Mode: ModeAmount, Qty: dec("0"), Value: dec("0.03"), Amount: dec("-0.03"), Scale: 2},
			newValue: "0", delta: "-0.03",
		},
		{
			name: "unit cost needs positive stock",
			in:   AdjustmentInput{Mode: ModeUnitCost, Qty: dec("0"), Value: dec("5"), UnitCost: dec("1"), Scale: 2},
			code: CodeAdjustUnitCost,
		},
		{
			name: "unit cost on negative stock is refused",
			in:   AdjustmentInput{Mode: ModeUnitCost, Qty: dec("-2"), Value: dec("-20"), UnitCost: dec("9"), Scale: 2},
			code: CodeAdjustUnitCost,
		},
		{
			name: "the same unit cost changes nothing",
			in:   AdjustmentInput{Mode: ModeUnitCost, Qty: dec("10"), Value: dec("100"), UnitCost: dec("10"), Scale: 2},
			code: CodeAdjustNoChange,
		},
		{
			name: "a zero amount changes nothing",
			in:   AdjustmentInput{Mode: ModeAmount, Qty: dec("10"), Value: dec("100"), Amount: dec("0"), Scale: 2},
			code: CodeAdjustNoChange,
		},
		{
			name: "a write-down below zero on positive stock is refused",
			in:   AdjustmentInput{Mode: ModeAmount, Qty: dec("10"), Value: dec("100"), Amount: dec("-100.01"), Scale: 2},
			code: CodeAdjustNegative,
		},
		{
			name:     "a write-down to exactly zero is allowed",
			in:       AdjustmentInput{Mode: ModeAmount, Qty: dec("10"), Value: dec("100"), Amount: dec("-100"), Scale: 2},
			newValue: "0", delta: "-100", oldUnit: "10", newUnit: "0",
		},
		{
			name: "an unknown mode is refused",
			in:   AdjustmentInput{Mode: "PERCENT", Qty: dec("1"), Value: dec("1"), Scale: 2},
			code: CodeAdjustNoChange,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p, err := PlanAdjustment(tc.in)
			if tc.code != "" {
				var de *Error
				if !errors.As(err, &de) || de.Code != tc.code {
					t.Fatalf("err = %v, want %s", err, tc.code)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if !p.NewValue.Equal(dec(tc.newValue)) || !p.Delta.Equal(dec(tc.delta)) {
				t.Errorf("new value %s delta %s, want %s and %s", p.NewValue, p.Delta, tc.newValue, tc.delta)
			}
			if !p.NewValue.Sub(p.OldValue).Equal(p.Delta) || !p.Qty.Equal(tc.in.Qty) {
				t.Errorf("delta %s is not new - old (%s - %s), or the quantity moved", p.Delta, p.NewValue, p.OldValue)
			}
			if p.HasUnitCost != (tc.oldUnit != "") {
				t.Fatalf("HasUnitCost = %v, want %v", p.HasUnitCost, tc.oldUnit != "")
			}
			if p.HasUnitCost && (!p.OldUnitCost.Equal(dec(tc.oldUnit)) || !p.NewUnitCost.Equal(dec(tc.newUnit))) {
				t.Errorf("unit cost %s -> %s, want %s -> %s", p.OldUnitCost, p.NewUnitCost, tc.oldUnit, tc.newUnit)
			}
		})
	}
}
