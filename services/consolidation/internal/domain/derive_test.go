package domain

import (
	"testing"
	"time"

	"github.com/shopspring/decimal"
)

func TestFiscalYearStart_FollowsTheUnitsOwnYear(t *testing.T) {
	for _, c := range []struct {
		period string
		start  int
		want   string
		prev   string
	}{
		{"2027-03", 0, "2027-01", "2026-12"},
		{"2027-03", 1, "2027-01", "2026-12"},
		{"2027-03", 4, "2026-04", "2026-03"},
		{"2027-04", 4, "2027-04", "2027-03"},
		{"2027-12", 7, "2027-07", "2027-06"},
		{"2027-01", 7, "2026-07", "2026-06"},
	} {
		if got := FiscalYearStart(c.period, c.start); got != c.want {
			t.Errorf("FiscalYearStart(%s, %d) = %s, want %s", c.period, c.start, got, c.want)
		}
		if got := PreviousFiscalYearEnd(c.period, c.start); got != c.prev {
			t.Errorf("PreviousFiscalYearEnd(%s, %d) = %s, want %s", c.period, c.start, got, c.prev)
		}
	}
	if err := (Unit{LegalEntityID: "x", LedgerID: "l", Currency: "EUR", FiscalYearStartMonth: 13}).Validate(); err == nil {
		t.Fatal("a fiscal year starting in month 13 was accepted")
	}
}

