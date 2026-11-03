package domain

import (
	"errors"
	"testing"
	"time"

	"github.com/shopspring/decimal"
)

func bankLine(id, account, cur, bal string) BankBalanceLine {
	return BankBalanceLine{LegacyID: id, BankAccount: account, Currency: cur, Balance: decimal.RequireFromString(bal)}
}

var bankMaster = map[string]struct{}{"OPER-USD": {}, "PAY-EUR": {}, "SAV-USD": {}}

func TestValidateBankBalances_Rules(t *testing.T) {
	in := []BankBalanceLine{
		bankLine("B1", "OPER-USD", "USD", "-1250.50"), // overdrawn is legal
		bankLine("B1", "PAY-EUR", "EUR", "10"),        // legacy id twice
		bankLine("B2", "OPER-USD", "USD", "5"),        // bank account twice
		bankLine("B3", "GONE", "USD", "5"),            // not in the master
		bankLine("B4", "PAY-EUR", "EU", "5"),          // not ISO 4217
		bankLine("", "SAV-USD", "USD", "5"),           // blank id
		bankLine("B6", "SAV-USD", "USD", "0"),
	}
	ok, q := ValidateBankBalances(in, bankMaster)
	if len(ok) != 2 || ok[0].LegacyID != "B1" || ok[1].LegacyID != "B6" {
		t.Fatalf("accepted %+v", ok)
	}
	want := []string{RuleUniqueness, RuleUniqueness, RuleReferential, RuleDomain, RuleUniqueness}
	if len(q) != len(want) {
		t.Fatalf("quarantined %+v", q)
	}
	for i, rule := range want {
		if q[i].Rule != rule {
			t.Errorf("quarantine %d is %s, want %s (%s)", i, q[i].Rule, rule, q[i].Reason)
		}
	}
}

// A bank balance is in whole minor units of its currency: a fraction of a cent or of a yen is a
// DOMAIN failure, and trailing zeros are not.
func TestValidateBankBalances_BalanceInMinorUnits(t *testing.T) {
	master := map[string]struct{}{"USD-1": {}, "USD-2": {}, "JPY-1": {}, "JPY-2": {}, "EUR-1": {}}
	ok, q := ValidateBankBalances([]BankBalanceLine{
		bankLine("U1", "USD-1", "USD", "-10.250000"),
		bankLine("U2", "USD-2", "USD", "10.255"),
		bankLine("J1", "JPY-1", "JPY", "1500"),
		bankLine("J2", "JPY-2", "JPY", "1500.5"),
		bankLine("E1", "EUR-1", "EUR", "7.10"),
	}, master)
	if len(ok) != 3 || ok[0].LegacyID != "U1" || ok[1].LegacyID != "J1" || ok[2].LegacyID != "E1" {
		t.Fatalf("accepted %+v", ok)
	}
	if len(q) != 2 || q[0].Record.LegacyID != "U2" || q[1].Record.LegacyID != "J2" || q[0].Rule != RuleDomain || q[1].Rule != RuleDomain {
		t.Fatalf("quarantined %+v", q)
	}
}

func bankRun() *PackRun {
	return &PackRun{Pack: PackBankBal, SourceSystem: SourceAX2012, SourceCompany: "USMF", Status: StatusValidated, CutoffTS: time.Date(2026, 12, 31, 0, 0, 0, 0, time.UTC)}
}

func heldOf(in []BankBalanceLine) map[string]BankBalanceLine {
	m := map[string]BankBalanceLine{}
	for _, b := range in {
		m[b.LegacyID] = b
	}
	return m
}

func TestReconcileBankBalances_MatchAndQuarantineMismatch(t *testing.T) {
	src := []BankBalanceLine{bankLine("B1", "OPER-USD", "USD", "-1250.50"), bankLine("B2", "PAY-EUR", "EUR", "900"), bankLine("B3", "SAV-USD", "USD", "50.25")}
	r := bankRun()
	if err := ReconcileBankBalances(r, src, src, 3, heldOf(src)); err != nil {
		t.Fatal(err)
	}
	if r.Status != StatusReconciled || r.HasMismatch() {
		t.Fatalf("status %s totals %+v", r.Status, r.Totals)
	}
	names := map[string]int{}
	for _, c := range r.Totals {
		names[c.Name]++
	}
	if names[BankBalanceName] != 3 || names[BankEntityName] != 2 || names[BankCountName] != 1 {
		t.Fatalf("totals %v", names)
	}

	// A line held back in quarantine is part of the source and leaves the run LOADED.
	r = bankRun()
	if err := ReconcileBankBalances(r, src, src[:2], 2, heldOf(src[:2])); err != nil {
		t.Fatal(err)
	}
	if r.Status != StatusLoaded || !r.HasMismatch() {
		t.Fatalf("status %s", r.Status)
	}
	r = bankRun()
	r.Status = StatusLoaded
	if err := ReconcileBankBalances(r, src, src, 3, heldOf(src)); !errors.Is(err, ErrTransition) {
		t.Fatalf("not VALIDATED: %v", err)
	}
}

func TestHashBankBalances_ChangesWithBalanceAndIgnoresOrder(t *testing.T) {
	a := []BankBalanceLine{bankLine("B1", "OPER-USD", "USD", "1"), bankLine("B2", "PAY-EUR", "EUR", "2")}
	b := []BankBalanceLine{a[1], a[0]}
	if HashBankBalances(a) != HashBankBalances(b) {
		t.Fatal("order must not matter")
	}
	c := []BankBalanceLine{a[0], bankLine("B2", "PAY-EUR", "EUR", "3")}
	if HashBankBalances(a) == HashBankBalances(c) {
		t.Fatal("a changed balance must change the hash")
	}
	if NotPosted(PackBankBal) == "" || !PackAvailable(PackBankBal) {
		t.Fatal("bank_balances is available and staged only")
	}
}
