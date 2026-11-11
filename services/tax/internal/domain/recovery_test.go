package domain_test

import (
	"testing"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/services/tax/internal/domain"
)

func policy(code, group, pct, from, to string) domain.RecoveryPolicy {
	p := domain.RecoveryPolicy{Code: code, ItemGroup: group, RecoverablePct: domain.D(pct), ValidFrom: domain.Day(from)}
	if to != "" {
		p.ValidTo = domain.Day(to)
	}
	return p
}

func TestRecoveryFor_PrefersTheItemGroupThenTheLaterStartAndHonoursDates(t *testing.T) {
	policies := []domain.RecoveryPolicy{
		policy("VAT", "", "100", "2020-01-01", "2025-12-31"),
		policy("VAT", "", "80", "2026-01-01", ""),
		policy("VAT", "CARS", "50", "2020-01-01", ""),
		policy("OTHER", "", "10", "2020-01-01", ""),
	}
	for _, c := range []struct {
		code, group, date string
		want              string
	}{
		{"VAT", "STANDARD", "2025-06-01", "100"},
		{"VAT", "STANDARD", "2026-06-01", "80"},
		{"VAT", "CARS", "2026-06-01", "50"},
		{"VAT", "CARS", "2020-06-01", "50"},
		{"OTHER", "", "2026-01-01", "10"},
		{"VAT", "STANDARD", "2019-12-31", ""},
		{"NONE", "STANDARD", "2026-01-01", ""},
	} {
		got := domain.RecoveryFor(policies, c.code, c.group, domain.Day(c.date).Time)
		if c.want == "" {
			if got != nil {
				t.Errorf("%+v: got %s, want none", c, got)
			}
			continue
		}
		if got == nil || !got.Equal(decimal.RequireFromString(c.want)) {
			t.Errorf("%+v: got %v, want %s", c, got, c.want)
		}
	}
}

func TestCalculateUsesTheRecoveryPolicyOfTheLinesItemGroup(t *testing.T) {
	cfg := buildConfig(domain.RoundPerLine, spec{name: "VAT", pct: "20"})
	cfg.ItemGroups["CARS"] = []string{"VAT"}
	cfg.Recovery = []domain.RecoveryPolicy{policy("VAT", "CARS", "50", "2020-01-01", "")}
	doc := buildDoc("EUR", domain.DirInput, false, "100.00", "100.00")
	doc.Lines[1].ItemTaxGroup = "CARS"
	res, err := domain.Calculate(doc, cfg)
	if err != nil {
		t.Fatal(err)
	}
	first, second := res.Lines[0].Taxes[0], res.Lines[1].Taxes[0]
	mustEqual(t, "ordinary line recoverable", first.Recoverable, "20")
	mustEqual(t, "ordinary line nonrecoverable", first.Nonrecoverable, "0")
	mustEqual(t, "company car recoverable", second.Recoverable, "10")
	mustEqual(t, "company car nonrecoverable", second.Nonrecoverable, "10")
	// Output tax is never reduced by a recovery policy.
	out := buildDoc("EUR", domain.DirOutput, false, "100.00")
	out.Lines[0].ItemTaxGroup = "CARS"
	res, err = domain.Calculate(out, cfg)
	if err != nil {
		t.Fatal(err)
	}
	mustEqual(t, "output tax recoverable", res.Lines[0].Taxes[0].Recoverable, "20")
}

func TestValidateRecoveryPolicy(t *testing.T) {
	codes, groups := map[string]bool{"VAT": true}, map[string]bool{"CARS": true}
	if err := domain.ValidateRecoveryPolicy(policy("VAT", "CARS", "50", "2026-01-01", ""), codes, groups); err != nil {
		t.Fatal(err)
	}
	for name, p := range map[string]domain.RecoveryPolicy{
		"unknown code":  policy("NOPE", "", "50", "2026-01-01", ""),
		"unknown group": policy("VAT", "BOATS", "50", "2026-01-01", ""),
		"over 100":      policy("VAT", "", "101", "2026-01-01", ""),
		"negative":      policy("VAT", "", "-1", "2026-01-01", ""),
		"too precise":   policy("VAT", "", "50.12345", "2026-01-01", ""),
		"ends before":   policy("VAT", "", "50", "2026-02-01", "2026-01-01"),
		"no start":      {Code: "VAT", RecoverablePct: domain.D("50")},
	} {
		if err := domain.ValidateRecoveryPolicy(p, codes, groups); err == nil {
			t.Errorf("%s was accepted", name)
		}
	}
}

func TestComputeReclaim_UsesTheRecordedRecoverableOrTheCodesShareAndNeverMoreThanTheTax(t *testing.T) {
	period := domain.Period{From: domain.Day("2026-04-01"), To: domain.Day("2026-06-30")}
	day := func(s string) domain.ReclaimTxn {
		return domain.ReclaimTxn{TaxDate: domain.Day(s).Time, Direction: domain.DirInput}
	}
	rec := decimal.RequireFromString("30.00")
	tooMuch := decimal.RequireFromString("999")
	a, b, c, d, e := day("2026-04-10"), day("2026-05-10"), day("2026-06-10"), day("2026-03-31"), day("2026-05-11")
	a.Code, a.Amount, a.Recoverable = "VAT19", decimal.RequireFromString("100.00"), &rec
	b.Code, b.Amount = "VAT19", decimal.RequireFromString("50.00") // takes the code's 80 percent: 40.00
	c.Code, c.Amount, c.Recoverable = "VAT7", decimal.RequireFromString("7.00"), &tooMuch
	d.Code, d.Amount = "VAT19", decimal.RequireFromString("1000.00") // before the period
	e.Code, e.Amount, e.Direction = "VAT19", decimal.RequireFromString("5.00"), domain.DirOutput
	pct := domain.D("80")
	got := domain.ComputeReclaim(period, map[string]*domain.Dec{"VAT19": &pct}, []domain.ReclaimTxn{a, b, c, d, e})
	if len(got.Rows) != 2 || got.Rows[0].Code != "VAT19" || got.Rows[1].Code != "VAT7" {
		t.Fatalf("rows = %+v", got.Rows)
	}
	v19, v7 := got.Rows[0], got.Rows[1]
	mustEqualDec(t, "VAT19 tax", v19.InputTax, "150.00")
	mustEqualDec(t, "VAT19 recoverable", v19.Recoverable, "70.00")
	mustEqualDec(t, "VAT19 nonrecoverable", v19.NonRecoverable, "80.00")
	if v19.Transactions != 2 || v19.Estimated != 1 {
		t.Errorf("VAT19 = %+v", v19)
	}
	mustEqualDec(t, "VAT7 recoverable is capped at the tax", v7.Recoverable, "7.00")
	mustEqualDec(t, "total tax", got.TotalInputTax, "157.00")
	mustEqualDec(t, "total recoverable", got.TotalRecoverable, "77.00")
	mustEqualDec(t, "total nonrecoverable", got.TotalNonRecoverable, "80.00")
}

func mustEqualDec(t *testing.T, label string, got decimal.Decimal, want string) {
	t.Helper()
	if !got.Equal(decimal.RequireFromString(want)) {
		t.Errorf("%s = %s, want %s", label, got, want)
	}
}
