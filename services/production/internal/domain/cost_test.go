package domain

import (
	"fmt"
	"testing"

	"github.com/shopspring/decimal"
	"pgregory.net/rapid"
)

// fixtureLines is the BR-21.7 worked example: FG-100, 100 units.
// Estimate: 300 kg RM @ 10.00; 50 h @ 30.00 labor; 50 h @ 10.00 overhead.
func fixtureEstimate(t *testing.T) []CostRow {
	t.Helper()
	ops := []Operation{{
		OperNumber: 10, ResourceID: "WC-1", SetupH: d("0"), RunHPer: d("0.5"), ScrapPct: d("0"),
		LaborRate: d("30"), OverheadRate: d("10"),
	}}
	ops, err := PlanOperations(ops, d("100"))
	if err != nil {
		t.Fatal(err)
	}
	lines := []OrderLine{{ItemID: "RM-1", QtyRequired: d("300"), UnitCost: d("10")}}
	return Estimate(lines, ops)
}

func TestEstimate_BR217Fixture(t *testing.T) {
	rows := fixtureEstimate(t)
	want := map[CostGroup]string{GroupMaterial: "3000", GroupLabor: "1500", GroupOverhead: "500"}
	got := map[CostGroup]decimal.Decimal{}
	for _, r := range rows {
		got[r.Group] = got[r.Group].Add(r.Amount)
	}
	for g, w := range want {
		if !got[g].Equal(d(w)) {
			t.Errorf("%s: want %s, got %s", g, w, got[g])
		}
	}
}

// AC6 / BR-21.7: the four variance rows total −49.00 and clear the WIP balance.
func TestSettle_BR217Fixture(t *testing.T) {
	planned := fixtureEstimate(t)
	actual := []CostRow{
		{Group: GroupMaterial, Key: "RM-1", Qty: d("305"), Amount: d("3111.00")},
		{Group: GroupLabor, Key: "WC-1@10", Qty: d("46"), Amount: d("1380.00")},
		{Group: GroupOverhead, Key: "WC-1@10", Qty: d("46"), Amount: d("460.00")},
	}
	s, err := Settle(d("100"), d("100"), planned, actual)
	if err != nil {
		t.Fatal(err)
	}
	if !s.WIPBefore.Equal(d("-49")) {
		t.Fatalf("WIP before settlement: want -49 (credit), got %s", s.WIPBefore)
	}
	want := map[string]string{
		"production-quantity/material": "50",
		"production-price/material":    "61",
		"production-quantity/labor":    "-120",
		"production-quantity/overhead": "-40",
	}
	if len(s.Variances) != len(want) {
		t.Fatalf("want %d variance rows, got %d: %+v", len(want), len(s.Variances), s.Variances)
	}
	sum := decimal.Zero
	for _, v := range s.Variances {
		key := fmt.Sprintf("%s/%s", v.Type, v.Group)
		w, ok := want[key]
		if !ok || !v.Amount.Equal(d(w)) {
			t.Errorf("%s: want %s, got %s", key, w, v.Amount)
		}
		sum = sum.Add(v.Amount)
	}
	if !sum.Equal(d("-49")) {
		t.Fatalf("variances total: want -49.00, got %s", sum)
	}
}

// AC4 / AC-21.7 shape: a 60-of-100 RAF leaves WIP equal to consumption − 60 × standard.
func TestSettle_PartialRAFLeavesWIPConsistent(t *testing.T) {
	planned := fixtureEstimate(t)
	actual := []CostRow{
		{Group: GroupMaterial, Key: "RM-1", Qty: d("180"), Amount: d("1800")},
		{Group: GroupLabor, Key: "WC-1@10", Qty: d("30"), Amount: d("900")},
		{Group: GroupOverhead, Key: "WC-1@10", Qty: d("30"), Amount: d("300")},
	}
	s, err := Settle(d("100"), d("60"), planned, actual)
	if err != nil {
		t.Fatal(err)
	}
	// Consumption 3000 − RAF value 60 × 50 = 3000 → WIP before = 3000 − 3000 = 0 here.
	if !s.WIPBefore.Equal(d("0")) {
		t.Fatalf("want WIP 0, got %s", s.WIPBefore)
	}
}