func TestValidateMapping_RolesMatchTheAccountType(t *testing.T) {
	ok := []AccountMapping{{MainAccount: "1000", ConsolidationAccount: "C1", Type: AccountBalanceSheet, Role: RoleAsset}, {MainAccount: "4000", ConsolidationAccount: "C4", Type: AccountIncome, Role: RoleICSales}}
	if err := ValidateMapping(ok); err != nil {
		t.Fatal(err)
	}
	for name, row := range map[string]AccountMapping{
		"unknown role":                  {MainAccount: "1", ConsolidationAccount: "C", Type: AccountBalanceSheet, Role: "plant"},
		"asset on an income account":    {MainAccount: "1", ConsolidationAccount: "C", Type: AccountIncome, Role: RoleAsset},
		"ic sales on the balance sheet": {MainAccount: "1", ConsolidationAccount: "C", Type: AccountBalanceSheet, Role: RoleICSales},
	} {
		if err := ValidateMapping([]AccountMapping{row}); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func collected(unit, account, typ, role, amount string) CollectLine {
	return CollectLine{UnitID: unit, MainAccount: account, ConsolidationAccount: account, Type: typ, Role: role, UnitAmount: dec(amount)}
}

func subsidiary(id, currency string, pct string) Unit {
	return Unit{ID: id, LegalEntityID: "le-" + id, LedgerID: "l-" + id, Currency: currency,
		Holdings: []Holding{{ID: "h-" + id, From: time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC), Percent: dec(pct)}}}
}

func TestRunUnits_ReadsNetAssetsIncomeAndTheICAmountsFromTheCollection(t *testing.T) {
	parent := Unit{ID: "P", LegalEntityID: "le-p", LedgerID: "l-p", Currency: "USD"}
	sub := subsidiary("S1", "EUR", "80")
	lines := []CollectLine{
		// Balance sheet: assets 1,120,000, liabilities 300,000, equity 700,000 before the year's profit of 120,000.
		collected("S1", "1000", AccountBalanceSheet, RoleAsset, "940000"),
		collected("S1", "1500", AccountBalanceSheet, RoleICReceivable, "180000"),
		collected("S1", "2000", AccountBalanceSheet, RoleLiability, "-300000"),
		collected("S1", "3000", AccountBalanceSheet, RoleEquity, "-500000"),
		collected("S1", "3100", AccountBalanceSheet, RoleEquity, "-200000"),
		// Income: revenue 500,000 of which 80,000 intercompany, cost 380,000 of which 60,000 intercompany.
		collected("S1", "4000", AccountIncome, "", "-420000"),
		collected("S1", "4100", AccountIncome, RoleICSales, "-80000"),
		collected("S1", "5000", AccountIncome, "", "320000"),
		collected("S1", "5100", AccountIncome, RoleICCost, "60000"),
		collected("P", "1000", AccountBalanceSheet, RoleAsset, "5"),
	}
	unsold := dec("0.25")
	payable := dec("190000")
	units, err := RunUnits(DeriveInput{
		GroupCurrency: "USD", Period: "2027-03", Units: []Unit{parent, sub}, Lines: lines,
		Rates:   map[string]CollectRate{"EUR": {Average: dec("1.08"), Closing: dec("1.10")}},
		Terms:   map[string]UnitTerms{"S1": {InvestmentCost: dec("900000"), ShareCapital: dec("300000"), PreAcquisitionRE: dec("100000"), HistoricalRate: dec("1.05")}},
		Opening: map[string]OpeningBasis{"S1": {NetAssets: dec("700000"), Rate: dec("1.02")}},
		IC:      map[string]ICOverride{"S1": {UnsoldShare: &unsold, PayableGroup: &payable}},
	})
	if err != nil || len(units) != 1 {
		t.Fatalf("units = %+v, %v; want only the subsidiary (the parent holds nobody's share)", units, err)
	}
	u := units[0]
	if !u.Pair.NetIncome.Equal(dec("120000")) || !u.ClosingNetAssets.Equal(dec("820000")) || !u.OpeningNetAssets.Equal(dec("700000")) {
		t.Fatalf("net income %s, closing %s, opening %s; want 120000, 820000, 700000 (profit is minus the income accounts, closing assets equity plus profit)", u.Pair.NetIncome, u.ClosingNetAssets, u.OpeningNetAssets)
	}
	p := u.Pair
	if !p.ICSales.Equal(dec("80000")) || !p.ICCost.Equal(dec("60000")) || !p.ICReceivable.Equal(dec("180000")) || !p.PayableGroup.Equal(dec("190000")) || !p.UnsoldShare.Equal(unsold) {
		t.Fatalf("IC amounts = %+v", p)
	}
	if !p.OwnershipPct.Equal(dec("80")) || !p.Rates.Closing.Equal(dec("1.10")) || !p.Rates.Average.Equal(dec("1.08")) || !p.Rates.Opening.Equal(dec("1.02")) || !p.Rates.Historical.Equal(dec("1.05")) {
		t.Fatalf("ownership and rates = %+v", p)
	}
	if !p.InvestmentCost.Equal(dec("900000")) || !p.ShareCapital.Equal(dec("300000")) || !p.PreAcqRE.Equal(dec("100000")) {
		t.Fatalf("acquisition figures = %+v", p)
	}
	// The derived units run through the engine as the request-fed ones do.
	if _, err := ComputeRun(RunInput{GroupID: "g1", Period: "2027-03", Currency: "USD", Units: units}); err != nil {
		t.Fatalf("compute: %v", err)
	}
}

func TestRunUnits_WithoutAPreviousYearEndOpeningIsClosingLessProfit(t *testing.T) {
	sub := subsidiary("S1", "USD", "100")
	units, err := RunUnits(DeriveInput{GroupCurrency: "USD", Period: "2027-03", Units: []Unit{sub},
		Lines: []CollectLine{collected("S1", "3000", AccountBalanceSheet, RoleEquity, "-500"), collected("S1", "4000", AccountIncome, "", "-40")},
		Terms: map[string]UnitTerms{"S1": {HistoricalRate: dec("1")}}})
	if err != nil {
		t.Fatal(err)
	}
	if !units[0].ClosingNetAssets.Equal(dec("540")) || !units[0].OpeningNetAssets.Equal(dec("500")) || !units[0].Pair.Rates.Opening.Equal(dec("1")) {
		t.Fatalf("closing %s opening %s opening rate %s; want 540, 500 and 1 for a unit in the group currency", units[0].ClosingNetAssets, units[0].OpeningNetAssets, units[0].Pair.Rates.Opening)
	}
}

func TestRunUnits_RefusesWhatItCannotDerive(t *testing.T) {
	base := func() DeriveInput {
		return DeriveInput{GroupCurrency: "USD", Period: "2027-03", Units: []Unit{subsidiary("S1", "EUR", "100")},
			Lines: []CollectLine{collected("S1", "3000", AccountBalanceSheet, RoleEquity, "-500")},
			Rates: map[string]CollectRate{"EUR": {Average: dec("1.1"), Closing: dec("1.1")}},
			Terms: map[string]UnitTerms{"S1": {HistoricalRate: dec("1")}}, OpeningRates: map[string]decimal.Decimal{"EUR": dec("1.05")}}
	}
	if _, err := RunUnits(base()); err != nil {
		t.Fatalf("the base case must work: %v", err)
	}
	for name, mutate := range map[string]func(*DeriveInput){
		"no terms": func(in *DeriveInput) { in.Terms = nil },
		"no equity": func(in *DeriveInput) {
			in.Lines = []CollectLine{collected("S1", "1000", AccountBalanceSheet, RoleAsset, "5")}
		},
		"no balances": func(in *DeriveInput) { in.Lines = nil },
		"no rate":     func(in *DeriveInput) { in.Rates = nil },
		"no opening":  func(in *DeriveInput) { in.OpeningRates = nil },
		"bad period":  func(in *DeriveInput) { in.Period = "March" },
		"no subsidiary": func(in *DeriveInput) {
			in.Units = []Unit{{ID: "P", LegalEntityID: "x", LedgerID: "l", Currency: "USD"}}
		},
		"divested": func(in *DeriveInput) { in.Units[0].Holdings[0].To = time.Date(2027, 2, 1, 0, 0, 0, 0, time.UTC) },
	} {
		in := base()
		mutate(&in)
		if _, err := RunUnits(in); err == nil {
			t.Errorf("%s: derived a run", name)
		}
	}
}

func TestUnitValidations_BalanceSheetRuleOverTheCollectedLines(t *testing.T) {
	lines := []CollectLine{
		collected("S1", "1000", AccountBalanceSheet, RoleAsset, "1000"),
		collected("S1", "2000", AccountBalanceSheet, RoleLiability, "-400"),
		collected("S1", "3000", AccountBalanceSheet, RoleEquity, "-500"),
		collected("S1", "4000", AccountIncome, "", "-100"),
		collected("S2", "1000", AccountBalanceSheet, RoleAsset, "1000"),
		collected("S2", "3000", AccountBalanceSheet, RoleEquity, "-987.50"),
	}
	rules := []ValidationRule{{ID: RuleBalanceSheet, Severity: SeverityError, Tolerance: dec("0.01")}}
	got, err := UnitValidations(lines, rules)
	if err != nil {
		t.Fatal(err)
	}
	if r := got["S1"][0]; r.Failed || !r.Difference.IsZero() {
		t.Fatalf("S1 = %+v; assets 1000 equal liabilities 400 plus equity 500 plus profit 100", r)
	}
	if r := got["S2"][0]; !r.Failed || !r.Difference.Equal(dec("12.50")) {
		t.Fatalf("S2 = %+v; want the 12.50 difference to fail", r)
	}
}
