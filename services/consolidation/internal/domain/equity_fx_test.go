package domain

import (
	"testing"
)

// Worked example for accountant sign-off (#512), IAS 21.44 with IAS 28: a EUR associate held 30 percent by a USD group.
//
//	cost 330,000.00 USD at the acquisition rate 1.10       -> 300,000.00 EUR opening carrying amount
//	profit 200,000 EUR, share 30 %   = 60,000.00 EUR at the average 1.12 = 67,200.00 USD
//	dividends declared 50,000 EUR, share = 15,000.00 EUR at the average 1.12 = 16,800.00 USD
//	closing carrying 300,000 + 60,000 - 15,000 = 345,000.00 EUR at the closing 1.15 = 396,750.00 USD
//	exchange difference 396,750 - (330,000 + 67,200 - 16,800) = 16,350.00 USD to the translation reserve
//	check: 300,000 x (1.15 - 1.10) + (60,000 - 15,000) x (1.15 - 1.12) = 15,000 + 1,350 = 16,350
func TestEquityMethod_RetranslatesAForeignAssociatesCarryingAmount(t *testing.T) {
	a := Associate{Currency: "USD", OwnershipPct: dec("30"), Foreign: &ForeignAssociate{UnitCurrency: "EUR", OpeningCarrying: dec("300000"),
		NetIncome: dec("200000"), Dividends: dec("15000"), OpeningRate: dec("1.10"), AverageRate: dec("1.12"), ClosingRate: dec("1.15")}}
	res, err := EquityMethod(a)
	if err != nil {
		t.Fatal(err)
	}
	if !res.ShareOfProfit.Equal(dec("67200")) || !res.ClosingCarrying.Equal(dec("396750")) || !res.ClosingCarryingUnit.Equal(dec("345000")) || !res.FXDifference.Equal(dec("16350")) {
		t.Fatalf("result = %+v", res)
	}
	got := map[string]string{}
	for _, e := range res.Entries {
		got[e.RuleCode+" "+e.Account] = e.Debit.Sub(e.Credit).String()
	}
	want := map[string]string{"EQ INVESTMENT_ASSOCIATE": "-16800", "EQ SHARE_OF_PROFIT_ASSOCIATES": "-67200", "EQ DIVIDEND_INCOME": "16800",
		"EQ-FX INVESTMENT_ASSOCIATE": "16350", "EQ-FX ASSOCIATE_TRANSLATION_RESERVE": "-16350"}
	// The two EQ legs on the investment (share +67,200 and dividends -16,800) are kept apart; the map holds the last one.
	for k, v := range want {
		if got[k] != v {
			t.Fatalf("%s = %s, want %s (entries %+v)", k, got[k], v, res.Entries)
		}
	}

	// A weaker euro gives a negative difference: closing 1.05 -> 345,000 x 1.05 = 362,250.00; 362,250 - 380,400 = -18,150.00.
	a.Foreign.ClosingRate = dec("1.05")
	res, err = EquityMethod(a)
	if err != nil || !res.FXDifference.Equal(dec("-18150")) || !res.ClosingCarrying.Equal(dec("362250")) {
		t.Fatalf("weaker euro = %+v, %v", res, err)
	}
}

// A loss beyond the carrying amount stops at zero in the associate's currency (IAS 28.38), and the zero carrying amount translates
// to zero whatever the rate.
func TestEquityMethod_ForeignLossStopsAtZero(t *testing.T) {
	a := Associate{Currency: "USD", OwnershipPct: dec("40"), Foreign: &ForeignAssociate{UnitCurrency: "EUR", OpeningCarrying: dec("10000"),
		NetIncome: dec("-50000"), OpeningRate: dec("1.10"), AverageRate: dec("1.12"), ClosingRate: dec("1.15")}}
	res, err := EquityMethod(a)
	if err != nil {
		t.Fatal(err)
	}
	// Share -20,000 EUR limited to -10,000; 10,000 EUR unrecognized = 11,200.00 USD at the average rate.
	if !res.ClosingCarrying.IsZero() || !res.ClosingCarryingUnit.IsZero() || !res.UnrecognizedLoss.Equal(dec("11200")) || !res.ShareOfProfit.Equal(dec("-11200")) {
		t.Fatalf("result = %+v", res)
	}
	// 0 - (11,000 - 11,200) = 200: the opening was at 1.10, the loss at 1.12.
	if !res.FXDifference.Equal(dec("200")) {
		t.Fatalf("fx difference = %s, want 200", res.FXDifference)
	}
	a.Foreign.AverageRate = dec("0")
	if _, err := EquityMethod(a); err == nil {
		t.Fatal("a zero rate was accepted")
	}
}

