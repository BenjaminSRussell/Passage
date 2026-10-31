package domain

import (
	"testing"

	"github.com/shopspring/decimal"
	"pgregory.net/rapid"
)

// ac395Order is AC-39.5: 100 planned, 90 reported finished; 3 units per output
// of a component at 2.00 planned for 300 and 290 consumed; setup 40.00 per lot
// with costing lot 100 (standard 0.40 per unit); 5 units of an unplanned part at 1.50.
// The setup is actually charged at 40.00, which is 4.00 above the 36.00 that
// the RAF output absorbs at standard. That 4.00 is the labor rate variance the
// AC's "plus any labor PV" refers to.
func ac395Order() Order {
	return Order{
		PlannedQty: d("100"), RAFQty: d("90"),
		Lines: []OrderLine{
			{Component: CompDM, Planned: true, PlannedQty: d("300"), StdUnitCost: d("2.00"), ActualQty: d("290"), ActualUnitCost: d("2.00")},
			{Component: CompDL, Planned: true, Fixed: true, EstPerLot: d("40.00"), StdUnitCost: d("0.40"), ActualQty: d("1"), ActualUnitCost: d("40.00")},
			{Component: CompDM, Planned: false, ActualQty: d("5"), ActualUnitCost: d("1.50")},
		},
	}
}

func TestDecompose_AC395Components(t *testing.T) {
	got, err := Decompose(ac395Order())
	if err != nil {
		t.Fatal(err)
	}
	if !got.Types.Quantity.Equal(d("40.00")) {
		t.Errorf("QV = %s, want 40.00", got.Types.Quantity)
	}
	if !got.Types.Substitution.Equal(d("7.50")) {
		t.Errorf("SV = %s, want 7.50", got.Types.Substitution)
	}
	if !got.Types.LotSize.IsZero() {
		t.Errorf("LSV = %s, want 0.00", got.Types.LotSize)
	}
	if !got.Types.Price.Equal(d("4.00")) {
		t.Errorf("PV = %s, want 4.00 (setup rate)", got.Types.Price)
	}
	if !got.Total.Equal(d("51.50")) {
		t.Errorf("total = %s, want 51.50", got.Total)
	}
}

func TestDecompose_UnconsumedPlannedLineIsNegativeQuantity(t *testing.T) {
	o := Order{PlannedQty: d("10"), RAFQty: d("10"), Lines: []OrderLine{
		{Component: CompDM, Planned: true, PlannedQty: d("20"), StdUnitCost: d("1.00"), ActualQty: d("0")},
	}}
	got, err := Decompose(o)
	if err != nil {
		t.Fatal(err)
	}
	if !got.Types.Quantity.Equal(d("-20.00")) || !got.Total.Equal(d("-20.00")) {
		t.Errorf("QV = %s total = %s, want -20.00", got.Types.Quantity, got.Total)
	}
}

func TestDecompose_RejectsBadQuantities(t *testing.T) {
	if _, err := Decompose(Order{PlannedQty: d("0"), RAFQty: d("1")}); err == nil {
		t.Error("zero planned quantity accepted")
	}
	if _, err := Decompose(Order{PlannedQty: d("1"), RAFQty: d("-1")}); err == nil {
		t.Error("negative RAF accepted")
	}
}

// genOrder draws an order with random planned, fixed, and substitution lines.
func genOrder(t *rapid.T) Order {
	cents := func(label string, lo, hi int64) decimal.Decimal {
		return decimal.New(rapid.Int64Range(lo, hi).Draw(t, label), -2)
	}
	planned := rapid.Int64Range(1, 500).Draw(t, "plannedQty")
	raf := rapid.Int64Range(0, planned).Draw(t, "raf")
	o := Order{PlannedQty: decimal.NewFromInt(planned), RAFQty: decimal.NewFromInt(raf)}
	comps := []string{CompDM, CompDL, CompIndMat}
	n := rapid.IntRange(1, 8).Draw(t, "lines")
	for i := 0; i < n; i++ {
		comp := rapid.SampledFrom(comps).Draw(t, "comp")
		switch rapid.IntRange(0, 2).Draw(t, "kind") {
		case 0:
			o.Lines = append(o.Lines, OrderLine{Component: comp, Planned: true,
				PlannedQty: decimal.NewFromInt(rapid.Int64Range(1, 500).Draw(t, "pq")), StdUnitCost: cents("std", 1, 10000),
				ActualQty: decimal.NewFromInt(rapid.Int64Range(0, 600).Draw(t, "aq")), ActualUnitCost: cents("ac", 1, 10000)})
		case 1:
			o.Lines = append(o.Lines, OrderLine{Component: comp, Planned: true, Fixed: true,
				EstPerLot: cents("est", 1, 100000), StdUnitCost: cents("sul", 1, 1000), ActualQty: decimal.NewFromInt(1), ActualUnitCost: cents("acl", 1, 100000)})
		default:
			o.Lines = append(o.Lines, OrderLine{Component: comp, ActualQty: decimal.NewFromInt(rapid.Int64Range(0, 50).Draw(t, "sq")), ActualUnitCost: cents("sc", 1, 10000)})
		}
	}
	return o
}

