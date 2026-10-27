package domain

import (
	"errors"
	"testing"

	"github.com/BenjaminSRussell/passage/platform/money"
)

// TestReverseJournal_AC4010Payment reverses the AC-40.10 centralized payment
// after a bank return (BR-40.11): every leg flips side under the same pair id.
func TestReverseJournal_AC4010Payment(t *testing.T) {
	setup := CentralizedPaymentSetup{PayingEntity: "P", OwningEntity: "S", PaymentMethod: "SEPA", Direction: DirectionPay}
	m := Matrix{{EntityA: "P", EntityB: "S", DueFrom: "1510-DUE-FROM-S", DueTo: "2510-DUE-TO-P"}}
	j, err := CentralPaymentJournal(m, "pair-1", setup, money.Must("10000.00", "EUR"), "1100-BANK", "2000-VENDOR")
	if err != nil {
		t.Fatal(err)
	}
	r, err := ReverseJournal(j)
	if err != nil {
		t.Fatal(err)
	}
	want := []Leg{
		{Entity: "P", Account: "1510-DUE-FROM-S", Side: Credit},
		{Entity: "P", Account: "1100-BANK", Side: Debit},
		{Entity: "S", Account: "2000-VENDOR", Side: Credit},
		{Entity: "S", Account: "2510-DUE-TO-P", Side: Debit},
	}
	if r.PairID != "pair-1" || len(r.Legs) != len(want) {
		t.Fatalf("reversal = %+v", r)
	}
	for i, w := range want {
		l := r.Legs[i]
		if l.Entity != w.Entity || l.Account != w.Account || l.Side != w.Side || !l.Amount.Value.Equal(d("10000")) {
			t.Fatalf("leg %d = %+v; want %+v at 10000.00", i, l, w)
		}
	}
	if err := r.Balanced(); err != nil {
		t.Fatal(err)
	}
}

func TestReverseJournal_Rejections(t *testing.T) {
	amt := money.Must("1.00", "EUR")
	legs := []Leg{{Entity: "P", Account: "A", Side: Debit, Amount: amt}, {Entity: "P", Account: "B", Side: Credit, Amount: amt}}
	cases := map[string]struct {
		j    Journal
		code string
	}{
		"no pair id": {Journal{Legs: legs}, CodeInvalid},
		"no legs":    {Journal{PairID: "p"}, CodeInvalid},
		"unbalanced": {Journal{PairID: "p", Legs: legs[:1]}, CodeUnbalanced},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := ReverseJournal(c.j)
			var de *Error
			if !errors.As(err, &de) || de.Code != c.code {
				t.Fatalf("err = %v; want %s", err, c.code)
			}
		})
	}
}
