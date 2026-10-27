package domain

import (
	"testing"

	"github.com/shopspring/decimal"
	"pgregory.net/rapid"
)

func bal(p, s, vt, qty, physical, financial string) Balance {
	return Balance{ProductID: p, SiteID: s, ValuationType: vt, Currency: "EUR", Qty: dec(qty),
		PhysicalValue: dec(physical), FinancialValue: dec(financial)}
}

// fixtureBalances: product p1 at site s1 is the AC-20.12 split valuation
// (DOMESTIC 1,000.00 and IMPORT 700.00 after issuing 50 IMPORT); p2 at s1 has
// 10 units received but not invoiced; p3 at s2 is in warehouse w1.
func fixtureBalances() []Balance {
	p3 := bal("p3", "s2", "", "5", "0", "25")
	p3.WarehouseID = "w1"
	return []Balance{
		bal("p1", "s1", "DOMESTIC", "100", "0", "1000"),
		bal("p1", "s1", "IMPORT", "50", "0", "700"),
		bal("p2", "s1", "", "10", "50", "0"),
		p3,
	}
}

func rowFor(t *testing.T, rep ValueReport, key map[string]string) ReportRow {
	t.Helper()
	for _, r := range rep.Rows {
		match := len(r.Key) == len(key)
		for k, v := range key {
			if r.Key[k] != v {
				match = false
			}
		}
		if match {
			return r
		}
	}
	t.Fatalf("no row for %v in %+v", key, rep.Rows)
	return ReportRow{}
}

func TestBuildValueReport_GroupsAndTotals(t *testing.T) {
	tests := []struct {
		name                       string
		groupBy                    []string
		rows                       int
		key                        map[string]string
		qty, physical, value       string
		wantTotalQty, wantTotalVal string
	}{
		{name: "no grouping is one total row", groupBy: nil, rows: 1, key: map[string]string{}, qty: "165", physical: "50", value: "1775"},
		{name: "by site", groupBy: []string{"site"}, rows: 2, key: map[string]string{"site": "s1"}, qty: "160", physical: "50", value: "1750"},
		{name: "AC-20.12 by valuation type", groupBy: []string{"product", "valuationType"}, rows: 4,
			key: map[string]string{"product": "p1", "valuationType": "IMPORT"}, qty: "50", physical: "0", value: "700"},
		{name: "by warehouse", groupBy: []string{"warehouse"}, rows: 2, key: map[string]string{"warehouse": "w1"}, qty: "5", physical: "0", value: "25"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rep, err := BuildValueReport(fixtureBalances(), tc.groupBy)
			if err != nil {
				t.Fatalf("BuildValueReport: %v", err)
			}
			if len(rep.Rows) != tc.rows {
				t.Fatalf("rows = %d, want %d: %+v", len(rep.Rows), tc.rows, rep.Rows)
			}
			if rep.Currency != "EUR" || rep.Total.Value().String() != "1775" || rep.Total.Qty.String() != "165" || rep.Total.PhysicalValue.String() != "50" {
				t.Fatalf("totals = %s %+v, want EUR qty 165 physical 50 value 1775", rep.Currency, rep.Total)
			}
			r := rowFor(t, rep, tc.key)
			if r.Qty.String() != tc.qty || r.PhysicalValue.String() != tc.physical || r.Value().String() != tc.value {
				t.Fatalf("row %v = qty %s physical %s value %s, want %s / %s / %s", tc.key, r.Qty, r.PhysicalValue, r.Value(), tc.qty, tc.physical, tc.value)
			}
		})
	}
}

func TestBuildValueReport_Refusals(t *testing.T) {
	tests := []struct {
		name    string
		bal     []Balance
		groupBy []string
		code    string
	}{
		{"empty report is one empty list", nil, []string{"site"}, ""},
		{"all dimensions", fixtureBalances(), []string{"product", "site", "warehouse", "valuationType"}, ""},
		{"unknown dimension", fixtureBalances(), []string{"colour"}, CodeReportGroupBy},
		{"valuation ledger is a selector, not a grouping", fixtureBalances(), []string{"valuationLedger"}, CodeReportGroupBy},
		{"repeated dimension", fixtureBalances(), []string{"site", "site"}, CodeReportGroupBy},
		{"two currencies cannot be added", append(fixtureBalances(), Balance{ProductID: "p9", SiteID: "s1", Currency: "USD",
			Qty: dec("1"), PhysicalValue: decimal.Zero, FinancialValue: dec("1")}), []string{"site"}, CodeReportCurrency},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rep, err := BuildValueReport(tc.bal, tc.groupBy)
			if got := domainCode(err); got != tc.code {
				t.Fatalf("code = %q, want %q (err %v)", got, tc.code, err)
			}
			if tc.code == "" && rep.Rows == nil {
				t.Fatal("rows must be an empty list, not nil")
			}
		})
	}
}

