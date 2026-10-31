package domain

import (
	"errors"
	"math/big"
	"testing"

	"github.com/shopspring/decimal"
	"pgregory.net/rapid"
)

func codeOf(err error) string {
	var de *Error
	if errors.As(err, &de) {
		return de.Code
	}
	return ""
}

func TestProposeQty(t *testing.T) {
	tests := []struct {
		name                                 string
		mode                                 Proposal
		required, consumed, reported, ordQty string
		want                                 string
	}{
		{"remaining takes what is left", ProposeRemaining, "315", "100", "0", "100", "215"},
		{"remaining never goes negative", ProposeRemaining, "315", "320", "0", "100", "0"},
		{"proportional to the reported share", ProposeProportional, "33.9966", "0", "40", "100", "13.59864"},
		{"proportional rounds up at 6 places", ProposeProportional, "10", "0", "1", "3", "3.333334"},
		{"proportional less what was consumed", ProposeProportional, "33.9966", "13.59864", "100", "100", "20.39796"},
		{"none proposes nothing", ProposeNone, "315", "0", "100", "100", "0"},
		// Regression: 306 × 100 ÷ 150 is 204 exactly; a rounded share 0.6666666666666667 gave 204.000001.
		{"RAF 100 of 150 consumes 2 × 100 × 1.02 exactly", ProposeProportional, "306", "0", "100", "150", "204"},
		{"RAF 2 of 3 consumes two thirds of 30 exactly", ProposeProportional, "30", "0", "2", "3", "20"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := ProposeQty(tc.mode, d(tc.required), d(tc.consumed), d(tc.reported), d(tc.ordQty)); !got.Equal(d(tc.want)) {
				t.Fatalf("want %s, got %s", tc.want, got)
			}
		})
	}
	if got := ProposeHours(ProposeProportional, d("50"), d("10"), d("333"), d("1000")); !got.Equal(d("6.65")) {
		t.Fatalf("hours rounds to 2 places: %s", got)
	}
	if got := ProposeHours(ProposeProportional, d("30"), d("0"), d("2"), d("3")); !got.Equal(d("20")) {
		t.Fatalf("hours of 2 of 3: %s", got)
	}
}

// ratCeil6 is the exact reference for the proportional proposal: ceil(x × 10^6) / 10^6
// over big.Rat, independent of the decimal package's division.
func ratCeil6(x *big.Rat) *big.Rat {
	scaled := new(big.Rat).Mul(x, big.NewRat(1_000_000, 1))
	q, m := new(big.Int).DivMod(scaled.Num(), scaled.Denom(), new(big.Int))
	if m.Sign() != 0 {
		q.Add(q, big.NewInt(1))
	}
	return new(big.Rat).SetFrac(q, big.NewInt(1_000_000))
}

func ratOf(t *rapid.T, v decimal.Decimal) *big.Rat {
	r, ok := new(big.Rat).SetString(v.String())
	if !ok {
		t.Fatalf("not a rational: %s", v)
	}
	return r
}

// AC3 (property): after each proportional RAF the cumulative consumption of a
// finish-flushed line is exactly ceil6(required × reported ÷ order quantity),
// checked against big.Rat arithmetic, for any order quantity and RAF split.
func TestProposeQty_ProportionalMatchesExactRational(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		required := decimal.New(rapid.Int64Range(1, 1_000_000_000).Draw(t, "required"), -6)
		orderQty := decimal.NewFromInt(rapid.Int64Range(1, 997).Draw(t, "qty"))
		consumed, reported := decimal.Zero, decimal.Zero
		for reported.LessThan(orderQty) {
			step := decimal.NewFromInt(rapid.Int64Range(1, orderQty.Sub(reported).IntPart()).Draw(t, "raf"))
			reported = reported.Add(step)
			consumed = consumed.Add(ProposeQty(ProposeProportional, required, consumed, reported, orderQty))
			exact := new(big.Rat).Mul(ratOf(t, required), new(big.Rat).SetFrac(ratOf(t, reported).Num(), ratOf(t, orderQty).Num()))
			if want := ratCeil6(exact); ratOf(t, consumed).Cmp(want) != 0 {
				t.Fatalf("after %s of %s: consumed %s, want %s", reported, orderQty, consumed, want.FloatString(6))
			}
		}
	})
}

