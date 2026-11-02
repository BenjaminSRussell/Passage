package domain

import (
	"errors"
	"strings"
	"testing"

	"github.com/shopspring/decimal"
)

func dec(s string) decimal.Decimal { return decimal.RequireFromString(s) }

func psUnit(id string) RunUnit {
	return RunUnit{
		ID: id,
		Pair: Pair{
			Currency: "USD", OwnershipPct: dec("80"),
			Rates:   Rates{Closing: dec("1.10"), Average: dec("1.08"), Opening: dec("1.05"), Historical: dec("1.05")},
			ICSales: dec("100000"), ICCost: dec("70000"), UnsoldShare: dec("0.4"), ICReceivable: dec("100000"),
			PayableGroup: dec("108500"), ShareCapital: dec("500000"), PreAcqRE: dec("200000"),
			InvestmentCost: dec("620000"), NetIncome: dec("150000"), SellerIsSubsidiary: true,
		},
		OpeningNetAssets: dec("700000"), ClosingNetAssets: dec("850000"),
	}
}

func psRun(units ...RunUnit) RunInput {
	return RunInput{GroupID: "G1", Period: "2026-06", Currency: "USD", Units: units}
}

func lineOf(res RunResult, statement, code string) decimal.Decimal {
	for _, l := range res.Statements {
		if l.Statement == statement && l.Code == code {
			return l.Amount
		}
	}
	return decimal.NewFromInt(-1)
}

func TestComputeRun_TranslationEliminationsAndStatements(t *testing.T) {
	res, err := ComputeRun(psRun(psUnit("S1")))
	if err != nil {
		t.Fatal(err)
	}
	if !res.CTA.Equal(dec("38000")) {
		t.Errorf("CTA = %s, want 38000 (AC-40.6)", res.CTA)
	}
	if len(res.Balances) != 6 || len(res.Entries) != 17 {
		t.Fatalf("balances=%d entries=%d", len(res.Balances), len(res.Entries))
	}
	entries := make([]Entry, 0, len(res.Entries))
	for _, e := range res.Entries {
		entries = append(entries, e.Entry)
	}
	if err := CheckBalanced(entries); err != nil {
		t.Errorf("entries: %v", err)
	}
	for statement, want := range map[string]map[string]string{
		StatementNetAssets: {LineOpeningNetAssets: "735000", LineUnitNetIncome: "162000", LineCTA: "38000", LineClosingNetAssets: "935000"},
		StatementIncome: {LineUnitNetIncome: "162000", LineUnrealizedProfitE3: "-12960", LineConsolidatedIncome: "149040",
			LineNCIProfit: "29808", LineOwnersProfit: "119232"},
	} {
		for code, amount := range want {
			if got := lineOf(res, statement, code); !got.Equal(dec(amount)) {
				t.Errorf("%s %s = %s, want %s", statement, code, got, amount)
			}
		}
	}
}

func TestComputeRun_SumsUnits(t *testing.T) {
	res, err := ComputeRun(psRun(psUnit("S1"), psUnit("S2")))
	if err != nil {
		t.Fatal(err)
	}
	if !res.CTA.Equal(dec("76000")) || !lineOf(res, StatementIncome, LineConsolidatedIncome).Equal(dec("298080")) {
		t.Errorf("two units: cta=%s income=%s", res.CTA, lineOf(res, StatementIncome, LineConsolidatedIncome))
	}
}

func TestComputeRun_Rejects(t *testing.T) {
	foreign := psUnit("S2")
	foreign.Pair.Currency = "EUR"
	noOpening := psUnit("S3")
	noOpening.Pair.Rates.Opening = decimal.Zero
	bargain := psUnit("S4")
	bargain.Pair.InvestmentCost = dec("1")
	cases := map[string]RunInput{
		"no units":        psRun(),
		"duplicate unit":  psRun(psUnit("S1"), psUnit("S1")),
		"foreign unit":    psRun(foreign),
		"bad period":      {GroupID: "G1", Period: "2026-13", Currency: "USD", Units: []RunUnit{psUnit("S1")}},
		"bad group":       {GroupID: "", Period: "2026-06", Currency: "USD", Units: []RunUnit{psUnit("S1")}},
		"missing opening": psRun(noOpening),
		"bargain":         psRun(bargain),
	}
	for name, in := range cases {
		_, err := ComputeRun(in)
		var de *Error
		if !errors.As(err, &de) {
			t.Errorf("%s: err = %v, want a domain error", name, err)
		}
	}
	_, err := ComputeRun(psRun(bargain))
	var de *Error
	if !errors.As(err, &de) || de.Code != CodeBargainPurchase || de.Detail[:7] != "unit S4" {
		t.Errorf("bargain error = %v, want CNS-0920 naming the unit", err)
	}
}

func TestRunInput_CanonicalIgnoresOrderAndDecimalSpelling(t *testing.T) {
	a, b := psUnit("S1"), psUnit("S2")
	respelled := psUnit("S1")
	respelled.Pair.ICSales = dec("100000.000")
	respelled.Pair.OwnershipPct = dec("80.0")
	first, err := psRun(a, b).Canonical()
	if err != nil {
		t.Fatal(err)
	}
	second, err := psRun(b, respelled).Canonical()
	if err != nil {
		t.Fatal(err)
	}
	if string(first) != string(second) {
		t.Fatalf("canonical forms differ:\n%s\n%s", first, second)
	}
	changed := psUnit("S1")
	changed.Pair.NetIncome = dec("150000.01")
	third, _ := psRun(changed, b).Canonical()
	if string(first) == string(third) || !strings.Contains(string(first), RulesVersion) {
		t.Fatalf("a changed amount must change the form, and the rules version must be in it: %s", third)
	}
}