func TestSettle_RejectsBadQuantities(t *testing.T) {
	if _, err := Settle(d("0"), d("0"), nil, nil); err == nil {
		t.Fatal("zero order quantity accepted")
	}
	if _, err := Settle(d("10"), d("-1"), nil, nil); err == nil {
		t.Fatal("negative RAF accepted")
	}
}

// BR-21.6: the four variance types sum to actual − RAF × planned, to the cent.
func TestSettle_VariancesSumToIdentity(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		money := func(label string, max int64) decimal.Decimal {
			return decimal.New(rapid.Int64Range(0, max).Draw(t, label), -2)
		}
		n := rapid.IntRange(1, 6).Draw(t, "rows")
		var planned, actual []CostRow
		for i := 0; i < n; i++ {
			g := rapid.SampledFrom([]CostGroup{GroupMaterial, GroupLabor, GroupOverhead}).Draw(t, "group")
			key := fmt.Sprintf("k%d", i)
			p := CostRow{Group: g, Key: key, Qty: money("pq", 100000), Amount: money("pa", 1000000)}
			p.Fixed = p.Amount.Mul(decimal.New(rapid.Int64Range(0, 100).Draw(t, "fixed"), -2))
			planned = append(planned, p)
			if rapid.Bool().Draw(t, "consumed") {
				actual = append(actual, CostRow{Group: g, Key: key, Qty: money("aq", 100000), Amount: money("aa", 1000000)})
			}
		}
		if rapid.Bool().Draw(t, "extra") {
			actual = append(actual, CostRow{Group: GroupMaterial, Key: "extra", Qty: money("eq", 1000), Amount: money("ea", 100000)})
		}
		orderQty := decimal.New(rapid.Int64Range(1, 100000).Draw(t, "order"), -2)
		raf := decimal.New(rapid.Int64Range(0, 100000).Draw(t, "raf"), -2)

		s, err := Settle(orderQty, raf, planned, actual)
		if err != nil {
			t.Fatal(err)
		}
		sum := decimal.Zero
		for _, v := range s.Variances {
			sum = sum.Add(v.Amount)
		}
		if !sum.Equal(s.WIPBefore) {
			t.Fatalf("variances %s do not clear WIP %s", sum, s.WIPBefore)
		}
		actualTotal, plannedTotal := decimal.Zero, decimal.Zero
		for _, a := range actual {
			actualTotal = actualTotal.Add(a.Amount)
		}
		for _, p := range planned {
			plannedTotal = plannedTotal.Add(p.Amount)
		}
		exact := actualTotal.Sub(raf.Div(orderQty).Mul(plannedTotal))
		if sum.Sub(exact).Abs().GreaterThan(d("0.01")) {
			t.Fatalf("identity off by more than a cent: sum %s, exact %s", sum, exact)
		}
	})
}

// An item on two lines (for example under two phantoms) gets one material row,
// so the estimate has one cost cell per item and settlement matches it once.
func TestEstimate_MergesAnItemOnSeveralLines(t *testing.T) {
	lines := []OrderLine{
		{LineNo: "10.1", ItemID: "bolt", QtyRequired: d("40"), UnitCost: d("0.125")},
		{LineNo: "20", ItemID: "frame", QtyRequired: d("10"), UnitCost: d("12")},
		{LineNo: "30.1", ItemID: "bolt", QtyRequired: d("24"), UnitCost: d("0.125")},
	}
	rows := Estimate(lines, nil)
	if len(rows) != 2 {
		t.Fatalf("want 2 material rows, got %+v", rows)
	}
	if rows[0].Key != "bolt" || !rows[0].Qty.Equal(d("64")) || !rows[0].Amount.Equal(d("8")) {
		t.Errorf("bolt row: %+v", rows[0])
	}
	if rows[1].Key != "frame" || !rows[1].Amount.Equal(d("120")) {
		t.Errorf("frame row: %+v", rows[1])
	}
}

