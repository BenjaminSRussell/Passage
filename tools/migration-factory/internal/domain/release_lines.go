package domain

import (
	"fmt"
	"strings"

	"github.com/shopspring/decimal"
)

// BankCorrection is an operator's fix to a quarantined bank_balances line. Only the bank account is
// correctable; the legacy id, currency and balance are the legacy truth and change only by
// re-extract, so the control totals keep comparing against the source (FR-49.2). Blank keeps the
// extracted value.
type BankCorrection struct {
	BankAccount string
}

// IsZero reports whether the correction changes nothing.
func (c BankCorrection) IsZero() bool { return strings.TrimSpace(c.BankAccount) == "" }

// ReleaseBankBalance re-runs the bank_balances suite on a corrected quarantined line and, when it
// passes, books the line into the run as loaded: the Passage side of the account, entity and count
// totals gains it, a corrected bank account moves the legacy balance from the extracted account's
// subtotal to the corrected one (as if the extract had mapped it there), the counts move from
// quarantined to loaded, and the run moves to RECONCILED when no total is MISMATCH any more.
// accounts is the bank account master to check the account against. It returns the accepted line,
// ErrInvalid naming the rule that still fails, or ErrTransition when the run is not LOADED.
func ReleaseBankBalance(r *PackRun, original BankBalanceLine, fix BankCorrection, accounts map[string]struct{}) (BankBalanceLine, error) {
	if r.Pack != PackBankBal {
		return BankBalanceLine{}, fmt.Errorf("%w: run is a %s run, not bank_balances", ErrUnknownPack, r.Pack)
	}
	if r.Status != StatusLoaded {
		return BankBalanceLine{}, fmt.Errorf("%w: a line can be released only while the run is LOADED, run is %s", ErrTransition, r.Status)
	}
	line := original
	if a := strings.TrimSpace(fix.BankAccount); a != "" {
		line.BankAccount = a
	}
	// Validated alone: the extract-wide uniqueness of the legacy id and account was decided when the
	// run was started; the store refuses an account Passage already holds under another legacy id.
	if _, quarantined := ValidateBankBalances([]BankBalanceLine{line}, accounts); len(quarantined) > 0 {
		q := quarantined[0]
		return BankBalanceLine{}, fmt.Errorf("%w: %s rule still fails: %s", ErrInvalid, q.Rule, q.Reason)
	}
	company, bal := r.SourceCompany, line.Balance
	account := func(a string) map[string]string {
		return map[string]string{"company": company, "bank_account": a, "currency": line.Currency}
	}
	moves := []totalMove{
		{BankEntityName, map[string]string{"company": company, "currency": line.Currency}, decimal.Zero, bal},
		{BankCountName, map[string]string{"company": company}, decimal.Zero, decimal.NewFromInt(1)},
	}
	if line.BankAccount != original.BankAccount {
		moves = append(moves,
			totalMove{BankBalanceName, account(original.BankAccount), bal.Neg(), decimal.Zero},
			totalMove{BankBalanceName, account(line.BankAccount), bal, bal})
	} else {
		moves = append(moves, totalMove{BankBalanceName, account(line.BankAccount), decimal.Zero, bal})
	}
	return line, finishRelease(r, moves)
}

// OnhandCorrection is an operator's fix to a quarantined onhand line: the item, the inventory
// dimension (site, warehouse, location, batch) and the unit. Quantity, value, currency and serial
// are the legacy truth and change only by re-extract. Blank keeps the extracted value, so a location
// or batch cannot be corrected to blank.
type OnhandCorrection struct {
	Item, Site, Warehouse, Location, Batch, Unit string
}

// IsZero reports whether the correction changes nothing: every field blank or whitespace, as
// Apply keeps the extracted value for those.
func (c OnhandCorrection) IsZero() bool {
	for _, v := range []string{c.Item, c.Site, c.Warehouse, c.Location, c.Batch, c.Unit} {
		if strings.TrimSpace(v) != "" {
			return false
		}
	}
	return true
}

// Apply returns the line with the correction applied.
func (c OnhandCorrection) Apply(o OnhandLine) OnhandLine {
	set := func(dst *string, v string) {
		if v = strings.TrimSpace(v); v != "" {
			*dst = v
		}
	}
	set(&o.Item, c.Item)
	set(&o.Site, c.Site)
	set(&o.Warehouse, c.Warehouse)
	set(&o.Location, c.Location)
	set(&o.Batch, c.Batch)
	set(&o.Unit, c.Unit)
	return o
}

// ReleaseOnhand is ReleaseBankBalance for an onhand line: it re-runs the onhand suite on the
// corrected line and books it into the run. The quantity totals (per item and dimension) and the
// value totals (per item, site and currency) move the legacy figures from the extracted keys to the
// corrected ones; the entity value and, when the run reconciled an inventory GL account (BR-49.3),
// that account's total gain the line on the Passage side. items is the item master.
func ReleaseOnhand(r *PackRun, original OnhandLine, fix OnhandCorrection, items map[string]struct{}) (OnhandLine, error) {
	if r.Pack != PackOnhand {
		return OnhandLine{}, fmt.Errorf("%w: run is a %s run, not onhand", ErrUnknownPack, r.Pack)
	}
	if r.Status != StatusLoaded {
		return OnhandLine{}, fmt.Errorf("%w: a line can be released only while the run is LOADED, run is %s", ErrTransition, r.Status)
	}
	line := fix.Apply(original)
	if _, quarantined := ValidateOnhand([]OnhandLine{line}, items); len(quarantined) > 0 {
		q := quarantined[0]
		return OnhandLine{}, fmt.Errorf("%w: %s rule still fails: %s", ErrInvalid, q.Rule, q.Reason)
	}
	company, cur := r.SourceCompany, line.Currency
	qtyKey := func(o OnhandLine) map[string]string {
		return map[string]string{"company": company, "item": o.Item, "site": o.Site, "warehouse": o.Warehouse,
			"location": o.Location, "batch": o.Batch, "serial": o.Serial}
	}
	valKey := func(o OnhandLine) map[string]string {
		return map[string]string{"company": company, "item": o.Item, "site": o.Site, "currency": o.Currency}
	}
	var moves []totalMove
	shift := func(name string, from, to map[string]string, amount decimal.Decimal) {
		if sameKey(from, to) {
			moves = append(moves, totalMove{name, to, decimal.Zero, amount})
			return
		}
		moves = append(moves, totalMove{name, from, amount.Neg(), decimal.Zero}, totalMove{name, to, amount, amount})
	}
	shift(OnhandQuantityName, qtyKey(original), qtyKey(line), line.Quantity)
	shift(OnhandValueName, valKey(original), valKey(line), line.Value)
	moves = append(moves, totalMove{OnhandEntityName, map[string]string{"company": company, "currency": cur}, decimal.Zero, line.Value})
	// BR-49.3: the loaded on-hand value of the reconciled inventory account, in the line's currency.
	account := ""
	for _, t := range r.Totals {
		if t.Name == OnhandAccountName {
			account = t.Key["account"]
		}
	}
	if account != "" {
		moves = append(moves, totalMove{OnhandAccountName, map[string]string{"company": company, "account": account, "currency": cur}, decimal.Zero, line.Value})
	}
	return line, finishRelease(r, moves)
}

// finishRelease books the moves and moves the run's counts and status after a line release.
func finishRelease(r *PackRun, moves []totalMove) error {
	if err := applyMoves(r, moves); err != nil {
		return err
	}
	r.RecordsLoaded++
	r.RecordsQuarantined--
	if !r.HasMismatch() {
		return r.Advance(StatusReconciled)
	}
	return nil
}
