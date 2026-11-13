package domain

import (
	"strings"
	"testing"
	"time"
)

// A subsidiary whose terms name its pair accounts has the E2 amounts read from the collected ledgers: its receivable from the parent
// from its own account and the parent's payable to it from the parent's account, not from every ic_receivable account and not from
// the request.
func TestDeriveRun_ReadsTheICPairAmountsFromTheLedgers(t *testing.T) {
	parent := Unit{ID: "P", LegalEntityID: "le-p", LedgerID: "l-p", Currency: "USD"}
	sub := Unit{ID: "S1", LegalEntityID: "le-s1", LedgerID: "l-s1", Currency: "EUR",
		Holdings: []Holding{{ID: "h1", HolderUnitID: "P", From: time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC), Percent: dec("80")}}}
	other := Unit{ID: "S2", LegalEntityID: "le-s2", LedgerID: "l-s2", Currency: "USD"}
	payable := func(unit, account, amount string) CollectLine {
		l := collected(unit, account, AccountBalanceSheet, RoleICPayable, amount)
		l.GroupAmount = dec(amount)
		return l
	}
	lines := []CollectLine{
		collected("S1", "3000", AccountBalanceSheet, RoleEquity, "-1000"),
		collected("S1", "4000", AccountIncome, "", "-200"),
		collected("S1", "1310", AccountBalanceSheet, RoleICReceivable, "450"), // due from the parent
		collected("S1", "1320", AccountBalanceSheet, RoleICReceivable, "75"),  // due from S2, not part of the pair
		payable("P", "2310", "-540"),                                          // the parent's payable to S1, USD
		payable("P", "2320", "-90"),                                           // the parent's payable to S2
		payable("S2", "2310", "-999"),                                         // S2 is not S1's holder
	}
	in := DeriveInput{GroupCurrency: "USD", Period: "2027-03", Units: []Unit{parent, sub, other}, Lines: lines,
		Rates: map[string]CollectRate{"EUR": {Average: dec("1.15"), Closing: dec("1.2")}}, OpeningRates: map[string]decimalT{"EUR": dec("1.1")},
		Terms: map[string]UnitTerms{"S1": {InvestmentCost: dec("800"), ShareCapital: dec("1000"), HistoricalRate: dec("1"), ICReceivableAccount: "1310", ParentPayableAccount: "2310"}}}
	d, err := DeriveRun(in)
	if err != nil || len(d.Units) != 1 {
		t.Fatalf("derive = %+v, %v", d, err)
	}
	p := d.Units[0].Pair
	if !p.ICReceivable.Equal(dec("450")) || !p.PayableGroup.Equal(dec("540")) {
		t.Fatalf("receivable %s payable %s, want 450 EUR and 540 USD", p.ICReceivable, p.PayableGroup)
	}
	res, err := ComputeRun(RunInput{GroupID: "g1", Period: "2027-03", Currency: "USD", Units: d.Units})
	if err != nil {
		t.Fatal(err)
	}
	// E2: payable 540.00 against the receivable 450 EUR at the closing 1.2, also 540.00: no IC difference.
	for _, e := range res.Entries {
		if e.RuleCode == RuleE2 && e.Account != AcctICPayable && e.Account != AcctICReceivable {
			t.Fatalf("an IC difference was booked: %+v", e)
		}
	}

	// Without the pair accounts every ic_receivable account counts, and the payable stays zero (the old behaviour).
	in.Terms["S1"] = UnitTerms{InvestmentCost: dec("800"), ShareCapital: dec("1000"), HistoricalRate: dec("1")}
	d, _ = DeriveRun(in)
	if p := d.Units[0].Pair; !p.ICReceivable.Equal(dec("525")) || !p.PayableGroup.IsZero() {
		t.Fatalf("without pair accounts: receivable %s payable %s", p.ICReceivable, p.PayableGroup)
	}
}

func TestDeriveRun_PairAccountsRefuseTheRequestAndOddBalances(t *testing.T) {
	parent := Unit{ID: "P", LegalEntityID: "le-p", LedgerID: "l-p", Currency: "USD"}
	sub := Unit{ID: "S1", LegalEntityID: "le-s1", LedgerID: "l-s1", Currency: "USD",
		Holdings: []Holding{{ID: "h1", HolderUnitID: "P", From: time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC), Percent: dec("100")}}}
	base := func() DeriveInput {
		return DeriveInput{GroupCurrency: "USD", Period: "2027-03", Units: []Unit{parent, sub},
			Lines: []CollectLine{collected("S1", "3000", AccountBalanceSheet, RoleEquity, "-1000"), collected("S1", "1310", AccountBalanceSheet, RoleICReceivable, "10")},
			Terms: map[string]UnitTerms{"S1": {HistoricalRate: dec("1"), ICReceivableAccount: "1310", ParentPayableAccount: "2310"}}}
	}
	in := base()
	v := dec("5")
	in.IC = map[string]ICOverride{"S1": {PayableGroup: &v}}
	if _, err := DeriveRun(in); err == nil || !strings.Contains(err.Error(), "leave payableGroup out") {
		t.Fatalf("a payable in the request was accepted: %v", err)
	}
	in = base()
	in.IC = map[string]ICOverride{"S1": {ICReceivable: &v}}
	if _, err := DeriveRun(in); err == nil || !strings.Contains(err.Error(), "leave icReceivable out") {
		t.Fatalf("a receivable in the request was accepted: %v", err)
	}
	in = base()
	in.Lines[1].UnitAmount = dec("-10")
	if _, err := DeriveRun(in); err == nil || !strings.Contains(err.Error(), "credit balance") {
		t.Fatalf("a credit receivable was accepted: %v", err)
	}
	in = base()
	in.Units[0].Currency = "EUR"
	in.Lines = append(in.Lines, collected("P", "2310", AccountBalanceSheet, RoleICPayable, "-10"))
	if _, err := DeriveRun(in); err == nil || !strings.Contains(err.Error(), "keeps its ledger in EUR") {
		t.Fatalf("a payable outside the group currency was accepted: %v", err)
	}
	// The unsold share still comes from the request.
	in = base()
	in.IC = map[string]ICOverride{"S1": {UnsoldShare: &v}}
	if _, err := DeriveRun(in); err != nil && !strings.Contains(err.Error(), "unsold") {
		t.Fatalf("an unsold share override was refused: %v", err)
	}
}
