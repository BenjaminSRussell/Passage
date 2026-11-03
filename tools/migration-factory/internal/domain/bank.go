package domain

import (
	"fmt"
	"strings"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/money"
)

// TableBank is the legacy_xref source_table the bank_balances pack writes.
const TableBank = "bank_balances"

// Control total names of the bank_balances pack (FR-49.2).
const (
	BankBalanceName = "bank_balances.balance"
	BankEntityName  = "bank_balances.balance_entity"
	BankCountName   = "bank_balances.count"
)

// BankBalanceLine is one legacy bank account balance at the cutoff (AX BankAccountTable ledger
// balance, SAP house bank account FEBKO/FDES). Balance is the book balance in Currency, signed: an
// overdrawn account is negative.
type BankBalanceLine struct {
	LegacyID    string
	BankAccount string
	Currency    string
	Balance     decimal.Decimal
}

// Record maps a line onto the shared record shape that quarantine keeps.
func (b BankBalanceLine) Record() Record {
	return Record{LegacyID: b.LegacyID, Account: b.BankAccount, Currency: b.Currency, Amount: b.Balance}
}

// HashBankBalances returns the run's input hash: the lines sorted, each as legacy id, bank account,
// currency and balance (FR-49.10).
func HashBankBalances(in []BankBalanceLine) string {
	keyed := make([]Keyed, len(in))
	for i, b := range in {
		keyed[i] = Keyed{LegacyID: b.LegacyID + "|" + b.BankAccount + "|" + b.Currency, Amount: b.Balance}
	}
	return HashRecords(keyed)
}

// ValidateBankBalances applies the bank_balances suite (FR-49.11): uniqueness (a legacy id and a
// bank account appear once), referential (the bank account is in the bank account master given
// with the run) and domain (ISO 4217 currency, and a balance in whole minor units of that currency). A failing line is quarantined with the rule and
// reason; the caller loads the rest.
func ValidateBankBalances(in []BankBalanceLine, accounts map[string]struct{}) (accepted []BankBalanceLine, quarantined []Quarantined) {
	seenID := make(map[string]struct{}, len(in))
	seenAccount := make(map[string]struct{}, len(in))
	hold := func(b BankBalanceLine, rule, reason string) {
		quarantined = append(quarantined, Quarantined{Record: b.Record(), Rule: rule, Reason: reason, Line: b})
	}
	for _, b := range in {
		if strings.TrimSpace(b.LegacyID) == "" {
			hold(b, RuleUniqueness, "legacy id is blank")
			continue
		}
		if _, dup := seenID[b.LegacyID]; dup {
			hold(b, RuleUniqueness, "legacy id appears twice in the extract")
			continue
		}
		seenID[b.LegacyID] = struct{}{}
		if _, dup := seenAccount[b.BankAccount]; dup {
			hold(b, RuleUniqueness, fmt.Sprintf("bank account %q appears twice in the extract", b.BankAccount))
			continue
		}
		seenAccount[b.BankAccount] = struct{}{}
		if _, ok := accounts[b.BankAccount]; !ok {
			hold(b, RuleReferential, fmt.Sprintf("bank account %q is not in the bank account master", b.BankAccount))
			continue
		}
		if _, ok := iso4217[b.Currency]; !ok {
			hold(b, RuleDomain, fmt.Sprintf("currency %q is not ISO 4217", b.Currency))
			continue
		}
		// A bank balance is held in whole minor units of its currency (cents, yen): a finer value is
		// a broken extract, not a balance to round.
		if units := money.MinorUnits(b.Currency); !b.Balance.Equal(b.Balance.Round(units)) {
			hold(b, RuleDomain, fmt.Sprintf("balance %s has more than the %d decimal places of %s", b.Balance.String(), units, b.Currency))
			continue
		}
		accepted = append(accepted, b)
	}
	return accepted, quarantined
}

// ReconcileBankBalances finishes a bank_balances run once the accepted lines are held. loaded is
// how many lines this run inserted; held maps each accepted legacy id to the line Passage holds.
// It computes the FR-49.2 totals of the whole extract against what Passage holds: the balance per
// (bank account, currency), the balance per entity and currency, and the account count. The run
// moves from VALIDATED to LOADED and, when no total is MISMATCH, to RECONCILED. Quarantined lines
// are part of the source, so a run with quarantine stays LOADED.
func ReconcileBankBalances(r *PackRun, source, accepted []BankBalanceLine, loaded int, held map[string]BankBalanceLine) error {
	if r.Status != StatusValidated {
		return fmt.Errorf("%w: reconcile needs a VALIDATED run, got %s", ErrTransition, r.Status)
	}
	r.RecordsLoaded = loaded
	r.AlreadyPresent = len(accepted) - loaded
	if err := r.Advance(StatusLoaded); err != nil {
		return err
	}
	var target []BankBalanceLine
	for _, b := range accepted {
		if h, ok := held[b.LegacyID]; ok {
			target = append(target, h)
		}
	}
	company := r.SourceCompany
	var totals []ControlTotal
	add := func(name string, key map[string]string, src, tgt decimal.Decimal) error {
		t, err := NewControlTotal(name, key, src, tgt, Tolerance{})
		if err != nil {
			return err
		}
		totals = append(totals, t)
		return nil
	}
	sum := func(in []BankBalanceLine, group func(BankBalanceLine) string) (map[string]decimal.Decimal, map[string]BankBalanceLine) {
		bal, first := map[string]decimal.Decimal{}, map[string]BankBalanceLine{}
		for _, b := range in {
			k := group(b)
			bal[k] = bal[k].Add(b.Balance)
			if _, ok := first[k]; !ok {
				first[k] = b
			}
		}
		return bal, first
	}
	union := func(a, b map[string]decimal.Decimal) []string {
		seen := map[string]struct{}{}
		for k := range a {
			seen[k] = struct{}{}
		}
		for k := range b {
			seen[k] = struct{}{}
		}
		return sortedKeys(seen)
	}
	perAccount := func(b BankBalanceLine) string { return b.BankAccount + "\x1f" + b.Currency }
	srcAcc, srcFirst := sum(source, perAccount)
	tgtAcc, tgtFirst := sum(target, perAccount)
	for _, k := range union(srcAcc, tgtAcc) {
		b, ok := srcFirst[k]
		if !ok {
			b = tgtFirst[k]
		}
		if err := add(BankBalanceName, map[string]string{"company": company, "bank_account": b.BankAccount, "currency": b.Currency}, srcAcc[k], tgtAcc[k]); err != nil {
			return err
		}
	}
	perCurrency := func(b BankBalanceLine) string { return b.Currency }
	srcEnt, _ := sum(source, perCurrency)
	tgtEnt, _ := sum(target, perCurrency)
	for _, k := range union(srcEnt, tgtEnt) {
		if err := add(BankEntityName, map[string]string{"company": company, "currency": k}, srcEnt[k], tgtEnt[k]); err != nil {
			return err
		}
	}
	if err := add(BankCountName, map[string]string{"company": company}, decimal.NewFromInt(int64(len(source))), decimal.NewFromInt(int64(len(target)))); err != nil {
		return err
	}
	r.Totals = totals
	if !r.HasMismatch() {
		return r.Advance(StatusReconciled)
	}
	return nil
}