func TestEstimate_SetupIsFixedAndSubcontractIsPricePerUnit(t *testing.T) {
	ops := []Operation{
		{OperNumber: 10, ResourceID: "WC-1", SetupH: d("2"), RunHPer: d("0.1"), LaborRate: d("30"), OverheadRate: d("5"), ProcessQty: d("100")},
		{OperNumber: 20, ResourceID: "VEND-1", Subcontract: true, SubcontractPrice: d("1.255"), ProcessQty: d("100")},
	}
	rows := Estimate(nil, ops)
	want := []CostRow{
		{Group: GroupLabor, Key: "WC-1@10", Qty: d("12"), Amount: d("360"), Fixed: d("60")},
		{Group: GroupOverhead, Key: "WC-1@10", Qty: d("12"), Amount: d("60"), Fixed: d("10")},
		{Group: GroupSubcontract, Key: "VEND-1@20", Qty: d("100"), Amount: d("125.5"), Fixed: d("0")},
	}
	if len(rows) != len(want) {
		t.Fatalf("want %d rows, got %+v", len(want), rows)
	}
	for i, w := range want {
		r := rows[i]
		if r.Group != w.Group || r.Key != w.Key || !r.Qty.Equal(w.Qty) || !r.Amount.Equal(w.Amount) || !r.Fixed.Equal(w.Fixed) {
			t.Errorf("row %d: want %+v, got %+v", i, w, r)
		}
	}
}

// Amounts are kept at 4 decimals internally (FR-21.3).
func TestEstimate_RoundsAmountsToFourDecimals(t *testing.T) {
	rows := Estimate([]OrderLine{{ItemID: "x", QtyRequired: d("0.333334"), UnitCost: d("1.234567")}}, nil)
	if !rows[0].Amount.Equal(d("0.4115")) {
		t.Fatalf("want 0.4115, got %s", rows[0].Amount)
	}
}

// AC-21.7 shape with setup: a 60-of-100 order carries 40% of the fixed setup as lot-size variance.
func TestSettle_LotSizeVarianceOnPartialOutput(t *testing.T) {
	planned := []CostRow{{Group: GroupLabor, Key: "WC-1@10", Qty: d("50"), Amount: d("1500"), Fixed: d("300")}}
	// Actual: full setup (10 h) plus 60% of the 40 run hours = 34 h at the standard rate.
	actual := []CostRow{{Group: GroupLabor, Key: "WC-1@10", Qty: d("34"), Amount: d("1020")}}
	s, err := Settle(d("100"), d("60"), planned, actual)
	if err != nil {
		t.Fatal(err)
	}
	if !s.WIPBefore.Equal(d("120")) || len(s.Variances) != 1 {
		t.Fatalf("want WIP 120 cleared by one lot-size row, got %+v", s)
	}
	if v := s.Variances[0]; v.Type != VarLotSize || !v.Amount.Equal(d("120")) {
		t.Fatalf("want lot-size 120, got %+v", v)
	}
}

// Journal lines for one cell are summed before matching, so a split issue
// settles like a single one.
func TestSettle_MergesRowsOfOneCell(t *testing.T) {
	planned := fixtureEstimate(t)
	split := []CostRow{
		{Group: GroupMaterial, Key: "RM-1", Qty: d("200"), Amount: d("2040.00")},
		{Group: GroupMaterial, Key: "RM-1", Qty: d("105"), Amount: d("1071.00")},
		{Group: GroupLabor, Key: "WC-1@10", Qty: d("46"), Amount: d("1380.00")},
		{Group: GroupOverhead, Key: "WC-1@10", Qty: d("46"), Amount: d("460.00")},
	}
	s, err := Settle(d("100"), d("100"), planned, split)
	if err != nil {
		t.Fatal(err)
	}
	total := decimal.Zero
	for _, v := range s.Variances {
		total = total.Add(v.Amount)
		if v.Type == VarSubstitution {
			t.Errorf("split rows produced a substitution variance: %+v", v)
		}
	}
	if !total.Equal(d("-49")) {
		t.Fatalf("want -49, got %s", total)
	}
}

// BR-21.6 substitution: an unplanned item consumed and a planned item not consumed.
func TestSettle_SubstitutionVariance(t *testing.T) {
	planned := []CostRow{{Group: GroupMaterial, Key: "RM-1", Qty: d("10"), Amount: d("100")}}
	actual := []CostRow{{Group: GroupMaterial, Key: "RM-2", Qty: d("10"), Amount: d("110")}}
	s, err := Settle(d("10"), d("10"), planned, actual)
	if err != nil {
		t.Fatal(err)
	}
	if len(s.Variances) != 1 || s.Variances[0].Type != VarSubstitution || !s.Variances[0].Amount.Equal(d("10")) {
		t.Fatalf("want one substitution variance of 10, got %+v", s.Variances)
	}
}