// A run from a collection retranslates from the register's carrying amount in the associate's currency when it has one.
func TestDeriveRun_ForeignAssociateStartsFromTheRegistersUnitAmount(t *testing.T) {
	in := DeriveInput{GroupCurrency: "USD", Period: "2027-03", Units: []Unit{subsidiary("A1", "EUR", "30")},
		Lines: []CollectLine{collected("A1", "3000", AccountBalanceSheet, RoleEquity, "-1000"), collected("A1", "4000", AccountIncome, "", "0")},
		Rates: map[string]CollectRate{"EUR": {Average: dec("1.12"), Closing: dec("1.15")}}, OpeningRates: map[string]decimalT{"EUR": dec("1.10")},
		Terms:                map[string]UnitTerms{"A1": {InvestmentCost: dec("330000"), Method: MethodEquity, HistoricalRate: dec("1.05")}},
		PreviousCarrying:     map[string]decimalT{"A1": dec("379500")},
		PreviousCarryingUnit: map[string]decimalT{"A1": dec("345000")}}
	d, err := DeriveRun(in)
	if err != nil {
		t.Fatal(err)
	}
	f := d.Associates[0].Associate.Foreign
	// 345,000 EUR at the opening 1.10 = 379,500.00 USD.
	if f == nil || !f.OpeningCarrying.Equal(dec("345000")) || !d.Associates[0].Associate.OpeningCarrying.Equal(dec("379500")) {
		t.Fatalf("associate = %+v foreign %+v", d.Associates[0].Associate, f)
	}
	// Without the unit amount, the group amount is taken back at the opening rate; without either, the cost at the acquisition rate.
	delete(in.PreviousCarryingUnit, "A1")
	d, _ = DeriveRun(in)
	if f := d.Associates[0].Associate.Foreign; !f.OpeningCarrying.Equal(dec("345000")) {
		t.Fatalf("from the group amount: %s", f.OpeningCarrying)
	}
	delete(in.PreviousCarrying, "A1")
	d, _ = DeriveRun(in)
	if f := d.Associates[0].Associate.Foreign; !f.OpeningCarrying.Round(2).Equal(dec("314285.71")) {
		t.Fatalf("from the cost: %s, want 330,000 / 1.05", f.OpeningCarrying.Round(2))
	}
	// From the cost the opening is translated at the acquisition rate, not the opening rate of the period, so the run opens at the
	// booked cost (330,000.00 USD) and the move from 1.05 to the closing rate goes to the translation reserve.
	if a := d.Associates[0].Associate; !a.OpeningCarrying.Equal(dec("330000")) || !a.Foreign.OpeningRate.Equal(dec("1.05")) {
		t.Fatalf("from the cost: opening %s at %s, want the cost 330000 at 1.05", a.OpeningCarrying, a.Foreign.OpeningRate)
	}
}

// The register line of a foreign associate adds up in the group currency: its opening and dividends are the amounts the entries
// were computed from, translated once, not the request's own roundings.
func TestComputeRun_ForeignAssociateRegisterLineAddsUp(t *testing.T) {
	in := RunInput{GroupID: "G", Period: "2027-03", Currency: "USD", Associates: []RunAssociate{{ID: "A1", Cost: dec("330000"), Associate: Associate{
		Currency: "USD", OwnershipPct: dec("33.3333"), OpeningCarrying: dec("330000.004"), DividendsReceived: dec("1.01"),
		Foreign: &ForeignAssociate{UnitCurrency: "EUR", OpeningCarrying: dec("314285.714285"), NetIncome: dec("123456.78"), Dividends: dec("3333.335"),
			OpeningRate: dec("1.05"), AverageRate: dec("1.1234"), ClosingRate: dec("1.1789")}}}}}
	res, err := ComputeRun(in)
	if err != nil {
		t.Fatal(err)
	}
	l := res.Investments[0]
	if got := l.OpeningCarrying.Add(l.ShareOfProfit).Sub(l.Dividends).Add(l.FXDifference); !got.Equal(l.ClosingCarrying) {
		t.Fatalf("opening %s + share %s - dividends %s + fx %s = %s, closing %s", l.OpeningCarrying, l.ShareOfProfit, l.Dividends, l.FXDifference, got, l.ClosingCarrying)
	}
}