// AC-21.4: successive proportional RAFs consume exactly the requirement once
// the whole quantity is reported, however the output is split.
func TestProposeQty_ProportionalSumsToTheRequirement(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		required := decimal.New(rapid.Int64Range(1, 1_000_000_000).Draw(t, "required"), -6)
		orderQty := decimal.NewFromInt(rapid.Int64Range(1, 500).Draw(t, "qty"))
		consumed, reported := decimal.Zero, decimal.Zero
		for reported.LessThan(orderQty) {
			step := decimal.NewFromInt(rapid.Int64Range(1, orderQty.Sub(reported).IntPart()).Draw(t, "raf"))
			reported = reported.Add(step)
			consumed = consumed.Add(ProposeQty(ProposeProportional, required, consumed, reported, orderQty))
		}
		if !consumed.Equal(required) {
			t.Fatalf("consumed %s, required %s", consumed, required)
		}
	})
}

func TestCheckHours(t *testing.T) {
	for _, ok := range []string{"0.01", "46", "7.25"} {
		if err := CheckHours(d(ok)); err != nil {
			t.Errorf("%s refused: %v", ok, err)
		}
	}
	for _, bad := range []string{"0", "0.009", "1.005", "-1"} {
		if codeOf(CheckHours(d(bad))) != CodeInvalidHours {
			t.Errorf("%s accepted", bad)
		}
	}
}

func TestCheckConsumption(t *testing.T) {
	if err := CheckConsumption("10", d("200"), d("220"), d("10"), false); err != nil {
		t.Fatalf("at the tolerance: %v", err)
	}
	if codeOf(CheckConsumption("10", d("200"), d("220.000001"), d("10"), false)) != CodeOverConsumption {
		t.Fatal("beyond the tolerance without the duty was accepted")
	}
	if err := CheckConsumption("10", d("200"), d("400"), d("10"), true); err != nil {
		t.Fatalf("the duty allows over-consumption: %v", err)
	}
}

func TestResolveProfile_MostSpecificWins(t *testing.T) {
	profiles := []Profile{
		{ItemGroup: AllKey, SiteID: AllKey, WIP: "all-all"},
		{ItemGroup: AllKey, SiteID: "S1", WIP: "all-s1"},
		{ItemGroup: "FG", SiteID: AllKey, WIP: "fg-all"},
		{ItemGroup: "FG", SiteID: "S1", WIP: "fg-s1"},
	}
	tests := []struct {
		group, site string
		set         []Profile
		want        string
	}{
		{"FG", "S1", profiles, "fg-s1"},
		{"FG", "S2", profiles, "fg-all"},
		{"RM", "S1", profiles, "all-s1"},
		{"RM", "S2", profiles, "all-all"},
		{"", "S1", profiles, "all-s1"},
		{"FG", "S1", profiles[:2], "all-s1"}, // the item relation outranks the site only when it exists
	}
	for _, tc := range tests {
		got, err := ResolveProfile(tc.set, tc.group, tc.site)
		if err != nil || got.WIP != tc.want {
			t.Errorf("%s × %s: want %s, got %s (%v)", tc.group, tc.site, tc.want, got.WIP, err)
		}
	}
	if _, err := ResolveProfile(profiles[3:], "RM", "S1"); codeOf(err) != CodeNoPostingProfile {
		t.Fatalf("missing profile: %v", err)
	}
}

