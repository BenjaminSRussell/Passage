package domain

import (
	"testing"
	"time"

	"github.com/shopspring/decimal"
)

var chargeMatrix = Matrix{{EntityA: "P", EntityB: "S", DueFrom: "DUE_FROM_S", DueTo: "DUE_TO_P"}}

func TestResolveTransferPrice(t *testing.T) {
	d := func(day int) time.Time { return time.Date(2026, 1, day, 0, 0, 0, 0, time.UTC) }
	prices := []TransferPrice{
		{Lender: "P", Borrower: "S", Category: "Hour", Role: "Consultant", Method: TPCostPlus, MarkupPct: decimal.NewFromInt(10), Currency: "EUR", EffectiveFrom: d(1)},
		{Lender: "P", Borrower: "S", Category: "Hour", Role: "Consultant", Method: TPTable, UnitPrice: decimal.NewFromInt(70), Currency: "EUR", EffectiveFrom: d(15)},
	}
	tests := []struct {
		name   string
		on     time.Time
		method string
		code   string
	}{
		{name: "earlier row applies before the new price", on: d(10), method: TPCostPlus},
		{name: "latest effective row applies on and after its date", on: d(20), method: TPTable},
		{name: "no row before the first effective date", on: time.Date(2025, 12, 31, 0, 0, 0, 0, time.UTC), code: CodeTransferPrice},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ResolveTransferPrice(prices, "P", "S", "Hour", "Consultant", tc.on)
			if tc.code != "" {
				if de, ok := err.(*Error); !ok || de.Code != tc.code {
					t.Fatalf("want %s, got %v", tc.code, err)
				}
				return
			}
			if err != nil || got.Method != tc.method {
				t.Fatalf("got %+v err %v", got, err)
			}
		})
	}
	if _, err := ResolveTransferPrice(prices, "P", "S", "Hour", "Other", d(20)); err == nil {
		t.Fatal("unknown role must fail with IC-0821")
	}
	dup := append(prices, TransferPrice{Lender: "P", Borrower: "S", Category: "Hour", Role: "Consultant", Method: TPCost, Currency: "EUR", EffectiveFrom: d(15)})
	if _, err := ResolveTransferPrice(dup, "P", "S", "Hour", "Consultant", d(20)); err == nil {
		t.Fatal("two prices effective on one date must be rejected")
	} else if de, ok := err.(*Error); !ok || de.Code != CodeInvalid {
		t.Fatalf("want %s for ambiguous prices, got %v", CodeInvalid, err)
	}
}

// TestChargeJournal_AC40_11 is AC-40.11: 8 hours at cost 60.00 with cost-plus 10%
// give a borrower cost and due-to of 528.00 and a lender recovery of 480.00 plus
// an IC margin of 48.00.
func TestChargeJournal_AC40_11(t *testing.T) {
	tp := TransferPrice{Method: TPCostPlus, MarkupPct: decimal.NewFromInt(10), Currency: "EUR"}
	p, err := PriceWithTransfer(tp, decimal.NewFromInt(8), decimal.NewFromInt(60))
	if err != nil {
		t.Fatal(err)
	}
	j, err := ChargeJournal(chargeMatrix, "pair-1", "P", "S", "PROJECT_COST", "COST_RECOVERY", "IC_MARGIN", p)
	if err != nil {
		t.Fatal(err)
	}
	if err := j.Balanced(); err != nil {
		t.Fatal(err)
	}
	want := []struct {
		entity, account string
		side            Side
		amount          string
	}{
		{"S", "PROJECT_COST", Debit, "528"},
		{"S", "DUE_TO_P", Credit, "528"},
		{"P", "DUE_FROM_S", Debit, "528"},
		{"P", "COST_RECOVERY", Credit, "480"},
		{"P", "IC_MARGIN", Credit, "48"},
	}
	if len(j.Legs) != len(want) {
		t.Fatalf("got %d legs, want %d", len(j.Legs), len(want))
	}
	for i, w := range want {
		l := j.Legs[i]
		if l.Entity != w.entity || l.Account != w.account || l.Side != w.side || !l.Amount.Value.Equal(decimal.RequireFromString(w.amount)) {
			t.Fatalf("leg %d = %+v, want %+v", i, l, w)
		}
	}
}

func TestPriceWithTransfer_TableAndErrors(t *testing.T) {
	tp := TransferPrice{Method: TPTable, UnitPrice: decimal.NewFromInt(70), Currency: "EUR"}
	p, err := PriceWithTransfer(tp, decimal.NewFromInt(8), decimal.NewFromInt(60))
	if err != nil {
		t.Fatal(err)
	}
	if !p.Price.Value.Equal(decimal.NewFromInt(560)) || !p.Margin.Value.Equal(decimal.NewFromInt(80)) {
		t.Fatalf("table pricing = %+v", p)
	}
	if _, err := PriceWithTransfer(TransferPrice{Method: "Bogus", Currency: "EUR"}, decimal.NewFromInt(1), decimal.Zero); err == nil {
		t.Fatal("unknown method must fail")
	}
	if _, err := ChargeJournal(chargeMatrix, "pair", "P", "P", "a", "b", "c", p); err == nil {
		t.Fatal("same lender and borrower must fail")
	}
	if _, err := ChargeJournal(chargeMatrix, "pair", "S", "P", "a", "b", "c", p); err == nil {
		t.Fatal("missing matrix row must fail with IC-0805")
	} else if de, ok := err.(*Error); !ok || de.Code != CodeNoMatrix {
		t.Fatalf("want %s, got %v", CodeNoMatrix, err)
	}
	zero, err := PriceWithTransfer(TransferPrice{Method: TPCost, Currency: "EUR"}, decimal.NewFromInt(1), decimal.Zero)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ChargeJournal(chargeMatrix, "pair", "P", "S", "a", "b", "c", zero); err == nil {
		t.Fatal("a zero-price charge must not post")
	}
}

// TestChargeJournal_TableBelowCost checks that a table price below cost posts the
// IC loss as a debit on the margin account and the lender still balances.
func TestChargeJournal_TableBelowCost(t *testing.T) {
	tp := TransferPrice{Method: TPTable, UnitPrice: decimal.NewFromInt(55), Currency: "EUR"}
	p, err := PriceWithTransfer(tp, decimal.NewFromInt(8), decimal.NewFromInt(60))
	if err != nil {
		t.Fatal(err)
	}
	if !p.Margin.Value.Equal(decimal.NewFromInt(-40)) {
		t.Fatalf("margin = %s, want -40", p.Margin.Value)
	}
	j, err := ChargeJournal(chargeMatrix, "pair-2", "P", "S", "PROJECT_COST", "COST_RECOVERY", "IC_MARGIN", p)
	if err != nil {
		t.Fatal(err)
	}
	last := j.Legs[len(j.Legs)-1]
	if last.Account != "IC_MARGIN" || last.Side != Debit || !last.Amount.Value.Equal(decimal.NewFromInt(40)) {
		t.Fatalf("margin leg = %+v, want debit 40", last)
	}
	for _, l := range j.Legs {
		if l.Amount.Value.IsNegative() {
			t.Fatalf("leg %+v has a negative amount", l)
		}
	}
}
