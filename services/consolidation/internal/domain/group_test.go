package domain

import (
	"testing"
	"time"

	"github.com/shopspring/decimal"
)

func day(s string) time.Time {
	t, err := time.Parse("2006-01-02", s)
	if err != nil {
		panic(err)
	}
	return t
}

func pct(s string) decimal.Decimal { return decimal.RequireFromString(s) }

func TestAddHolding_ClosesTheOpenIntervalOfTheSameHolder(t *testing.T) {
	history := []Holding{{ID: "h1", From: day("2025-01-01"), Percent: pct("60")}}
	next, err := AddHolding(history, Holding{From: day("2026-07-01"), Percent: pct("80")})
	if err != nil {
		t.Fatal(err)
	}
	if !next[0].To.Equal(day("2026-07-01")) {
		t.Fatalf("old interval closes on the new start, got %v", next[0].To)
	}
	u := Unit{Holdings: next}
	if got := u.PercentOn(day("2026-06-30")); !got.Equal(pct("60")) {
		t.Errorf("before the change = %s, want 60", got)
	}
	if got := u.PercentOn(day("2026-07-01")); !got.Equal(pct("80")) {
		t.Errorf("from the change = %s, want 80", got)
	}
}

func TestAddHolding_HoldersTogetherStayAtOrBelowOneHundred(t *testing.T) {
	history := []Holding{{ID: "h1", From: day("2025-01-01"), Percent: pct("70")}}
	_, err := AddHolding(history, Holding{HolderUnitID: "other", From: day("2025-06-01"), Percent: pct("40")})
	var de *Error
	if err == nil || !asError(err, &de) || de.Code != CodeOwnership {
		t.Fatalf("110 %% in total must fail with %s, got %v", CodeOwnership, err)
	}
	if _, err := AddHolding(history, Holding{HolderUnitID: "other", From: day("2025-06-01"), Percent: pct("30")}); err != nil {
		t.Fatalf("exactly 100 %% is allowed: %v", err)
	}
}

func TestAddHolding_RejectsASecondIntervalOnTheSameDate(t *testing.T) {
	history := []Holding{{ID: "h1", From: day("2025-01-01"), Percent: pct("70")}}
	if _, err := AddHolding(history, Holding{From: day("2025-01-01"), Percent: pct("50")}); err == nil {
		t.Fatal("two intervals starting on one date for one holder must be refused")
	}
}

func TestValidateMapping(t *testing.T) {
	ok := []AccountMapping{{"1000", "CASH", AccountBalanceSheet}, {"4000", "REV", AccountIncome}}
	if err := ValidateMapping(ok); err != nil {
		t.Fatal(err)
	}
	for name, rows := range map[string][]AccountMapping{
		"duplicate main account": {{"1000", "CASH", AccountBalanceSheet}, {"1000", "BANK", AccountBalanceSheet}},
		"unknown type":           {{"1000", "CASH", "equity"}},
		"bad code":               {{"10 00", "CASH", AccountBalanceSheet}},
	} {
		if err := ValidateMapping(rows); err == nil {
			t.Errorf("%s must be refused", name)
		}
	}
}

func TestGroupAndUnitValidate(t *testing.T) {
	if err := (Group{Code: "CRP", Name: "Cedar Ridge", Currency: "USD"}).Validate(); err != nil {
		t.Fatal(err)
	}
	for name, g := range map[string]Group{
		"code":     {Code: "no good", Name: "x", Currency: "USD"},
		"name":     {Code: "CRP", Currency: "USD"},
		"currency": {Code: "CRP", Name: "x", Currency: "usd"},
	} {
		if g.Validate() == nil {
			t.Errorf("group with a bad %s must be refused", name)
		}
	}
	if (Unit{LegalEntityID: "e", LedgerID: "l", Currency: "EUR"}).Validate() != nil {
		t.Error("valid unit refused")
	}
	if (Unit{LegalEntityID: "e", Currency: "EUR"}).Validate() == nil {
		t.Error("a unit without a ledger must be refused")
	}
}