func TestCostKinds(t *testing.T) {
	for _, tc := range []struct {
		kind              string
		valid, qty, stock bool
	}{
		{KindPhysical, true, true, true},
		{KindFinancial, true, true, true},
		{KindPPV, true, true, false}, // posted to the PPV account, never to stock (AC-20.4)
		{KindInvoiceDiff, true, false, true},
		{KindSettlementAdj, true, false, true},
		{KindRevaluation, true, false, true},
		{KindManualAdj, true, false, true},
		{KindRecalc, true, false, true},
		{"RECEIPT", false, false, false},
	} {
		if ValidCostKind(tc.kind) != tc.valid || (tc.valid && CarriesQuantity(tc.kind) != tc.qty) || CountsInInventory(tc.kind) != tc.stock {
			t.Errorf("%s: valid %v qty %v stock %v", tc.kind, ValidCostKind(tc.kind), CarriesQuantity(tc.kind), CountsInInventory(tc.kind))
		}
	}
}

func TestTieOutReport(t *testing.T) {
	rep, err := BuildValueReport(fixtureBalances(), []string{"site"})
	if err != nil {
		t.Fatal(err)
	}
	site := func(s, amount string) GLBalance {
		return GLBalance{Dims: map[string]string{"site": s}, Balance: dec(amount)}
	}
	tests := []struct {
		name        string
		gl          []GLBalance
		differences int
		totalDiff   string
		code        string
	}{
		{name: "ties to the ledger (physical value included)", gl: []GLBalance{site("s1", "1750"), site("s2", "25")}, totalDiff: "0"},
		{name: "difference in one group", gl: []GLBalance{site("s1", "1700"), site("s2", "25")}, differences: 1, totalDiff: "50"},
		{name: "group missing from the ledger compares with zero", gl: []GLBalance{site("s1", "1750")}, differences: 1, totalDiff: "25"},
		{name: "ledger-only group shows as a difference", gl: []GLBalance{site("s1", "1750"), site("s2", "25"), site("s3", "10")},
			differences: 1, totalDiff: "-10"},
		{name: "ledger keyed by another dimension", gl: []GLBalance{{Dims: map[string]string{"product": "p1"}, Balance: dec("1")}}, code: CodeTieOutDimension},
		{name: "ledger missing the grouping dimension", gl: []GLBalance{{Dims: map[string]string{}, Balance: dec("1")}}, code: CodeTieOutDimension},
		{name: "ledger with an extra dimension", gl: []GLBalance{{Dims: map[string]string{"site": "s1", "product": "p1"}, Balance: dec("1")}}, code: CodeTieOutDimension},
		{name: "duplicate ledger key", gl: []GLBalance{site("s1", "1"), site("s1", "2")}, code: CodeTieOutDuplicate},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			out, err := TieOutReport(rep, tc.gl)
			if got := domainCode(err); got != tc.code {
				t.Fatalf("code = %q, want %q (err %v)", got, tc.code, err)
			}
			if tc.code != "" {
				return
			}
			if out.Differences != tc.differences || out.TotalDiff.String() != tc.totalDiff || out.Ties() != (tc.differences == 0) {
				t.Fatalf("differences %d total %s ties %v, want %d %s", out.Differences, out.TotalDiff, out.Ties(), tc.differences, tc.totalDiff)
			}
		})
	}
}

