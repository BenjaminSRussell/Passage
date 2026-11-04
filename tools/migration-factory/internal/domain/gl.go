package domain

import (
	"fmt"
	"sort"
	"strings"

	"github.com/shopspring/decimal"
)

// TableGLBalances is the legacy_xref source_table the gl_balances pack writes.
const TableGLBalances = "gl_balances"

// GLBalance is one legacy GL balance at (account, dimension combination, currency) at the
// cutoff, as extracted for the gl_balances pack (FR-49.1; AX GeneralJournalAccountEntry with
// DimensionAttributeValueCombination, SAP FAGLFLEXT or ACDOCA). Balance is signed: debit
// positive, credit negative. Dimensions is the legacy dimension combination as one display
// value (for example "USMF-100-SALES"); an empty combination is allowed.
type GLBalance struct {
	LegacyID   string
	Account    string
	Dimensions string
	Currency   string
	Balance    decimal.Decimal
}

// Key is the natural key of a balance: account, dimension combination and currency. Two rows
// with the same key would load the same opening balance twice (FR-49.10).
func (g GLBalance) Key() string {
	return g.Account + "|" + g.Dimensions + "|" + g.Currency
}

// Record maps a balance onto the shared record shape that quarantine keeps.
func (g GLBalance) Record() Record {
	return Record{LegacyID: g.LegacyID, Account: g.Account, Dimensions: g.Dimensions, Currency: g.Currency, Amount: g.Balance}
}

// HashGL returns the run's input hash: the balances sorted, each as legacy id, natural key and
// balance, so a changed account, dimension or amount changes the hash (FR-49.10).
func HashGL(in []GLBalance) string {
	keyed := make([]Keyed, len(in))
	for i, g := range in {
		keyed[i] = Keyed{LegacyID: g.LegacyID + "|" + g.Key(), Amount: g.Balance}
	}
	return HashRecords(keyed)
}

// ValidateGL applies the gl_balances suite (FR-49.11): uniqueness (a legacy id and a natural key
// appear once), referential (the account is in the chart of accounts) and domain (ISO 4217
// currency). A failing balance is quarantined with the rule and reason; the caller loads the rest.
func ValidateGL(in []GLBalance, accounts map[string]struct{}) (accepted []GLBalance, quarantined []Quarantined) {
	seenID := make(map[string]struct{}, len(in))
	seenKey := make(map[string]struct{}, len(in))
	hold := func(g GLBalance, rule, reason string) {
		quarantined = append(quarantined, Quarantined{Record: g.Record(), Rule: rule, Reason: reason})
	}
	for _, g := range in {
		if strings.TrimSpace(g.LegacyID) == "" {
			hold(g, RuleUniqueness, "legacy id is blank")
			continue
		}
		if _, dup := seenID[g.LegacyID]; dup {
			hold(g, RuleUniqueness, "legacy id appears twice in the extract")
			continue
		}
		seenID[g.LegacyID] = struct{}{}
		if _, dup := seenKey[g.Key()]; dup {
			hold(g, RuleUniqueness, "account, dimensions and currency appear twice in the extract")
			continue
		}
		seenKey[g.Key()] = struct{}{}
		if _, ok := accounts[g.Account]; !ok {
			hold(g, RuleReferential, fmt.Sprintf("account %q is not in the chart of accounts", g.Account))
			continue
		}
		if _, ok := iso4217[g.Currency]; !ok {
			hold(g, RuleDomain, fmt.Sprintf("currency %q is not ISO 4217", g.Currency))
			continue
		}
		accepted = append(accepted, g)
	}
	return accepted, quarantined
}

// glSides returns the debit and credit totals of the balances in one currency.
func glSides(in []GLBalance, currency string) (debit, credit decimal.Decimal) {
	for _, g := range in {
		if g.Currency != currency {
			continue
		}
		if g.Balance.IsPositive() {
			debit = debit.Add(g.Balance)
		} else {
			credit = credit.Sub(g.Balance)
		}
	}
	return debit, credit
}

// ExtractBalanced rejects an extract whose debits differ from its credits in any currency. An
// unbalanced extract is not a legacy trial balance, so nothing of it is loaded (FR-49.11 balance
// expectation, FR-49.2 "trial balance debit = credit").
func ExtractBalanced(in []GLBalance) error {
	for _, cur := range glCurrencies(in) {
		debit, credit := glSides(in, cur)
		if err := TrialBalanceBalanced(debit, credit); err != nil {
			return fmt.Errorf("%w: gl_balances extract is not balanced in %s: debit %s, credit %s", ErrInvalid, cur, debit.StringFixed(2), credit.StringFixed(2))
		}
	}
	return nil
}

