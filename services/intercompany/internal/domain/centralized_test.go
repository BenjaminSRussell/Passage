package domain

import (
	"testing"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/money"
)

// centralMatrix holds the rows for P (paying) and S (owning) in both orders.
var centralMatrix = Matrix{
	{EntityA: "P", EntityB: "S", DueFrom: "P_DUE_FROM_S", DueTo: "S_DUE_TO_P"},
	{EntityA: "S", EntityB: "P", DueFrom: "S_DUE_FROM_P", DueTo: "P_DUE_TO_S"},
}

// TestCentralPaymentJournal_AC40_10 is AC-40.10: P pays S's vendor invoice of
// EUR 10,000.00. P: Dr Due from S / Cr Bank; S: Dr Vendor / Cr Due to P.
func TestCentralPaymentJournal_AC40_10(t *testing.T) {
	amt := money.Must("10000.00", "EUR")
	s := CentralizedPaymentSetup{PayingEntity: "P", OwningEntity: "S", PaymentMethod: "SEPA", Direction: DirectionPay}
	j, err := CentralPaymentJournal(centralMatrix, "pair-9", s, amt, "BANK_P", "VENDOR_AP")
	if err != nil {
		t.Fatal(err)
	}
	want := []Leg{
		{Entity: "P", Account: "P_DUE_FROM_S", Side: Debit, Amount: amt},
		{Entity: "P", Account: "BANK_P", Side: Credit, Amount: amt},
		{Entity: "S", Account: "VENDOR_AP", Side: Debit, Amount: amt},
		{Entity: "S", Account: "S_DUE_TO_P", Side: Credit, Amount: amt},
	}
	if err := j.Balanced(); err != nil {
		t.Fatal(err)
	}
	if len(j.Legs) != len(want) || j.PairID != "pair-9" {
		t.Fatalf("journal = %+v", j)
	}
	for i, w := range want {
		l := j.Legs[i]
		if l.Entity != w.Entity || l.Account != w.Account || l.Side != w.Side || !l.Amount.Value.Equal(w.Amount.Value) {
			t.Fatalf("leg %d = %+v, want %+v", i, l, w)
		}
	}
}

// TestCentralPaymentJournal_ReceiveMirrorsPay checks that a receipt for the owning
// entity runs the reverse direction and still balances on both sides.
func TestCentralPaymentJournal_ReceiveMirrorsPay(t *testing.T) {
	amt := money.Must("2500.00", "EUR")
	s := CentralizedPaymentSetup{PayingEntity: "P", OwningEntity: "S", PaymentMethod: "Lockbox", Direction: DirectionReceive}
	j, err := CentralPaymentJournal(centralMatrix, "pair-10", s, amt, "BANK_P", "CUSTOMER_AR")
	if err != nil {
		t.Fatal(err)
	}
	if j.Legs[0].Entity != "P" || j.Legs[0].Account != "BANK_P" || j.Legs[0].Side != Debit {
		t.Fatalf("paying entity leg = %+v", j.Legs[0])
	}
	if j.Legs[1].Account != "P_DUE_TO_S" || j.Legs[2].Account != "S_DUE_FROM_P" || j.Legs[3].Account != "CUSTOMER_AR" {
		t.Fatalf("accounts = %+v", j.Legs)
	}
	if err := j.Balanced(); err != nil {
		t.Fatal(err)
	}
}

func TestCentralPaymentJournal_Rejects(t *testing.T) {
	amt := money.Must("10.00", "EUR")
	good := CentralizedPaymentSetup{PayingEntity: "P", OwningEntity: "S", PaymentMethod: "SEPA", Direction: DirectionPay}
	tests := []struct {
		name  string
		setup CentralizedPaymentSetup
		amt   money.Amount
		code  string
	}{
		{"same entity", CentralizedPaymentSetup{PayingEntity: "P", OwningEntity: "P", PaymentMethod: "SEPA", Direction: DirectionPay}, amt, CodeInvalid},
		{"bad direction", CentralizedPaymentSetup{PayingEntity: "P", OwningEntity: "S", PaymentMethod: "SEPA", Direction: "Both"}, amt, CodeInvalid},
		{"zero amount", good, money.Must("0", "EUR"), CodeInvalid},
		{"amount that rounds to zero", good, money.Amount{Value: decimal.RequireFromString("0.004"), Currency: "EUR"}, CodeInvalid},
		{"no payment method", CentralizedPaymentSetup{PayingEntity: "P", OwningEntity: "S", Direction: DirectionPay}, amt, CodeInvalid},
		{"no matrix row", CentralizedPaymentSetup{PayingEntity: "S", OwningEntity: "X", PaymentMethod: "SEPA", Direction: DirectionPay}, amt, CodeNoMatrix},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := CentralPaymentJournal(centralMatrix, "pair", tc.setup, tc.amt, "BANK", "AP")
			de, ok := err.(*Error)
			if !ok || de.Code != tc.code {
				t.Fatalf("want %s, got %v", tc.code, err)
			}
		})
	}
}
