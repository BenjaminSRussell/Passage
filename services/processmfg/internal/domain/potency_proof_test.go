package domain

import (
	"testing"

	"github.com/shopspring/decimal"
	"pgregory.net/rapid"
)

// TestConsumePotency_AdversarialFixture runs pmf-potency-adv-40 (Wave 24 proof:
// consumption scales correctly with active-ingredient percentage). The expected
// values come from testdata/gen_pmf_potency_adv.py, an exact-rational reference
// that shares no code with the engine: repeating fractions, the half-up
// boundary at the 7th decimal, the filler landing on zero and one micro-unit
// below it, potencies above 100 % and numeric(18,6)-scale quantities.
func TestConsumePotency_AdversarialFixture(t *testing.T) {
	rows := readFixture(t, "pmf-potency-adv-40.csv", 8)
	if len(rows) != 40 {
		t.Fatalf("pmf-potency-adv-40 has %d rows", len(rows))
	}
	for _, r := range rows {
		res, err := ConsumePotency(dec(r[0]), dec(r[1]), dec(r[2]), dec(r[3]))
		if want := r[6]; want != "" {
			de, ok := err.(*Error)
			if !ok || de.Code != want {
				t.Errorf("%s: got %+v err %v, want %s", r[7], res, err, want)
			}
			continue
		}
		if err != nil {
			t.Errorf("%s: %v", r[7], err)
			continue
		}
		if !res.Qty.Equal(dec(r[4])) || !res.CompensatingQty.Equal(dec(r[5])) {
			t.Errorf("%s: got qty %s comp %s, want %s and %s", r[7], res.Qty, res.CompensatingQty, r[4], r[5])
		}
	}
}

// micro draws a non-negative quantity with six decimals.
func micro(t *rapid.T, label string, max int64) decimal.Decimal {
	return decimal.New(rapid.Int64Range(0, max).Draw(t, label), -QtyPrecision)
}

// TestConsumePotency_ScalesWithActiveIngredient checks BR-24.2 as properties
// rather than by recomputing the formula:
//   - the active ingredient delivered, qty × batch potency, equals planned ×
//     standard potency to within half a micro-unit of qty;
//   - the batch size is kept: qty + filler = planned + planned filler exactly;
//   - a stronger batch never needs more and never fails where a weaker one
//     succeeded;
//   - PMF-0002 is answered only when the exact extra quantity really exceeds the
//     filler (beyond the half micro-unit rounding allows).
func TestConsumePotency_ScalesWithActiveIngredient(t *testing.T) {
	half := decimal.New(5, -(QtyPrecision + 1))
	rapid.Check(t, func(t *rapid.T) {
		planned := micro(t, "planned", 1_000_000_000_000)
		comp := micro(t, "comp", 1_000_000_000_000)
		ps := decimal.New(rapid.Int64Range(1, 1_200_000).Draw(t, "ps"), -4)
		pa := decimal.New(rapid.Int64Range(1, 1_200_000).Draw(t, "pa"), -4)
		stronger := pa.Add(decimal.New(rapid.Int64Range(1, 100_000).Draw(t, "delta"), -4))

		res, err := ConsumePotency(planned, ps, pa, comp)
		if err != nil {
			de, ok := err.(*Error)
			if !ok || de.Code != CodeCompNegative {
				t.Fatalf("unexpected error %v", err)
			}
			// exact extra = planned × ps / pa − planned, compared without rounding:
			// extra > comp − half  ⇔  planned × ps − planned × pa > (comp − half) × pa.
			lhs := planned.Mul(ps).Sub(planned.Mul(pa))
			if !lhs.GreaterThan(comp.Sub(half).Mul(pa)) {
				t.Fatalf("PMF-0002 for planned %s ps %s pa %s comp %s, but the filler covers the extra", planned, ps, pa, comp)
			}
			return
		}
		if res.Qty.IsNegative() || res.CompensatingQty.IsNegative() {
			t.Fatalf("negative result %+v", res)
		}
		if gap := res.Qty.Mul(pa).Sub(planned.Mul(ps)).Abs(); gap.GreaterThan(half.Mul(pa)) {
			t.Fatalf("active delivered %s vs required %s (gap %s > %s)", res.Qty.Mul(pa), planned.Mul(ps), gap, half.Mul(pa))
		}
		if !res.Qty.Add(res.CompensatingQty).Equal(planned.Add(comp)) {
			t.Fatalf("batch size changed: %s + %s != %s + %s", res.Qty, res.CompensatingQty, planned, comp)
		}
		more, err := ConsumePotency(planned, ps, stronger, comp)
		if err != nil {
			t.Fatalf("stronger batch %s failed where %s succeeded: %v", stronger, pa, err)
		}
		if more.Qty.GreaterThan(res.Qty) {
			t.Fatalf("stronger batch %s needs %s, more than %s at %s", stronger, more.Qty, res.Qty, pa)
		}
	})
}