// ReconcileGL finishes a gl_balances run once the accepted balances are held. loaded is how
// many balances this run inserted; held maps each accepted legacy id to the balance Passage
// holds for it (loaded now or by an earlier run, with the account, dimensions and currency it was
// loaded under). openItems are the control totals of an open_items run to reconcile with
// (BR-49.2), or nil.
//
// It computes the FR-49.2 gl_balances totals of the whole extract against what Passage holds:
// the balance per (company, account, dimensions, currency), the count of non-zero combinations,
// and the trial balance per currency (the loaded debits less credits must equal the extract's,
// which is zero). With openItems it adds one gl_balances.control_account total per control
// account and currency of the open items: the migrated GL balance of that account (all its
// dimension combinations) against the open items loaded for it, to 0.00. The run moves from
// VALIDATED to LOADED and, when no total is MISMATCH, to RECONCILED. Quarantined balances are part
// of the source, so a run with quarantine stays LOADED.
func ReconcileGL(r *PackRun, source, accepted []GLBalance, loaded int, held map[string]GLBalance, openItems []ControlTotal) error {
	if r.Status != StatusValidated {
		return fmt.Errorf("%w: reconcile needs a VALIDATED run, got %s", ErrTransition, r.Status)
	}
	r.RecordsLoaded = loaded
	r.AlreadyPresent = len(accepted) - loaded
	if err := r.Advance(StatusLoaded); err != nil {
		return err
	}
	var target []GLBalance
	for _, g := range accepted {
		if h, ok := held[g.LegacyID]; ok {
			target = append(target, h)
		}
	}
	company := r.SourceCompany
	srcBy, tgtBy := sumGL(source), sumGL(target)
	keys := make(map[string]GLBalance, len(srcBy)+len(tgtBy))
	for k, g := range tgtBy {
		keys[k] = g
	}
	for k, g := range srcBy {
		keys[k] = g
	}
	var totals []ControlTotal
	for _, k := range sortedKeys(keys) {
		g := keys[k]
		key := map[string]string{"company": company, "account": g.Account, "dimensions": g.Dimensions, "currency": g.Currency}
		t, err := NewControlTotal("gl_balances.balance", key, srcBy[k].Balance, tgtBy[k].Balance, Tolerance{})
		if err != nil {
			return err
		}
		totals = append(totals, t)
	}
	n, err := NewControlTotal("gl_balances.nonzero_count", map[string]string{"company": company},
		decimal.NewFromInt(int64(countNonZero(srcBy))), decimal.NewFromInt(int64(countNonZero(tgtBy))), Tolerance{})
	if err != nil {
		return err
	}
	totals = append(totals, n)
	for _, cur := range glCurrencies(source, target) {
		srcDebit, srcCredit := glSides(source, cur)
		tgtDebit, tgtCredit := glSides(target, cur)
		t, err := NewControlTotal("gl_balances.trial_balance", map[string]string{"company": company, "currency": cur},
			srcDebit.Sub(srcCredit), tgtDebit.Sub(tgtCredit), Tolerance{})
		if err != nil {
			return err
		}
		totals = append(totals, t)
	}
	controls, err := controlAccountTotals(company, target, openItems)
	if err != nil {
		return err
	}
	r.Totals = append(totals, controls...)
	if err := r.applyTolerances(); err != nil {
		return err
	}
	if !r.HasMismatch() {
		return r.Advance(StatusReconciled)
	}
	return nil
}

// ControlAccountName is the BR-49.2 total: open items per control account against the GL.
const ControlAccountName = "gl_balances.control_account"

// controlAccountTotals compares, for every (account, currency) of the open items run's
// open_items.amount_by_account totals, the GL balance loaded for that account against the open
// items Passage holds for it (BR-49.2). The GL balance is the source. Both sides carry the ledger
// sign, so payables open items are negative like the payables control account.
func controlAccountTotals(company string, gl []GLBalance, openItems []ControlTotal) ([]ControlTotal, error) {
	type acct struct{ account, currency string }
	open := map[acct]decimal.Decimal{}
	for _, t := range openItems {
		if t.Name != "open_items.amount_by_account" {
			continue
		}
		k := acct{t.Key["account"], t.Key["currency"]}
		open[k] = open[k].Add(t.Target)
	}
	ledger := map[acct]decimal.Decimal{}
	for _, g := range gl {
		k := acct{g.Account, g.Currency}
		if _, ok := open[k]; ok {
			ledger[k] = ledger[k].Add(g.Balance)
		}
	}
	keys := make([]acct, 0, len(open))
	for k := range open {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i].account != keys[j].account {
			return keys[i].account < keys[j].account
		}
		return keys[i].currency < keys[j].currency
	})
	out := make([]ControlTotal, 0, len(keys))
	for _, k := range keys {
		t, err := NewControlTotal(ControlAccountName, map[string]string{"company": company, "account": k.account, "currency": k.currency},
			ledger[k], open[k], Tolerance{})
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, nil
}

// ControlAccountMessage renders MIG-1506 for a control-account total that does not match.
func ControlAccountMessage(t ControlTotal) string {
	return fmt.Sprintf("MIG-1506 Open items for control account %s (%s %s) differ from migrated GL balance (%s %s).",
		t.Key["account"], t.Target.StringFixed(2), t.Key["currency"], t.Source.StringFixed(2), t.Key["currency"])
}

// sumGL sums balances by natural key; the first row of a key supplies the identity fields.
func sumGL(in []GLBalance) map[string]GLBalance {
	out := make(map[string]GLBalance, len(in))
	for _, g := range in {
		cur, ok := out[g.Key()]
		if !ok {
			cur = g
			cur.Balance = decimal.Zero
		}
		cur.Balance = cur.Balance.Add(g.Balance)
		out[g.Key()] = cur
	}
	return out
}

// glCurrencies returns the sorted distinct currencies of the lists.
func glCurrencies(lists ...[]GLBalance) []string {
	seen := map[string]struct{}{}
	for _, list := range lists {
		for _, g := range list {
			seen[g.Currency] = struct{}{}
		}
	}
	return sortedKeys(seen)
}

func countNonZero(by map[string]GLBalance) int {
	n := 0
	for _, g := range by {
		if !g.Balance.IsZero() {
			n++
		}
	}
	return n
}