func TestPostings_SidesAndReversal(t *testing.T) {
	p := Profile{WIP: "WIP", Inventory: "INV", FinishedGoods: "FG", LaborAbsorption: "LAB", OverheadAbsorption: "OH"}
	issue := IssuePostings(p, d("3111"))
	if len(issue) != 2 || issue[0].Account != "WIP" || !issue[0].Dr.Equal(d("3111")) || !issue[0].WIP ||
		issue[1].Account != "INV" || !issue[1].Cr.Equal(d("3111")) || issue[1].WIP {
		t.Fatalf("issue: %+v", issue)
	}
	reversal := IssuePostings(p, d("-10"))
	if !reversal[0].Cr.Equal(d("10")) || !reversal[1].Dr.Equal(d("10")) || !Balanced(reversal) {
		t.Fatalf("a negative amount swaps the sides: %+v", reversal)
	}
	hours := HoursPostings(p, d("1380"), d("0"))
	if len(hours) != 2 || !Net(hours, true).Equal(d("1380")) {
		t.Fatalf("hours with no overhead: %+v", hours)
	}
	receipt := ReceiptPostings(p, d("5000"))
	if receipt[0].Account != "FG" || !receipt[0].Dr.Equal(d("5000")) || !Net(receipt, true).Equal(d("-5000")) {
		t.Fatalf("receipt: %+v", receipt)
	}
}

// BR-21.7: WIP Dr 4,951 / Cr 5,000 clears with Dr WIP 49 against the four variances.
func TestSettleWIP_WorkedExample(t *testing.T) {
	planned := fixtureEstimate(t)
	actual := []CostRow{
		{Group: GroupMaterial, Key: "RM-1", Qty: d("305"), Amount: d("3111")},
		{Group: GroupLabor, Key: "WC-1@10", Qty: d("46"), Amount: d("1380")},
		{Group: GroupOverhead, Key: "WC-1@10", Qty: d("46"), Amount: d("460")},
	}
	vars, err := SettleWIP(d("100"), d("100"), planned, actual, d("-49"), "USD")
	if err != nil || len(vars) != 4 {
		t.Fatalf("variances: %+v %v", vars, err)
	}
	p := Profile{WIP: "WIP", VarQuantity: "VQ", VarPrice: "VP", VarLotSize: "VL", VarSubstitution: "VS"}
	ps, err := SettlementPostings(p, d("-49"), vars)
	if err != nil {
		t.Fatal(err)
	}
	if ps[0].Account != "WIP" || !ps[0].Dr.Equal(d("49")) || !Net(ps, true).Equal(d("49")) {
		t.Fatalf("WIP clearing: %+v", ps)
	}
	byAccount := map[string]decimal.Decimal{}
	for _, x := range ps[1:] {
		byAccount[x.Account] = byAccount[x.Account].Add(x.Dr).Sub(x.Cr)
	}
	if !byAccount["VQ"].Equal(d("-110")) || !byAccount["VP"].Equal(d("61")) {
		t.Fatalf("variance accounts: %v", byAccount)
	}
	if _, err := SettlementPostings(p, d("-50"), vars); codeOf(err) != CodeUnbalanced {
		t.Fatalf("variances that do not clear the balance: %v", err)
	}
}

// The variances always clear the posted WIP balance exactly, in the
// currency's minor unit, whatever per-line rounding the postings carried.
func TestSettleWIP_ClearsThePostedBalance(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		currency := rapid.SampledFrom([]string{"USD", "JPY", "KWD"}).Draw(t, "currency")
		amount := func(label string) decimal.Decimal {
			return decimal.New(rapid.Int64Range(0, 10_000_000).Draw(t, label), -4)
		}
		planned := []CostRow{{Group: GroupMaterial, Key: "RM", Qty: d("300"), Amount: amount("planned material")},
			{Group: GroupLabor, Key: "R@10", Qty: d("50"), Amount: amount("planned labor"), Fixed: amount("fixed")}}
		actual := []CostRow{{Group: GroupMaterial, Key: "RM", Qty: decimal.New(rapid.Int64Range(0, 400_000).Draw(t, "qty"), -3), Amount: amount("actual material")}}
		balance := RoundMoney(amount("balance").Sub(amount("credit")), currency)
		vars, err := SettleWIP(d("100"), decimal.NewFromInt(rapid.Int64Range(0, 100).Draw(t, "raf")), planned, actual, balance, currency)
		if err != nil {
			t.Fatal(err)
		}
		sum := decimal.Zero
		for _, v := range vars {
			if !v.Amount.Equal(RoundMoney(v.Amount, currency)) || v.Amount.IsZero() {
				t.Fatalf("variance %s not in %s minor units or zero", v.Amount, currency)
			}
			sum = sum.Add(v.Amount)
		}
		if !sum.Equal(balance) {
			t.Fatalf("variances %s do not clear the balance %s", sum, balance)
		}
	})
}

