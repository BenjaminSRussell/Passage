package domain

import (
	"errors"
	"testing"

	"github.com/shopspring/decimal"
	"pgregory.net/rapid"
)

func TestCanBeCredited(t *testing.T) {
	tests := []struct {
		name    string
		status  string
		typ     string
		wantErr bool
	}{
		{name: "posted invoice", status: StatusPosted, typ: TypeFreeText},
		{name: "partially settled invoice", status: StatusPartiallySettled, typ: TypeShipment},
		{name: "settled invoice", status: StatusSettled, typ: TypeSubscription},
		{name: "proposal", status: StatusProposal, typ: TypeFreeText, wantErr: true},
		{name: "reversed invoice", status: StatusReversed, typ: TypeFreeText, wantErr: true},
		{name: "credit note", status: StatusPosted, typ: TypeCreditNote, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := CanBeCredited(tt.status, tt.typ)
			if (err != nil) != tt.wantErr {
				t.Fatalf("CanBeCredited(%s, %s) = %v, want error %v", tt.status, tt.typ, err, tt.wantErr)
			}
			if err != nil {
				wantCode(t, err, CodeCreditExceeded)
			}
		})
	}
}

func wantCode(t *testing.T, err error, code string) {
	t.Helper()
	var de *Error
	if !errors.As(err, &de) || de.Code != code {
		t.Fatalf("got %v, want %s", err, code)
	}
}

// original is a 2-line EUR invoice: 3 × 33.33 at 19% (net 99.99, tax 19.00)
// and 1 × 100 at 7% (net 100.00, tax 7.00), plus 10.00 charges: gross 235.99.
func original() ([]PostedLine, Totals) {
	lines := []PostedLine{
		{Line: Line{No: 1, Description: "Consulting", Qty: d("3"), UnitPrice: d("33.33"), DiscountPct: decimal.Zero, RevenueAccount: "4900", TaxCode: "DE19"}, Net: d("99.99"), Tax: d("19.00")},
		{Line: Line{No: 2, Description: "Book", Qty: d("1"), UnitPrice: d("100"), DiscountPct: decimal.Zero, RevenueAccount: "4000", TaxCode: "DE7"}, Net: d("100.00"), Tax: d("7.00")},
	}
	return lines, Totals{Net: d("199.99"), Charges: d("10.00"), Tax: d("26.00"), Gross: d("235.99")}
}

func noUsage() CreditUsage {
	return CreditUsage{Lines: map[int]Credited{}, Charges: decimal.Zero, Gross: decimal.Zero}
}

// add records a posted credit note in usage, as the store aggregates it.
func add(u CreditUsage, lines []CreditLine, totals Totals) CreditUsage {
	for _, l := range lines {
		c := u.line(l.OriginalLineNo)
		u.Lines[l.OriginalLineNo] = Credited{Qty: c.Qty.Add(l.Line.Qty), Net: c.Net.Add(l.Net), Tax: c.Tax.Add(l.Tax)}
	}
	u.Charges = u.Charges.Add(totals.Charges)
	u.Gross = u.Gross.Add(totals.Gross)
	return u
}

func TestPlanCredit_WholeInvoiceReversesItExactly(t *testing.T) {
	orig, totals := original()
	lines, got, err := PlanCredit("EUR", orig, totals, noUsage(), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !got.Gross.Equal(totals.Gross) || !got.Tax.Equal(totals.Tax) || !got.Charges.Equal(totals.Charges) || !got.Net.Equal(totals.Net) {
		t.Fatalf("whole credit totals %+v, want the original %+v", got, totals)
	}
	if len(lines) != 2 || lines[0].OriginalLineNo != 1 || lines[1].OriginalLineNo != 2 || lines[1].Line.No != 2 || lines[0].Line.TaxCode != "DE19" {
		t.Fatalf("credit lines %+v", lines)
	}
	// Nothing is left afterwards.
	_, _, err = PlanCredit("EUR", orig, totals, add(noUsage(), lines, got), nil, nil)
	wantCode(t, err, CodeCreditExceeded)
}

func TestPlanCredit_PartialQuantitiesSumToTheLine(t *testing.T) {
	orig, totals := original()
	used := noUsage()
	var net, tax decimal.Decimal
	// Credit line 1 one unit at a time: 33.33 + 33.33 + 33.33 net, 6.33 + 6.33 + 6.34 tax.
	for i := 0; i < 3; i++ {
		lines, got, err := PlanCredit("EUR", orig, totals, used, []CreditQty{{LineNo: 1, Qty: d("1")}}, nil)
		if err != nil {
			t.Fatalf("credit %d: %v", i+1, err)
		}
		if !got.Charges.IsZero() || len(lines) != 1 || lines[0].Line.No != 1 {
			t.Fatalf("credit %d: %+v %+v (a line credit carries no charges)", i+1, lines, got)
		}
		net, tax = net.Add(got.Net), tax.Add(got.Tax)
		used = add(used, lines, got)
	}
	if !net.Equal(d("99.99")) || !tax.Equal(d("19.00")) {
		t.Fatalf("three partial credits sum to net %s tax %s, want the line's 99.99 and 19.00", net, tax)
	}
	_, _, err := PlanCredit("EUR", orig, totals, used, []CreditQty{{LineNo: 1, Qty: d("0.5")}}, nil)
	wantCode(t, err, CodeCreditExceeded)
	// The rest of the invoice is still creditable.
	_, rest, err := PlanCredit("EUR", orig, totals, used, nil, nil)
	if err != nil || !rest.Gross.Equal(d("117.00")) {
		t.Fatalf("remainder %+v %v, want 100.00 + 7.00 + 10.00 charges", rest, err)
	}
}

func TestPlanCredit_Refusals(t *testing.T) {
	orig, totals := original()
	charge := func(s string) *decimal.Decimal { v := d(s); return &v }
	tests := []struct {
		name    string
		req     []CreditQty
		charges *decimal.Decimal
		code    string
	}{
		{name: "more than the line quantity", req: []CreditQty{{LineNo: 2, Qty: d("2")}}, code: CodeCreditExceeded},
		{name: "unknown line", req: []CreditQty{{LineNo: 9, Qty: d("1")}}, code: "COM-0001"},
		{name: "line listed twice", req: []CreditQty{{LineNo: 1, Qty: d("1")}, {LineNo: 1, Qty: d("1")}}, code: "COM-0001"},
		{name: "zero quantity", req: []CreditQty{{LineNo: 1, Qty: decimal.Zero}}, code: "COM-0001"},
		{name: "charges beyond the original", req: []CreditQty{{LineNo: 1, Qty: d("1")}}, charges: charge("10.01"), code: CodeCreditExceeded},
		{name: "charges without lines", charges: charge("5"), code: "COM-0001"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, _, err := PlanCredit("EUR", orig, totals, noUsage(), tt.req, tt.charges)
			wantCode(t, err, tt.code)
		})
	}
	lines, got, err := PlanCredit("EUR", orig, totals, noUsage(), []CreditQty{{LineNo: 2, Qty: d("1")}}, charge("10"))
	if err != nil || len(lines) != 1 || !got.Gross.Equal(d("117.00")) {
		t.Fatalf("line 2 with charges: %+v %+v %v", lines, got, err)
	}
}

