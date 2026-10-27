package domain

import (
	"errors"
	"testing"

	"github.com/shopspring/decimal"
	"pgregory.net/rapid"
)

func rpDec(s string) decimal.Decimal { return decimal.RequireFromString(s) }

func rpTradeSource(id string) string { return PriceSourceTradePrefix + id }

func TestRepriceSkipReason(t *testing.T) {
	base := RepriceLine{LineNo: 1, ProductID: "P", PriceSource: rpTradeSource("T1"), InvoicedQty: decimal.Zero}
	cases := []struct {
		name string
		mut  func(*RepriceLine)
		want string
	}{
		{"trade agreement line is looked up", func(*RepriceLine) {}, ""},
		{"invoiced line is still looked up (handed to payables)", func(l *RepriceLine) { l.InvoicedQty = rpDec("1") }, ""},
		{"manual price", func(l *RepriceLine) { l.PriceSource = PriceSourceManual }, SkipManualPrice},
		{"manual wins over agreement", func(l *RepriceLine) { l.PriceSource, l.AgreementDrawn = PriceSourceManual, true }, SkipManualPrice},
		{"drawn against a purchase agreement", func(l *RepriceLine) { l.AgreementDrawn = true }, SkipAgreementLine},
		{"agreement price", func(l *RepriceLine) { l.PriceSource = "AGREEMENT:A1" }, SkipNotTradePrice},
		{"no product", func(l *RepriceLine) { l.ProductID = "" }, SkipNotTradePrice},
		{"requisition estimate", func(l *RepriceLine) { l.PriceSource = "REQUISITION_ESTIMATE" }, SkipNotTradePrice},
		{"RFQ reply", func(l *RepriceLine) { l.PriceSource = "RFQ_REPLY:R1" }, SkipNotTradePrice},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			l := base
			tc.mut(&l)
			if got := RepriceSkipReason(l); got != tc.want {
				t.Fatalf("RepriceSkipReason = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestCheckRepriceable(t *testing.T) {
	if err := CheckRepriceable(POStatusConfirmed); err != nil {
		t.Fatalf("CONFIRMED refused: %v", err)
	}
	for _, s := range []string{POStatusDraft, POStatusInReview, POStatusApproved, POStatusRejected, POStatusFinalized, POStatusCancelled} {
		var de *Error
		if err := CheckRepriceable(s); !errors.As(err, &de) || de.Code != CodePOState {
			t.Fatalf("%s: error %v, want %s", s, err, CodePOState)
		}
	}
}

func TestPlanReprice(t *testing.T) {
	line := func(no int, price, priceUnit, disc string) RepriceLine {
		return RepriceLine{LineNo: no, ProductID: "P", Unit: "EA", PriceSource: rpTradeSource("T1"), InvoicedQty: decimal.Zero,
			Input: LineInput{Qty: rpDec("100"), UnitPrice: rpDec(price), PriceUnit: rpDec(priceUnit), DiscountPct: rpDec(disc)}}
	}
	quoteOf := func(price, priceUnit string) func(RepriceLine) (RepriceQuote, bool) {
		return func(RepriceLine) (RepriceQuote, bool) {
			return RepriceQuote{Price: rpDec(price), PriceUnit: rpDec(priceUnit), Source: rpTradeSource("T2")}, true
		}
	}
	noQuote := func(RepriceLine) (RepriceQuote, bool) { return RepriceQuote{}, false }
	invoiced := line(4, "10", "1", "0")
	invoiced.InvoicedQty = rpDec("40")

	cases := []struct {
		name         string
		lines        []RepriceLine
		quote        func(RepriceLine) (RepriceQuote, bool)
		wantLines    []int
		wantInvoiced []int
		wantSkip     map[int]string
		wantDelta    string
	}{
		{"price drop moves the line", []RepriceLine{line(1, "10", "1", "0")}, quoteOf("9.5", "1"), []int{1}, nil, nil, "-50"},
		{"per-100 price unit", []RepriceLine{line(1, "1000", "100", "0")}, quoteOf("900", "100"), []int{1}, nil, nil, "-100"},
		{"price unit change alone moves the line", []RepriceLine{line(1, "10", "1", "0")}, quoteOf("10", "10"), []int{1}, nil, nil, "-900"},
		{"discount applies to the delta", []RepriceLine{line(1, "10", "1", "10")}, quoteOf("9.5", "1"), []int{1}, nil, nil, "-45"},
		{"same price is unchanged", []RepriceLine{line(1, "10", "1", "0")}, quoteOf("10", "1"), nil, nil,
			map[int]string{1: SkipUnchanged}, "0"},
		{"no current price skips", []RepriceLine{line(1, "10", "1", "0")}, noQuote, nil, nil,
			map[int]string{1: SkipNoCurrentPrice}, "0"},
		{"manual line is never touched", []RepriceLine{{LineNo: 2, ProductID: "P", PriceSource: PriceSourceManual,
			Input: LineInput{Qty: rpDec("1"), UnitPrice: rpDec("5"), PriceUnit: rpDec("1")}}}, quoteOf("1", "1"), nil, nil,
			map[int]string{2: SkipManualPrice}, "0"},
		{"invoiced line is handed over, not moved", []RepriceLine{invoiced, line(1, "10", "1", "0")}, quoteOf("9.5", "1"),
			[]int{1}, []int{4}, map[int]string{4: SkipInvoiced}, "-50"},
		{"invoiced line already handed over for this row", []RepriceLine{func() RepriceLine {
			l := invoiced
			l.HandedOver = rpTradeSource("T2")
			return l
		}()}, quoteOf("9.5", "1"), nil, nil, map[int]string{4: SkipHandedOver}, "0"},
		{"invoiced line handed over for an older row goes again", []RepriceLine{func() RepriceLine {
			l := invoiced
			l.HandedOver = rpTradeSource("T0")
			return l
		}()}, quoteOf("9.5", "1"), nil, []int{4}, map[int]string{4: SkipInvoiced}, "0"},
		{"invoiced line at the current price is just unchanged", []RepriceLine{invoiced}, quoteOf("10", "1"), nil, nil,
			map[int]string{4: SkipUnchanged}, "0"},
		{"results are ordered by line", []RepriceLine{line(3, "10", "1", "0"), line(1, "10", "1", "0")}, quoteOf("9", "1"),
			[]int{1, 3}, nil, nil, "-200"},
	}
	lineNos := func(cs []RepriceChange) []int {
		out := []int{}
		for _, c := range cs {
			out = append(out, c.LineNo)
		}
		return out
	}
	same := func(a, b []int) bool {
		if len(a) != len(b) {
			return false
		}
		for i := range a {
			if a[i] != b[i] {
				return false
			}
		}
		return true
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			plan, err := PlanReprice(tc.lines, tc.quote, 2)
			if err != nil {
				t.Fatal(err)
			}
			if got := lineNos(plan.Changes); !same(got, tc.wantLines) {
				t.Fatalf("changed lines = %v, want %v", got, tc.wantLines)
			}
			if got := lineNos(plan.Invoiced); !same(got, tc.wantInvoiced) {
				t.Fatalf("invoiced lines = %v, want %v", got, tc.wantInvoiced)
			}
			if len(plan.Skipped) != len(tc.wantSkip) {
				t.Fatalf("skipped = %+v, want %v", plan.Skipped, tc.wantSkip)
			}
			for _, s := range plan.Skipped {
				if tc.wantSkip[s.LineNo] != s.Reason {
					t.Fatalf("line %d skipped as %q, want %q", s.LineNo, s.Reason, tc.wantSkip[s.LineNo])
				}
			}
			if !plan.TotalDelta.Equal(rpDec(tc.wantDelta)) {
				t.Fatalf("total delta = %s, want %s", plan.TotalDelta, tc.wantDelta)
			}
		})
	}
}

// The invoiced hand-over is priced on the invoiced quantity only and keeps
// both prices and sources, which is what payables needs for FR-18.18.
func TestPlanRepriceInvoicedLineCarriesInvoicedQuantity(t *testing.T) {
	l := RepriceLine{LineNo: 7, ProductID: "P", PriceSource: rpTradeSource("T1"), InvoicedQty: rpDec("40"),
		Input: LineInput{Qty: rpDec("100"), UnitPrice: rpDec("10"), PriceUnit: rpDec("1"), DiscountPct: decimal.Zero}}
	plan, err := PlanReprice([]RepriceLine{l}, func(RepriceLine) (RepriceQuote, bool) {
		return RepriceQuote{Price: rpDec("9.5"), PriceUnit: rpDec("1"), Source: rpTradeSource("T2")}, true
	}, 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Invoiced) != 1 {
		t.Fatalf("invoiced = %+v", plan.Invoiced)
	}
	c := plan.Invoiced[0]
	if !c.Qty.Equal(rpDec("40")) || !c.OldAmount.Equal(rpDec("400")) || !c.NewAmount.Equal(rpDec("380")) || !c.Delta.Equal(rpDec("-20")) {
		t.Fatalf("invoiced change = %+v, want 40 × 10.00 → 9.50 = -20", c)
	}
	if c.OldSource != rpTradeSource("T1") || c.NewSource != rpTradeSource("T2") {
		t.Fatalf("sources %q → %q", c.OldSource, c.NewSource)
	}
	if !plan.TotalDelta.IsZero() {
		t.Fatalf("total delta %s counts an invoiced line", plan.TotalDelta)
	}
}

// Property: a change's delta is exactly the difference between the two priced
// line amounts, the total is the sum of the deltas, and an unchanged price
// never produces a change.
func TestPlanRepriceDeltaIdentity(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		n := rapid.IntRange(1, 8).Draw(t, "lines")
		lines := make([]RepriceLine, n)
		quotes := map[int]RepriceQuote{}
		for i := range lines {
			in := LineInput{Qty: decimal.NewFromInt(rapid.Int64Range(1, 500).Draw(t, "qty")),
				UnitPrice:   decimal.New(rapid.Int64Range(0, 100000).Draw(t, "oldPrice"), -4),
				PriceUnit:   decimal.NewFromInt(rapid.SampledFrom([]int64{1, 10, 100}).Draw(t, "unit")),
				DiscountPct: decimal.NewFromInt(rapid.Int64Range(0, 100).Draw(t, "discount"))}
			lines[i] = RepriceLine{LineNo: i + 1, ProductID: "P", PriceSource: rpTradeSource("T1"), Input: in}
			quotes[i+1] = RepriceQuote{Price: decimal.New(rapid.Int64Range(0, 100000).Draw(t, "newPrice"), -4),
				PriceUnit: in.PriceUnit, Source: rpTradeSource("T2")}
		}
		plan, err := PlanReprice(lines, func(l RepriceLine) (RepriceQuote, bool) { return quotes[l.LineNo], true }, 2)
		if err != nil {
			t.Fatal(err)
		}
		sum := decimal.Zero
		for _, c := range plan.Changes {
			l := lines[c.LineNo-1]
			if c.NewPrice.Equal(l.Input.UnitPrice) {
				t.Fatalf("line %d changed at an unchanged price", c.LineNo)
			}
			before, _ := ComputeLine(l.Input, 2)
			next := l.Input
			next.UnitPrice = quotes[c.LineNo].Price
			after, _ := ComputeLine(next, 2)
			if !c.Delta.Equal(after.Amount.Sub(before.Amount)) {
				t.Fatalf("line %d delta %s, want %s", c.LineNo, c.Delta, after.Amount.Sub(before.Amount))
			}
			sum = sum.Add(c.Delta)
		}
		if !plan.TotalDelta.Equal(sum) {
			t.Fatalf("total %s, sum of deltas %s", plan.TotalDelta, sum)
		}
		if len(plan.Changes)+len(plan.Skipped) != n {
			t.Fatalf("%d changes + %d skipped for %d lines", len(plan.Changes), len(plan.Skipped), n)
		}
	})
}