// TestProperty_VarianceIdentityHolds checks LSV+QV+PV+SV = total for every
// component and in aggregate, to the cent, with residual inside the rounding bound.
func TestProperty_VarianceIdentityHolds(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		o := genOrder(t)
		got, err := Decompose(o)
		if err != nil {
			t.Fatalf("decompose: %v", err)
		}
		if !got.Total.Equal(got.Types.Sum()) {
			t.Fatalf("aggregate identity: total %s types %s", got.Total, got.Types.Sum())
		}
		comp := decimal.Zero
		for _, cv := range got.ByComponent {
			if !cv.Total.Equal(cv.Types.Sum()) {
				t.Fatalf("%s identity: total %s types %s", cv.Component, cv.Total, cv.Types.Sum())
			}
			comp = comp.Add(cv.Total)
		}
		if !comp.Equal(got.Total) {
			t.Fatalf("components %s do not add to total %s", comp, got.Total)
		}
		if got.Residual.Abs().GreaterThan(maxResidual(MoneyScale)) {
			t.Fatalf("residual %s above bound", got.Residual)
		}
	})
}

// TestProperty_LinesAreOrderIndependent: reordering lines changes no amount.
func TestProperty_LinesAreOrderIndependent(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		o := genOrder(t)
		a, err := Decompose(o)
		if err != nil {
			t.Fatal(err)
		}
		for i, j := 0, len(o.Lines)-1; i < j; i, j = i+1, j-1 {
			o.Lines[i], o.Lines[j] = o.Lines[j], o.Lines[i]
		}
		b, err := Decompose(o)
		if err != nil {
			t.Fatal(err)
		}
		if !a.Total.Equal(b.Total) || !a.Types.Quantity.Equal(b.Types.Quantity) || !a.Types.Price.Equal(b.Types.Price) {
			t.Fatalf("order changed amounts: %+v vs %+v", a.Types, b.Types)
		}
	})
}

// TestDecompose_HalfMinorUnitRoundsOnTheExactValue is fixture order PO-0036 of
// testdata/variance-200, worked by hand: 55 of 76 finished, setup estimate
// 2,264.42 per lot, standard 4.1998 per unit, 2 lots charged at 4,697.29.
// Absorbed estimate 2,264.42 × 55 ÷ 76 = 1,638.725; PV = 9,394.58 − 1,638.725 =
// 7,755.855 → 7,755.86; LSV = 1,638.725 − 230.989 = 1,407.736 → 1,407.74;
// total = 9,394.58 − 230.989 = 9,163.591 → 9,163.59, so the −0.01 residual
// goes to QV. Dividing 55 by 76 first rounded PV to 7,755.85.
func TestDecompose_HalfMinorUnitRoundsOnTheExactValue(t *testing.T) {
	o := Order{PlannedQty: d("76"), RAFQty: d("55"), Lines: []OrderLine{
		{Component: CompIndMat, Planned: true, Fixed: true, EstPerLot: d("2264.42"), StdUnitCost: d("4.1998"), ActualQty: d("2"), ActualUnitCost: d("4697.29")},
	}}
	got, err := Decompose(o)
	if err != nil {
		t.Fatal(err)
	}
	if !got.Types.Price.Equal(d("7755.86")) || !got.Types.LotSize.Equal(d("1407.74")) ||
		!got.Types.Quantity.Equal(d("-0.01")) || !got.Total.Equal(d("9163.59")) {
		t.Errorf("decomposition = %+v, want PV 7755.86 LSV 1407.74 QV -0.01 total 9163.59", got.Types)
	}
}

func TestDecompose_RoundsToTheCurrencyMinorUnit(t *testing.T) {
	// 10 consumed against 9.5 standard at 101.4 JPY: QV 50.7 rounds to 51 yen, and the identity holds in whole yen.
	o := Order{Currency: "JPY", PlannedQty: d("2"), RAFQty: d("2"), Lines: []OrderLine{
		{Component: CompDM, Planned: true, PlannedQty: d("9.5"), StdUnitCost: d("101.4"), ActualQty: d("10"), ActualUnitCost: d("101.4")},
	}}
	got, err := Decompose(o)
	if err != nil {
		t.Fatal(err)
	}
	if !got.Total.Equal(d("51")) || !got.Types.Quantity.Equal(d("51")) {
		t.Errorf("JPY decomposition = %+v", got)
	}
	if !got.Total.Equal(got.Types.Sum()) {
		t.Errorf("identity: total %s, types %s", got.Total, got.Types.Sum())
	}
}
