package domain

import (
	"errors"
	"testing"

	"github.com/shopspring/decimal"
)

func d(s string) decimal.Decimal { return decimal.RequireFromString(s) }

// psExample is the FR-40.9 worked example: P (USD) owns 80 % of S (EUR).
func psExample() Pair {
	return Pair{
		Currency:           "USD",
		OwnershipPct:       d("80"),
		Rates:              Rates{Closing: d("1.10"), Average: d("1.08"), Opening: d("1.05"), Historical: d("1.05")},
		ICSales:            d("100000"),
		ICCost:             d("70000"),
		UnsoldShare:        d("0.4"),
		ICReceivable:       d("100000"),
		PayableGroup:       d("108500"),
		ShareCapital:       d("500000"),
		PreAcqRE:           d("200000"),
		InvestmentCost:     d("620000"),
		NetIncome:          d("150000"),
		SellerIsSubsidiary: true,
	}
}

type want struct {
	rule, account string
	debit, credit string
}

// TestEliminate_PSWorkedExample checks every line of E1–E5 against the amounts in FR-40.9.
func TestEliminate_PSWorkedExample(t *testing.T) {
	entries, err := Eliminate(psExample())
	if err != nil {
		t.Fatal(err)
	}
	wants := []want{
		{RuleE1, AcctICRevenue, "108000.00", "0.00"},
		{RuleE1, AcctICCOGS, "0.00", "108000.00"},
		{RuleE2, AcctICPayable, "108500.00", "0.00"},
		{RuleE2, AcctICReceivable, "0.00", "110000.00"},
		{RuleE2, AcctFXGainLoss, "1500.00", "0.00"},
		{RuleE3, AcctCOGS, "12960.00", "0.00"},
		{RuleE3, AcctInventory, "0.00", "12960.00"},
		{RuleE3, AcctNCIEquity, "2592.00", "0.00"},
		{RuleE3, AcctNCIProfit, "0.00", "2592.00"},
		{RuleE4, AcctShareCapital, "525000.00", "0.00"},
		{RuleE4, AcctPreAcqRE, "210000.00", "0.00"},
		{RuleE4, AcctGoodwill, "33523.81", "0.00"},
		{RuleE4, AcctCTA, "0.00", "1523.81"},
		{RuleE4, AcctInvestment, "0.00", "620000.00"},
		{RuleE4, AcctNCIEquity, "0.00", "147000.00"},
		{RuleE5, AcctNCIProfit, "32400.00", "0.00"},
		{RuleE5, AcctNCIEquity, "0.00", "32400.00"},
	}
	if len(entries) != len(wants) {
		t.Fatalf("got %d entries; want %d: %+v", len(entries), len(wants), entries)
	}
	for i, w := range wants {
		e := entries[i]
		if e.RuleCode != w.rule || e.Account != w.account || e.Debit.StringFixed(2) != w.debit || e.Credit.StringFixed(2) != w.credit {
			t.Fatalf("entry %d = %s %s Dr %s Cr %s; want %s %s Dr %s Cr %s", i,
				e.RuleCode, e.Account, e.Debit.StringFixed(2), e.Credit.StringFixed(2), w.rule, w.account, w.debit, w.credit)
		}
	}
	dr, cr := Totals(entries)
	if dr.StringFixed(2) != "1034475.81" || cr.StringFixed(2) != "1034475.81" {
		t.Fatalf("totals Dr %s Cr %s; want 1034475.81 both", dr.StringFixed(2), cr.StringFixed(2))
	}
}

func TestEliminate_DownstreamSaleLeavesNCIUntouched(t *testing.T) {
	p := psExample()
	p.SellerIsSubsidiary = false
	entries, err := Eliminate(p)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if e.RuleCode == RuleE3 && (e.Account == AcctNCIEquity || e.Account == AcctNCIProfit) {
			t.Fatalf("downstream profit touched the NCI: %+v", e)
		}
	}
	if err := CheckBalanced(entries); err != nil {
		t.Fatal(err)
	}
}

