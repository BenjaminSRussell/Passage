package domain

import (
	"errors"
	"fmt"
	"math/rand"
	"testing"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/money"
)

func group() Matrix {
	return Matrix{
		{EntityA: "A", EntityB: "B", DueFrom: "1500-DUE-FROM-B", DueTo: "2500-DUE-TO-A"},
		{EntityA: "B", EntityB: "A", DueFrom: "1500-DUE-FROM-A", DueTo: "2500-DUE-TO-B"},
	}
}

func TestDeriveJournal_WorkedExampleAC404(t *testing.T) {
	// AC-40.4: USD 1,000.00 expense in A offset to B, B pays from cash.
	line := Line{Entity: "A", Account: "6100-EXPENSE", Side: Debit, Amount: money.Must("1000", "USD")}
	j, err := DeriveJournal(group(), "pair-9", line, "B", "1000-CASH", "USD", decimal.NewFromInt(1))
	if err != nil {
		t.Fatal(err)
	}
	want := []Leg{
		{"A", "6100-EXPENSE", Debit, money.Must("1000", "USD")},
		{"A", "1500-DUE-FROM-B", Credit, money.Must("1000", "USD")},
		{"B", "2500-DUE-TO-A", Debit, money.Must("1000", "USD")},
		{"B", "1000-CASH", Credit, money.Must("1000", "USD")},
	}
	if len(j.Legs) != len(want) || j.PairID != "pair-9" {
		t.Fatalf("journal = %+v", j)
	}
	for i := range want {
		got, w := j.Legs[i], want[i]
		if got.Entity != w.Entity || got.Account != w.Account || got.Side != w.Side || got.Amount.String() != w.Amount.String() {
			t.Fatalf("leg %d = %+v; want %+v", i, got, w)
		}
	}
}

func TestDeriveJournal_Table(t *testing.T) {
	cases := []struct {
		name     string
		m        Matrix
		line     Line
		offset   string
		counter  string
		rate     decimal.Decimal
		wantCode string
		wantB    string
	}{
		{"converts counterparty at rate", group(), Line{"A", "6100", Debit, money.Must("100", "USD")}, "B", "EUR", decimal.RequireFromString("0.92"), "", "92.00 EUR"},
		{"missing matrix row fails IC-0805", Matrix{}, Line{"A", "6100", Debit, money.Must("100", "USD")}, "B", "USD", decimal.NewFromInt(1), CodeNoMatrix, ""},
		{"reverse direction uses B row", group(), Line{"B", "6100", Credit, money.Must("50", "EUR")}, "A", "EUR", decimal.NewFromInt(1), "", "50.00 EUR"},
		{"self offset rejected", group(), Line{"A", "6100", Debit, money.Must("1", "USD")}, "A", "USD", decimal.NewFromInt(1), CodeInvalid, ""},
		{"zero rate rejected", group(), Line{"A", "6100", Debit, money.Must("1", "USD")}, "B", "USD", decimal.Zero, CodeInvalid, ""},
		{"zero amount rejected", group(), Line{"A", "6100", Debit, money.Amount{Value: decimal.RequireFromString("0.004"), Currency: "USD"}}, "B", "USD", decimal.NewFromInt(1), CodeInvalid, ""},
		{"line amount rounds to the minor unit before deriving", group(), Line{"A", "6100", Debit, money.Amount{Value: decimal.RequireFromString("10.005"), Currency: "USD"}}, "B", "USD", decimal.NewFromInt(1), "", "10.01 USD"},
		{"JPY counterparty has no decimals", group(), Line{"A", "6100", Debit, money.Must("100", "USD")}, "B", "JPY", decimal.RequireFromString("151.237"), "", "15124 JPY"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			j, err := DeriveJournal(c.m, "p", c.line, c.offset, "1000-CASH", c.counter, c.rate)
			if c.wantCode != "" {
				var de *Error
				if !errors.As(err, &de) || de.Code != c.wantCode {
					t.Fatalf("err = %v; want code %s", err, c.wantCode)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if got := j.Legs[2].Amount.String(); got != c.wantB {
				t.Fatalf("B leg = %s; want %s", got, c.wantB)
			}
		})
	}
}

func TestJournalBalanced_DetectsImbalance(t *testing.T) {
	j := Journal{Legs: []Leg{
		{"A", "x", Debit, money.Must("10", "USD")},
		{"A", "y", Credit, money.Must("9.99", "USD")},
	}}
	if err := j.Balanced(); err == nil {
		t.Fatal("unbalanced journal passed")
	}
}

// TestDeriveJournal_PropertyBalancesAcrossEntities is the random-order check
// behind AC1: every derived pair balances in both entities and the amounts
// are equal at the document rate.
func TestDeriveJournal_PropertyBalancesAcrossEntities(t *testing.T) {
	r := rand.New(rand.NewSource(7))
	for i := 0; i < 1000; i++ {
		cents := int64(1 + r.Intn(10000000))
		amount := decimal.New(cents, -2)
		side := Debit
		if r.Intn(2) == 0 {
			side = Credit
		}
		rate := decimal.New(int64(50+r.Intn(150)), -2)
		line := Line{Entity: "A", Account: fmt.Sprintf("6%03d", r.Intn(1000)), Side: side, Amount: money.Amount{Value: amount, Currency: "USD"}}
		j, err := DeriveJournal(group(), "p", line, "B", "1000-CASH", "EUR", rate)
		if err != nil {
			t.Fatalf("case %d: %v", i, err)
		}
		if err := j.Balanced(); err != nil {
			t.Fatalf("case %d: %v", i, err)
		}
		if j.Legs[2].Amount.Value.Cmp(j.Legs[3].Amount.Value) != 0 {
			t.Fatalf("case %d: B legs differ", i)
		}
	}
}