func drawBalances(t *rapid.T) []Balance {
	n := rapid.IntRange(0, 25).Draw(t, "n")
	out := make([]Balance, 0, n)
	for i := 0; i < n; i++ {
		out = append(out, Balance{
			ProductID:      rapid.SampledFrom([]string{"p1", "p2", "p3"}).Draw(t, "p"),
			SiteID:         rapid.SampledFrom([]string{"s1", "s2"}).Draw(t, "s"),
			WarehouseID:    rapid.SampledFrom([]string{"", "w1"}).Draw(t, "w"),
			ValuationType:  rapid.SampledFrom([]string{"", "A", "B"}).Draw(t, "vt"),
			Currency:       "EUR",
			Qty:            decimal.New(int64(rapid.IntRange(-50_000, 500_000).Draw(t, "q")), -3),
			PhysicalValue:  decimal.New(int64(rapid.IntRange(-10_000, 10_000).Draw(t, "pv")), -2),
			FinancialValue: decimal.New(int64(rapid.IntRange(-1_000_000, 1_000_000).Draw(t, "fv")), -4),
		})
	}
	return out
}

// Property: grouping never creates or loses quantity or value, and neither the
// rows nor the totals depend on the order of the input.
func TestBuildValueReport_ConservesValue(t *testing.T) {
	dims := []string{"product", "site", "warehouse", "valuationType"}
	rapid.Check(t, func(t *rapid.T) {
		bals := drawBalances(t)
		total := zeroAmounts()
		for _, b := range bals {
			total.add(b)
		}
		groupBy := dims[:rapid.IntRange(0, len(dims)).Draw(t, "k")]
		rep, err := BuildValueReport(bals, groupBy)
		if err != nil {
			t.Fatalf("BuildValueReport: %v", err)
		}
		if !rep.Total.Value().Equal(total.Value()) || !rep.Total.Qty.Equal(total.Qty) || !rep.Total.PhysicalValue.Equal(total.PhysicalValue) {
			t.Fatalf("totals %+v differ from inputs %+v", rep.Total, total)
		}
		rowSum := zeroAmounts()
		for _, r := range rep.Rows {
			rowSum.Qty, rowSum.PhysicalValue, rowSum.FinancialValue = rowSum.Qty.Add(r.Qty), rowSum.PhysicalValue.Add(r.PhysicalValue), rowSum.FinancialValue.Add(r.FinancialValue)
		}
		if !rowSum.Value().Equal(total.Value()) || !rowSum.Qty.Equal(total.Qty) {
			t.Fatalf("rows sum to %+v, inputs to %+v", rowSum, total)
		}
		shuffled := rapid.Permutation(bals).Draw(t, "perm")
		rep2, _ := BuildValueReport(shuffled, groupBy)
		if len(rep2.Rows) != len(rep.Rows) {
			t.Fatalf("shuffled input gives %d rows, want %d", len(rep2.Rows), len(rep.Rows))
		}
		for i := range rep.Rows {
			if groupKey(groupBy, rep.Rows[i].Key) != groupKey(groupBy, rep2.Rows[i].Key) || !rep.Rows[i].Value().Equal(rep2.Rows[i].Value()) {
				t.Fatalf("row %d differs with shuffled input", i)
			}
		}
	})
}

// Property: a ledger equal to the inventory in every group ties, and the total
// difference is always total inventory minus total ledger.
func TestTieOutReport_TotalDifference(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		rep, err := BuildValueReport(drawBalances(t), []string{"site"})
		if err != nil {
			t.Fatal(err)
		}
		var gl []GLBalance
		ledgerTotal, off := decimal.Zero, 0
		for _, r := range rep.Rows {
			amt := r.Value()
			if rapid.Bool().Draw(t, "off") {
				amt = amt.Add(decimal.New(1, -2))
				off++
			}
			ledgerTotal = ledgerTotal.Add(amt)
			gl = append(gl, GLBalance{Dims: r.Key, Balance: amt})
		}
		out, err := TieOutReport(rep, gl)
		if err != nil {
			t.Fatal(err)
		}
		if want := rep.Total.Value().Sub(ledgerTotal); !out.TotalDiff.Equal(want) {
			t.Fatalf("total diff %s, want %s", out.TotalDiff, want)
		}
		if out.Differences != off || out.Ties() != (off == 0) {
			t.Fatalf("%d differences (ties %v), want %d", out.Differences, out.Ties(), off)
		}
	})
}