func TestEliminate_LossInInventoryReversesSides(t *testing.T) {
	p := psExample()
	p.ICCost = d("130000") // a 30,000 loss on the sale
	entries, err := Eliminate(p)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if e.RuleCode == RuleE3 && e.Account == AcctCOGS && (!e.Debit.IsZero() || e.Credit.IsZero()) {
			t.Fatalf("loss should credit COGS: %+v", e)
		}
	}
	if err := CheckBalanced(entries); err != nil {
		t.Fatal(err)
	}
}

func TestEliminate_E2NetInvestmentGoesToCTA(t *testing.T) {
	p := psExample()
	p.ICNetInvestment = true
	entries, err := Eliminate(p)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if e.RuleCode == RuleE2 && e.Account == AcctCTA && e.Debit.StringFixed(2) == "1500.00" {
			return
		}
	}
	t.Fatalf("net-investment IC difference not posted to CTA: %+v", entries)
}

func TestEliminate_E2ConfiguredFXAccount(t *testing.T) {
	p := psExample()
	p.FXGainLossAccount = "FX_REALIZED"
	entries, err := Eliminate(p)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if e.RuleCode == RuleE2 && e.Account == "FX_REALIZED" && e.Debit.StringFixed(2) == "1500.00" {
			return
		}
	}
	t.Fatalf("IC difference not posted to the configured account: %+v", entries)
}

func TestEliminate_GoodwillAtHistoricalRateHasNoCTA(t *testing.T) {
	p := psExample()
	p.Rates.Closing = p.Rates.Historical
	entries, err := Eliminate(p)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if e.RuleCode == RuleE4 && e.Account == AcctCTA {
			t.Fatalf("closing = historical rate should leave no goodwill CTA: %+v", e)
		}
	}
}

func TestEliminate_Rejections(t *testing.T) {
	cases := []struct {
		name string
		edit func(*Pair)
		code string
	}{
		{"bargain purchase", func(p *Pair) { p.InvestmentCost = d("1") }, CodeBargainPurchase},
		{"zero ownership", func(p *Pair) { p.OwnershipPct = decimal.Zero }, CodeInvalid},
		{"over 100 percent", func(p *Pair) { p.OwnershipPct = d("100.5") }, CodeInvalid},
		{"missing closing rate", func(p *Pair) { p.Rates.Closing = decimal.Zero }, CodeInvalid},
		{"unsold share above one", func(p *Pair) { p.UnsoldShare = d("1.2") }, CodeInvalid},
		{"bad currency", func(p *Pair) { p.Currency = "US" }, CodeInvalid},
		{"negative IC sales", func(p *Pair) { p.ICSales = d("-1") }, CodeInvalid},
		{"negative share capital", func(p *Pair) { p.ShareCapital = d("-1") }, CodeInvalid},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			p := psExample()
			c.edit(&p)
			_, err := Eliminate(p)
			var de *Error
			if !errors.As(err, &de) || de.Code != c.code {
				t.Fatalf("err = %v; want %s", err, c.code)
			}
		})
	}
}

// TestEliminate_WhollyOwnedHasNoNCI: at 100 % ownership E3 keeps no NCI lines,
// E4 credits no NCI and goodwill is investment minus net assets.
func TestEliminate_WhollyOwnedHasNoNCI(t *testing.T) {
	p := psExample()
	p.OwnershipPct = d("100")
	p.InvestmentCost = d("800000")
	entries, err := Eliminate(p)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if e.Account == AcctNCIEquity || e.Account == AcctNCIProfit {
			t.Fatalf("wholly owned unit produced an NCI line: %+v", e)
		}
		if e.RuleCode == RuleE4 && e.Account == AcctGoodwill && e.Debit.StringFixed(2) != "68095.24" {
			t.Fatalf("goodwill = %s; want 68095.24 (65,000 at 1.05 retranslated at 1.10)", e.Debit.StringFixed(2))
		}
	}
}

func TestCheckBalanced_DetectsDefect(t *testing.T) {
	err := CheckBalanced([]Entry{{RuleCode: RuleE1, Account: "x", Debit: d("10")}})
	var de *Error
	if !errors.As(err, &de) || de.Code != CodeUnbalancedRule {
		t.Fatalf("err = %v; want CNS-0903", err)
	}
}