// TestVerifyCredit_RejectsAProposalOvertakenByAnotherPost: two proposals that
// each fit alone cannot both post.
func TestVerifyCredit_RejectsAProposalOvertakenByAnotherPost(t *testing.T) {
	orig, totals := original()
	first, firstTotals, err := PlanCredit("EUR", orig, totals, noUsage(), []CreditQty{{LineNo: 1, Qty: d("2")}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	second, secondTotals, err := PlanCredit("EUR", orig, totals, noUsage(), []CreditQty{{LineNo: 1, Qty: d("2")}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	used := add(noUsage(), first, firstTotals)
	wantCode(t, VerifyCredit("EUR", orig, totals, used, second, secondTotals), CodeCreditExceeded)
}

// TestPlanCredit_NeverExceedsTheOriginal: any sequence of partial credits stays
// within each line and the original gross, and crediting the rest afterwards
// lands exactly on the original totals.
func TestPlanCredit_NeverExceedsTheOriginal(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		qty := rapid.Int64Range(1, 50).Draw(t, "qty")
		cents := rapid.Int64Range(1, 1_000_000).Draw(t, "unitCents")
		taxCents := rapid.Int64Range(0, 500_000).Draw(t, "taxCents")
		line := Line{No: 1, Description: "x", Qty: decimal.NewFromInt(qty), UnitPrice: decimal.New(cents, -2), DiscountPct: decimal.Zero, RevenueAccount: "4000", TaxCode: "T"}
		orig := []PostedLine{{Line: line, Net: line.Net("USD"), Tax: decimal.New(taxCents, -2)}}
		totals := Totals{Net: orig[0].Net, Charges: decimal.Zero, Tax: orig[0].Tax}
		totals.Gross = totals.Net.Add(totals.Tax)
		used := noUsage()
		for left := qty; left > 1; {
			step := rapid.Int64Range(1, left-1).Draw(t, "step")
			lines, got, err := PlanCredit("USD", orig, totals, used, []CreditQty{{LineNo: 1, Qty: decimal.NewFromInt(step)}}, nil)
			if err != nil {
				t.Fatalf("partial credit of %d of %d refused: %v", step, left, err)
			}
			used = add(used, lines, got)
			left -= step
		}
		_, rest, err := PlanCredit("USD", orig, totals, used, nil, nil)
		if err != nil {
			t.Fatalf("crediting the rest failed: %v", err)
		}
		if sum := used.Gross.Add(rest.Gross); !sum.Equal(totals.Gross) {
			t.Fatalf("partials %s + rest %s = %s, want the original gross %s", used.Gross, rest.Gross, sum, totals.Gross)
		}
	})
}

// A credit by amount takes the net exactly, the quantity and tax in proportion, and the last of a line exactly what is left.
func TestPlanAmountCredit(t *testing.T) {
	d := decimal.RequireFromString
	orig := []PostedLine{{Line: Line{No: 1, Description: "Support", Qty: d("12"), UnitPrice: d("100"), RevenueAccount: "4000", TaxCode: "S19"}, Net: d("1200.00"), Tax: d("228.00")}}
	totals := Totals{Net: d("1200.00"), Charges: decimal.Zero, Tax: d("228.00"), Gross: d("1428.00")}
	lines, got, err := PlanAmountCredit("EUR", orig, totals, CreditUsage{}, []AmountCredit{{LineNo: 1, Net: d("300.00")}})
	if err != nil {
		t.Fatal(err)
	}
	if !lines[0].Net.Equal(d("300")) || !lines[0].Tax.Equal(d("57")) || !lines[0].Line.Qty.Equal(d("3")) || !got.Gross.Equal(d("357")) {
		t.Fatalf("credit %+v totals %+v", lines[0], got)
	}
	if _, _, err := PlanAmountCredit("EUR", orig, totals, CreditUsage{}, []AmountCredit{{LineNo: 1, Net: d("1200.01")}}); err == nil {
		t.Fatal("a credit above the line was accepted")
	}
	if _, _, err := PlanAmountCredit("EUR", orig, totals, CreditUsage{}, []AmountCredit{{LineNo: 1, Net: d("0.001")}}); err == nil {
		t.Fatal("a credit below the minor unit was accepted")
	}
}
