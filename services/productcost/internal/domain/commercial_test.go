package domain

import (
	"fmt"
	"strings"
	"testing"

	"github.com/shopspring/decimal"
	"pgregory.net/rapid"
)

// mixCatalog: buy item BUY at 10.00 and a production item PROD whose only
// component is a purchased part at 12.50 (AC-39.11).
func mixCatalog() Catalog {
	return Catalog{
		"BUY":  {ID: "BUY", Kind: KindPurchased, Price: sptr(d("10"))},
		"RM":   {ID: "RM", Kind: KindPurchased, Price: sptr(d("12.50"))},
		"PROD": {ID: "PROD", Kind: KindManufactured, CostingLot: d("1"), BOM: []BOMLine{{ChildID: "RM", Qty: d("1")}}},
		"NOP":  {ID: "NOP", Kind: KindPurchased},
	}
}

// TestRollMixed_WeightedSplit covers AC-39.11: 60% vendor at 10.00 and 40%
// production at 12.50 mix to 11.00, and the split is weighted the same way.
func TestRollMixed_WeightedSplit(t *testing.T) {
	got, err := RollMixed(mixCatalog(), "FG", []AltRef{
		{Source: "VENDOR-A", Percent: d("60"), ItemID: "BUY"},
		{Source: "IN-HOUSE", Percent: d("40"), ItemID: "PROD"},
	}, RollOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != StatusPending || !got.Unit.Equal(d("11.00")) {
		t.Fatalf("status %s unit %s, want Pending 11.00", got.Status, got.Unit)
	}
	if !got.Split[CompDM].Equal(d("11.00")) || !got.Split.Total().Equal(got.Unit) {
		t.Errorf("split %v does not carry 11.00 in DM", got.Split)
	}
}

func TestRollMixed_ZeroRatioIsReportedNotRolled(t *testing.T) {
	got, err := RollMixed(mixCatalog(), "FG", []AltRef{
		{Source: "VENDOR-A", Percent: d("100"), ItemID: "BUY"},
		{Source: "NOT-USED", Percent: d("0"), ItemID: "NOP"}, // no price, but excluded
	}, RollOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != StatusPending || !got.Unit.Equal(d("10")) {
		t.Fatalf("unit %s status %s, want 10.00 Pending", got.Unit, got.Status)
	}
	if got.Alternatives[1].Status != AltStatusExcluded {
		t.Errorf("zero-ratio alternative status = %s", got.Alternatives[1].Status)
	}
}

func TestRollMixed_AlternativeWithoutCostFailsItem(t *testing.T) {
	got, err := RollMixed(mixCatalog(), "FG", []AltRef{
		{Source: "VENDOR-A", Percent: d("50"), ItemID: "BUY"},
		{Source: "NO-PRICE", Percent: d("50"), ItemID: "NOP"},
	}, RollOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != StatusError || len(got.Diagnostics) == 0 || got.Diagnostics[0].Code != CodeNoPrice {
		t.Fatalf("want Error with %s, got %s %+v", CodeNoPrice, got.Status, got.Diagnostics)
	}
	if got.Alternatives[1].Status != StatusError {
		t.Errorf("failing alternative status = %s", got.Alternatives[1].Status)
	}
}

func TestRollMixed_RatiosMustTotalOneHundred(t *testing.T) {
	_, err := RollMixed(mixCatalog(), "FG", []AltRef{
		{Source: "VENDOR-A", Percent: d("60"), ItemID: "BUY"},
		{Source: "IN-HOUSE", Percent: d("39.99"), ItemID: "PROD"},
	}, RollOptions{})
	if err == nil || !strings.Contains(err.Error(), CodeMixingRatioTotal) {
		t.Fatalf("err = %v, want %s", err, CodeMixingRatioTotal)
	}
}

func TestDeriveRatios_Table(t *testing.T) {
	cases := []struct {
		name string
		in   map[string]decimal.Decimal
		want map[string]string
	}{
		{"even thirds", map[string]decimal.Decimal{"A": d("1"), "B": d("1"), "C": d("1")},
			map[string]string{"A": "33.34", "B": "33.33", "C": "33.33"}},
		{"sixty forty", map[string]decimal.Decimal{"VENDOR-A": d("600"), "IN-HOUSE": d("400")},
			map[string]string{"VENDOR-A": "60.00", "IN-HOUSE": "40.00"}},
		{"zero receipts keep 0.00", map[string]decimal.Decimal{"A": d("5"), "B": d("0")},
			map[string]string{"A": "100.00", "B": "0.00"}},
	}
	for _, tc := range cases {
		got, err := DeriveRatios(tc.in)
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		for src, want := range tc.want {
			if !got[src].Equal(d(want)) {
				t.Errorf("%s: %s = %s, want %s", tc.name, src, got[src], want)
			}
		}
	}
	if _, err := DeriveRatios(map[string]decimal.Decimal{"A": d("0")}); err == nil {
		t.Error("no receipts accepted")
	}
}

// TestProperty_DeriveRatiosTotalOneHundred: ratios total exactly 100.00, are
// never negative, and stay within one hundredth of the exact share.
func TestProperty_DeriveRatiosTotalOneHundred(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		n := rapid.IntRange(1, 6).Draw(t, "n")
		qty := map[string]decimal.Decimal{}
		total := decimal.Zero
		for i := 0; i < n; i++ {
			q := decimal.New(rapid.Int64Range(0, 100000).Draw(t, "q"), -2)
			qty[fmt.Sprintf("S%d", i)] = q
			total = total.Add(q)
		}
		if !total.IsPositive() {
			return // no receipts: DeriveRatios is specified to error, covered by TestDeriveRatios_Table
		}
		got, err := DeriveRatios(qty)
		if err != nil {
			t.Fatal(err)
		}
		sum := decimal.Zero
		for src, r := range got {
			if r.IsNegative() {
				t.Fatalf("%s negative ratio %s", src, r)
			}
			exact := qty[src].Mul(decimal.NewFromInt(100)).Div(total)
			if r.Sub(exact).Abs().GreaterThan(d("0.01")) {
				t.Fatalf("%s ratio %s too far from exact %s", src, r, exact)
			}
			sum = sum.Add(r)
		}
		if !sum.Equal(d("100")) {
			t.Fatalf("ratios total %s, want 100.00", sum)
		}
	})
}

// TestCostOfSalesLines covers AC-39.12: 10 × FG-100 at 104.32 posts four COGS
// lines totalling 1,043.20 against Inventory.
func TestCostOfSalesLines(t *testing.T) {
	unit := Split{CompDM: d("21.20"), CompDL: d("54.00"), CompIndMat: d("2.12"), CompIndLab: d("27.00")}
	lines, total := CostOfSalesLines(d("10"), unit, MoneyScale)
	if !total.Equal(d("1043.20")) {
		t.Fatalf("total %s, want 1043.20", total)
	}
	want := map[string]string{"COGS-DM": "212.00", "COGS-DL": "540.00", "COGS-IND-MAT": "21.20", "COGS-IND-LAB": "270.00"}
	dr := decimal.Zero
	cr := decimal.Zero
	for _, ln := range lines {
		dr = dr.Add(ln.Debit)
		cr = cr.Add(ln.Credit)
		if w, ok := want[ln.Account]; ok && !ln.Debit.Equal(d(w)) {
			t.Errorf("%s debit %s, want %s", ln.Account, ln.Debit, w)
		}
		if ln.Account == AccountInventory && !ln.Credit.Equal(d("1043.20")) {
			t.Errorf("inventory credit %s, want 1043.20", ln.Credit)
		}
	}
	if !dr.Equal(cr) {
		t.Fatalf("unbalanced: debit %s credit %s", dr, cr)
	}
}

// TestPriceDifferenceLines covers AC-39.7: 10 at 5.20 against a 5.00 standard
// books PPV 2.00 in DM, offset to the clearing account.
func TestPriceDifferenceLines(t *testing.T) {
	lines, total := PriceDifferenceLines(d("10"), d("5.20"), Split{CompDM: d("5.00")}, MoneyScale)
	if !total.Equal(d("2.00")) {
		t.Fatalf("PPV %s, want 2.00", total)
	}
	if len(lines) != 2 || lines[0].Account != AccountPPVPrefix+CompDM || !lines[0].Debit.Equal(d("2.00")) {
		t.Fatalf("lines %+v", lines)
	}
	if lines[1].Account != AccountPriceDiffClear || !lines[1].Credit.Equal(d("2.00")) {
		t.Fatalf("clearing line %+v", lines[1])
	}
}

// TestProperty_PostingJournalsBalance: component journals balance to the cent
// for random standards, quantities and receipt prices, including favorable PPV.
func TestProperty_PostingJournalsBalance(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		split := Split{}
		for _, c := range []string{CompDM, CompDL, CompIndMat} {
			v := decimal.New(rapid.Int64Range(0, 500000).Draw(t, "std"), -4)
			split[c] = v
		}
		qty := decimal.NewFromInt(rapid.Int64Range(1, 1000).Draw(t, "qty"))
		receipt := decimal.New(rapid.Int64Range(0, 500000).Draw(t, "receipt"), -4)
		cogs, _ := CostOfSalesLines(qty, split, MoneyScale)
		ppv, _ := PriceDifferenceLines(qty, receipt, split, MoneyScale)
		for name, lines := range map[string][]JournalLine{"cogs": cogs, "ppv": ppv} {
			dr, cr := decimal.Zero, decimal.Zero
			for _, ln := range lines {
				dr = dr.Add(ln.Debit)
				cr = cr.Add(ln.Credit)
			}
			if !dr.Equal(cr) {
				t.Fatalf("%s unbalanced: debit %s credit %s", name, dr, cr)
			}
		}
	})
}

