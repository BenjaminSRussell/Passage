package domain

import (
	"testing"
	"time"
)

// TestCreditAmounts_ReturnToStockFourAtHundred is AC-30.5: 4 × 100.00 USD
// RETURN_TO_STOCK at 15% restocking credits 340.00 after a 60.00 restocking
// charge, posting Dr 4000 400 / Cr 4200 60 / Cr 1200 340.
func TestCreditAmounts_ReturnToStockFourAtHundred(t *testing.T) {
	got, err := CreditAmounts(ReturnCredit{
		Disposition: DispositionReturnToStock,
		Quantity:    dec("4"),
		UnitPrice:   dec("100.00"),
		Currency:    "USD",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !got.Gross.Equal(dec("400.00")) || !got.Restocking.Equal(dec("60.00")) || !got.Net.Equal(dec("340.00")) {
		t.Fatalf("amounts %+v", got)
	}
	want := []GLLine{
		{Account: AccountSalesReturns, Debit: dec("400.00")},
		{Account: AccountRestocking, Credit: dec("60.00")},
		{Account: AccountReceivable, Credit: dec("340.00")},
	}
	lines := got.Postings()
	if len(lines) != len(want) {
		t.Fatalf("postings %v", lines)
	}
	for i, line := range lines {
		if line.Account != want[i].Account || !line.Debit.Equal(want[i].Debit) || !line.Credit.Equal(want[i].Credit) {
			t.Fatalf("posting %d = %+v, want %+v", i, line, want[i])
		}
	}
}

func TestCreditAmounts_RestockingFloorAndCreditOnly(t *testing.T) {
	floor, err := CreditAmounts(ReturnCredit{Disposition: DispositionReturnToStock, Quantity: dec("1"), UnitPrice: dec("100.00"), Currency: "USD"})
	if err != nil || !floor.Restocking.Equal(dec("25.00")) || !floor.Net.Equal(dec("75.00")) {
		t.Fatalf("USD floor: %+v %v", floor, err)
	}
	eur, err := CreditAmounts(ReturnCredit{Disposition: DispositionReturnToStock, Quantity: dec("1"), UnitPrice: dec("100.00"), Currency: "EUR"})
	if err != nil || !eur.Restocking.Equal(dec("20.00")) {
		t.Fatalf("EUR floor: %+v %v", eur, err)
	}
	only, err := CreditAmounts(ReturnCredit{Disposition: DispositionCreditOnly, Quantity: dec("4"), UnitPrice: dec("100.00"), Currency: "USD"})
	if err != nil || !only.Restocking.IsZero() || !only.Net.Equal(dec("400.00")) {
		t.Fatalf("CREDIT_ONLY: %+v %v", only, err)
	}
}

func TestRMAAdvance_AuthorizeReceiveInspectDispositionCredit(t *testing.T) {
	now := time.Date(2027, 3, 1, 0, 0, 0, 0, time.UTC)
	status, expires, err := AdvanceRMA(RMARequested, RMAAuthorize, now)
	if err != nil || status != RMAAuthorized || expires == nil || expires.Format("2006-01-02") != "2027-03-31" {
		t.Fatalf("authorize: %s %v %v", status, expires, err)
	}
	for _, step := range []struct {
		from, action, want string
	}{
		{RMAAuthorized, RMAReceive, RMAReceived},
		{RMAReceived, RMAInspect, RMAInspected},
		{RMAInspected, RMADisposition, RMADispositioned},
		{RMADispositioned, RMACreditStart, RMACredited},
		{RMACredited, RMACredit, RMAClosed},
	} {
		got, _, err := AdvanceRMA(step.from, step.action, now)
		if err != nil || got != step.want {
			t.Fatalf("%s %s: %s %v", step.from, step.action, got, err)
		}
	}
	if _, _, err := AdvanceRMA(RMARequested, RMAReceive, now); err == nil {
		t.Fatal("receive from REQUESTED must fail")
	}
}