// Successive receipts never drift from round(planned × reported ÷ quantity).
func TestReceiptValue_NoDrift(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		planned := decimal.New(rapid.Int64Range(1, 100_000_000).Draw(t, "planned"), -4)
		orderQty := decimal.NewFromInt(rapid.Int64Range(1, 300).Draw(t, "qty"))
		sum, reported := decimal.Zero, decimal.Zero
		for reported.LessThan(orderQty) {
			step := decimal.NewFromInt(rapid.Int64Range(1, orderQty.Sub(reported).IntPart()).Draw(t, "raf"))
			sum = sum.Add(ReceiptValue(planned, orderQty, reported, reported.Add(step), "USD"))
			reported = reported.Add(step)
		}
		if !sum.Equal(planned.Round(2)) {
			t.Fatalf("receipts %s, planned %s", sum, planned.Round(2))
		}
	})
}

func TestActualRows_ReversalsNetOut(t *testing.T) {
	rows := ActualRows([]ActualLine{
		{Material: true, Key: "RM", Qty: d("200"), Amount: d("2040")},
		{Material: true, Key: "RM", Qty: d("105"), Amount: d("1071")},
		{Key: "R@10", Qty: d("46"), Amount: d("1380"), Overhead: d("460")},
		{Key: "R@20", Qty: d("2"), Amount: d("60"), Overhead: d("20")},
		{Key: "R@20", Qty: d("-2"), Amount: d("-60"), Overhead: d("-20")},
	})
	if len(rows) != 3 || rows[0].Key != "RM" || !rows[0].Qty.Equal(d("305")) || !rows[0].Amount.Equal(d("3111")) ||
		rows[1].Group != GroupLabor || rows[2].Group != GroupOverhead || !rows[2].Amount.Equal(d("460")) {
		t.Fatalf("actual rows: %+v", rows)
	}
}

func TestJournalAllowed(t *testing.T) {
	for _, s := range []Status{Created, Estimated, Scheduled, Ended, Cancelled} {
		for _, typ := range []JournalType{JournalPickingList, JournalRouteCard, JournalReportFinished} {
			if JournalAllowed(typ, s, true) {
				t.Errorf("%s allowed on %s", typ, s)
			}
		}
	}
	for _, s := range []Status{Started, ReportedAsFinished} {
		for _, typ := range []JournalType{JournalPickingList, JournalRouteCard, JournalReportFinished} {
			if !JournalAllowed(typ, s, false) {
				t.Errorf("%s refused on %s", typ, s)
			}
		}
	}
	if !JournalAllowed(JournalRouteCard, Released, true) || JournalAllowed(JournalRouteCard, Released, false) ||
		JournalAllowed(JournalPickingList, Released, true) || JournalAllowed(JournalReportFinished, Released, true) {
		t.Fatal("Released: only a route card, and only with auto-start")
	}
}

func TestExplode_FlushingPrinciple(t *testing.T) {
	bom := []BOMComponent{
		{LineNo: "10", ItemID: "A", QtyPer: d("1")},
		{LineNo: "20", ItemID: "B", QtyPer: d("1"), Flushing: FlushFinish},
		{LineNo: "30", Phantom: true, QtyPer: d("1"), Flushing: FlushStart, Children: []BOMComponent{
			{ItemID: "C", QtyPer: d("1")},
			{ItemID: "D", QtyPer: d("1"), Flushing: FlushManual},
		}},
	}
	lines, err := Explode(bom, d("10"))
	if err != nil {
		t.Fatal(err)
	}
	want := []Flushing{FlushManual, FlushFinish, FlushStart, FlushManual}
	for i, w := range want {
		if lines[i].Flushing != w || !lines[i].QtyConsumed.IsZero() {
			t.Errorf("line %s: flushing %s, want %s", lines[i].LineNo, lines[i].Flushing, w)
		}
	}
	bom[0].Flushing = "sometimes"
	if _, err := Explode(bom, d("10")); codeOf(err) != CodeInvalidInput {
		t.Fatalf("unknown flushing principle: %v", err)
	}
}
