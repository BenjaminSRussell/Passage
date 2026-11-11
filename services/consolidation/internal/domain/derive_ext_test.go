package domain

import (
	"strings"
	"testing"

	"github.com/shopspring/decimal"
)

type decimalT = decimal.Decimal

func TestDeriveRun_DividendsGiveE6AndComeOffTheNetAssets(t *testing.T) {
	parent := Unit{ID: "P", LegalEntityID: "le-p", LedgerID: "l-p", Currency: "USD"}
	sub := subsidiary("S1", "EUR", "80")
	lines := []CollectLine{
		collected("S1", "3000", AccountBalanceSheet, RoleEquity, "-1000"),
		collected("S1", "3900", AccountBalanceSheet, RoleDividendPaid, "100"), // 100 EUR declared, a debit against equity
		collected("S1", "4000", AccountIncome, "", "-200"),
	}
	d, err := DeriveRun(DeriveInput{GroupCurrency: "USD", Period: "2027-03", Units: []Unit{parent, sub}, Lines: lines,
		Rates:        map[string]CollectRate{"EUR": {Average: dec("1.1"), Closing: dec("1.2")}},
		OpeningRates: map[string]decimalT{"EUR": dec("1.0")},
		Terms:        map[string]UnitTerms{"S1": {InvestmentCost: dec("800"), ShareCapital: dec("1000"), HistoricalRate: dec("1")}}})
	if err != nil || len(d.Units) != 1 {
		t.Fatalf("derive = %+v, %v", d, err)
	}
	u := d.Units[0]
	// Net assets: equity 1000 less the dividend 100 plus the profit 200.
	if !u.Pair.Dividend.Equal(dec("100")) || !u.ClosingNetAssets.Equal(dec("1100")) {
		t.Fatalf("dividend %s closing %s, want 100 and 1100", u.Pair.Dividend, u.ClosingNetAssets)
	}
	res, err := ComputeRun(RunInput{GroupID: "g1", Period: "2027-03", Currency: "USD", Units: d.Units})
	if err != nil {
		t.Fatal(err)
	}
	// E6 at the average rate: 100 EUR is 110.00 USD; the parent's 80 percent is 88.00 and the NCI's 22.00.
	got := map[string]string{}
	for _, e := range res.Entries {
		if e.RuleCode == RuleE6 {
			got[e.Account+"D"] = e.Debit.StringFixed(2)
			got[e.Account+"C"] = e.Credit.StringFixed(2)
		}
	}
	if got["DIVIDEND_INCOMED"] != "88.00" || got["NCI_EQUITYD"] != "22.00" || got["DIVIDENDS_DECLAREDC"] != "110.00" {
		t.Fatalf("E6 = %v", got)
	}
	if err := CheckBalanced(entriesOf(res)); err != nil {
		t.Fatalf("the run does not balance: %v", err)
	}
	// With no dividend there is no E6.
	noDiv := lines[:1:1]
	noDiv = append(noDiv, lines[2])
	d2, _ := DeriveRun(DeriveInput{GroupCurrency: "USD", Period: "2027-03", Units: []Unit{parent, sub}, Lines: noDiv, Rates: map[string]CollectRate{"EUR": {Average: dec("1.1"), Closing: dec("1.2")}},
		OpeningRates: map[string]decimalT{"EUR": dec("1.0")}, Terms: map[string]UnitTerms{"S1": {HistoricalRate: dec("1")}}})
	r2, _ := ComputeRun(RunInput{GroupID: "g1", Period: "2027-03", Currency: "USD", Units: d2.Units})
	for _, e := range r2.Entries {
		if e.RuleCode == RuleE6 {
			t.Fatalf("E6 without a dividend: %+v", e)
		}
	}
}

func entriesOf(r RunResult) []Entry {
	out := make([]Entry, 0, len(r.Entries))
	for _, e := range r.Entries {
		out = append(out, e.Entry)
	}
	return out
}