// TestSalesOrderEstimate covers AC-39.13: a line priced at 2,400.00 whose
// configuration costs 18.60 per unit at quantity 100 shows margin 540.00 (22.50%).
// Setup of 40.00 at rate 40 for the order lot of 100 adds 0.40 per unit.
func TestSalesOrderEstimate(t *testing.T) {
	cat := Catalog{
		"CFG": {ID: "CFG", Kind: KindManufactured, CostingLot: d("1"),
			BOM:        []BOMLine{{ChildID: "X", Qty: d("1")}},
			Operations: []Operation{{SetupHours: d("1"), Rate: d("40")}}},
		"X": {ID: "X", Kind: KindPurchased, Price: sptr(d("18.60"))},
	}
	est, err := SalesOrderEstimate(cat, "CFG", d("100"), d("2400"), MoneyScale, RollOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if est.Status != StatusPending || est.CalculationType != CalcTypeSalesOrder {
		t.Fatalf("status %s type %s", est.Status, est.CalculationType)
	}
	if !est.Cost.Equal(d("1900.00")) || !est.Margin.Equal(d("500.00")) || !est.MarginPct.Equal(d("20.83")) {
		t.Errorf("cost %s margin %s pct %s, want 1900.00 / 500.00 / 20.83", est.Cost, est.Margin, est.MarginPct)
	}
	// The configuration of AC-39.13 has no setup: it rolls at 18.60 per unit.
	plain := Catalog{
		"CFG": {ID: "CFG", Kind: KindManufactured, CostingLot: d("1"), BOM: []BOMLine{{ChildID: "X", Qty: d("1")}}},
		"X":   cat["X"],
	}
	est, err = SalesOrderEstimate(plain, "CFG", d("100"), d("2400"), MoneyScale, RollOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if !est.Unit.Equal(d("18.60")) || !est.Margin.Equal(d("540.00")) || !est.MarginPct.Equal(d("22.50")) {
		t.Errorf("unit %s margin %s pct %s, want 18.60 / 540.00 / 22.50", est.Unit, est.Margin, est.MarginPct)
	}
}

func TestSalesOrderEstimate_Rejects(t *testing.T) {
	cat := mixCatalog()
	if _, err := SalesOrderEstimate(cat, "PROD", d("0"), d("10"), MoneyScale, RollOptions{}); err == nil {
		t.Error("zero quantity accepted")
	}
	if _, err := SalesOrderEstimate(cat, "PROD", d("1"), d("0"), MoneyScale, RollOptions{}); err == nil {
		t.Error("zero net amount accepted")
	}
	if _, err := SalesOrderEstimate(cat, "MISSING", d("1"), d("1"), MoneyScale, RollOptions{}); err == nil {
		t.Error("unknown item accepted")
	}
}

func TestCompare_BaselineCandidateRows(t *testing.T) {
	base := MassRoll(Catalog{
		"A": {ID: "A", Kind: KindPurchased, Price: sptr(d("10"))},
		"B": {ID: "B", Kind: KindPurchased, Price: sptr(d("5"))},
	}, RollOptions{})
	cand := MassRoll(Catalog{
		"A": {ID: "A", Kind: KindPurchased, Price: sptr(d("11"))},
		"C": {ID: "C", Kind: KindPurchased, Price: sptr(d("3"))},
	}, RollOptions{})
	got := Compare(base, cand)
	if len(got.Rows) != 3 || got.Rows[0].ItemID != "A" {
		t.Fatalf("rows %+v", got.Rows)
	}
	a := got.Rows[0]
	if a.Status != RowCompared || !a.Delta.Equal(d("1")) || !a.DeltaPct.Equal(d("10.00")) || !a.ByComponent[CompDM].Equal(d("1")) {
		t.Errorf("A row %+v", a)
	}
	if got.Rows[1].Status != RowBaselineOnly || got.Rows[2].Status != RowCandidateOnly {
		t.Errorf("only-side rows %s %s", got.Rows[1].Status, got.Rows[2].Status)
	}
	if !got.TotalDelta.Equal(d("1")) {
		t.Errorf("total delta %s, want 1", got.TotalDelta)
	}
}

func TestReconcileStandard(t *testing.T) {
	got := ReconcileStandard(
		[]StandardOnHand{
			{SiteID: "S1", ItemID: "A", Qty: d("10"), StdUnit: d("10.50")},
			{SiteID: "S2", ItemID: "A", Qty: d("4"), StdUnit: d("2.50")},
		},
		[]SiteBalance{
			{SiteID: "S1", Amount: d("100")},
			{SiteID: "S2", Amount: d("10")},
			{SiteID: "S3", Amount: d("20")},
		}, MoneyScale)
	if len(got) != 3 {
		t.Fatalf("sites %+v", got)
	}
	s1 := got[0]
	if !s1.StandardValue.Equal(d("105.00")) || !s1.Difference.Equal(d("5.00")) || s1.Status != ReconDifference {
		t.Errorf("S1 %+v", s1)
	}
	if got[1].Status != ReconReconciled || !got[1].Difference.IsZero() {
		t.Errorf("S2 %+v", got[1])
	}
	if !got[2].Difference.Equal(d("-20")) || got[2].Lines != 0 {
		t.Errorf("S3 (GL only) %+v", got[2])
	}
}