func TestDeriveRun_LayersReplaceTheUnsoldShareInE3AndWarnOnTheMarginFallback(t *testing.T) {
	sub := subsidiary("S1", "USD", "100")
	lines := []CollectLine{
		collected("S1", "3000", AccountBalanceSheet, RoleEquity, "-1000"),
		collected("S1", "4100", AccountIncome, RoleICSales, "-1000"),
		collected("S1", "5100", AccountIncome, RoleICCost, "600"),
	}
	unsold := dec("0.5")
	layers := []ICLayer{
		{ID: "r1", Quantity: dec("10"), LegalUnitCost: dec("50"), GroupUnitCost: dec("30"), GroupCostKnown: true}, // 10 x 20 = 200
		{ID: "r2", Quantity: dec("4"), LegalUnitCost: dec("25")},                                                  // 4 x 25 x 40 percent margin = 40
	}
	in := DeriveInput{GroupCurrency: "USD", Period: "2027-03", Units: []Unit{sub}, Lines: lines, Terms: map[string]UnitTerms{"S1": {HistoricalRate: dec("1")}},
		IC: map[string]ICOverride{"S1": {UnsoldShare: &unsold}}, Layers: map[string][]ICLayer{"S1": layers}}
	d, err := DeriveRun(in)
	if err != nil {
		t.Fatal(err)
	}
	if p := d.Units[0].Pair.UnrealizedProfit; p == nil || !p.Equal(dec("240")) {
		t.Fatalf("unrealized profit = %v, want 240 (200 on group cost, 40 on the seller's 40 percent margin)", p)
	}
	if len(d.Warnings) != 1 || !strings.Contains(d.Warnings[0], "CNS-0908") {
		t.Fatalf("warnings = %v", d.Warnings)
	}
	res, err := ComputeRun(RunInput{GroupID: "g1", Period: "2027-03", Currency: "USD", Units: d.Units})
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range res.Entries {
		if e.RuleCode == RuleE3 && e.Account == AcctCOGS && !e.Debit.Equal(dec("240")) {
			t.Fatalf("E3 COGS = %s, want the layers' 240 and not the unsold share's 200", e.Debit)
		}
	}
	// Without layers the unsold share applies as before.
	in.Layers = nil
	d, _ = DeriveRun(in)
	if d.Units[0].Pair.UnrealizedProfit != nil {
		t.Fatal("no layers must leave the unsold share in charge")
	}
}

func TestDeriveRun_AnAssociateGoesThroughTheEquityMethodAndTheRegister(t *testing.T) {
	assoc := subsidiary("A1", "EUR", "30")
	lines := []CollectLine{
		collected("A1", "3000", AccountBalanceSheet, RoleEquity, "-1000"),
		collected("A1", "3900", AccountBalanceSheet, RoleDividendPaid, "100"),
		collected("A1", "4000", AccountIncome, "", "-2000"), // profit 2000 EUR
	}
	in := DeriveInput{GroupCurrency: "USD", Period: "2027-03", Units: []Unit{assoc}, Lines: lines,
		Rates: map[string]CollectRate{"EUR": {Average: dec("1.1"), Closing: dec("1.2")}}, OpeningRates: map[string]decimalT{"EUR": dec("1.0")},
		Terms: map[string]UnitTerms{"A1": {InvestmentCost: dec("500"), Method: MethodEquity, HistoricalRate: dec("1")}}}
	d, err := DeriveRun(in)
	if err != nil || len(d.Units) != 0 || len(d.Associates) != 1 {
		t.Fatalf("derive = %+v, %v", d, err)
	}
	a := d.Associates[0].Associate
	// Profit 2000 EUR is 2200.00 USD at the average rate; the group's 30 percent of the 100 EUR dividend is 30 EUR, 33.00 USD.
	if !a.NetIncome.Equal(dec("2200")) || !a.DividendsReceived.Equal(dec("33")) || !a.OpeningCarrying.Equal(dec("500")) {
		t.Fatalf("associate = %+v", a)
	}
	res, err := ComputeRun(RunInput{GroupID: "g1", Period: "2027-03", Currency: "USD", Associates: d.Associates})
	if err != nil {
		t.Fatal(err)
	}
	// Share of profit 30 percent of 2200 = 660; carrying 500 + 660 - 33 = 1127.
	if len(res.Investments) != 1 || res.Investments[0].Method != MethodEquity || !res.Investments[0].ShareOfProfit.Equal(dec("660")) || !res.Investments[0].ClosingCarrying.Equal(dec("1127")) {
		t.Fatalf("register = %+v", res.Investments)
	}
	var share string
	for _, s := range res.Statements {
		if s.Code == LineShareOfAssociates {
			share = s.Amount.String()
		}
		if s.Code == LineConsolidatedIncome && !s.Amount.Equal(dec("660")) {
			t.Errorf("consolidated income = %s, want the share of the associate's profit, 660", s.Amount)
		}
	}
	if share != "660" {
		t.Errorf("share of associates line = %q", share)
	}
	// The previous year's carrying amount is the opening one; more than 50 percent is refused.
	in.PreviousCarrying = map[string]decimalT{"A1": dec("900")}
	d, _ = DeriveRun(in)
	if !d.Associates[0].Associate.OpeningCarrying.Equal(dec("900")) {
		t.Errorf("opening carrying = %s, want the previous year's 900", d.Associates[0].Associate.OpeningCarrying)
	}
	in.Units = []Unit{subsidiary("A1", "EUR", "60")}
	if _, err := DeriveRun(in); err == nil || !strings.Contains(err.Error(), "50 percent") {
		t.Fatalf("a controlled equity unit: %v", err)
	}
}
