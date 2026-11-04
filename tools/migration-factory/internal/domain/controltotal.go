package domain

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/shopspring/decimal"
)

// ControlStatus is the outcome of comparing a source and target total (FR-49.2).
type ControlStatus string

const (
	StatusMatch           ControlStatus = "MATCH"
	StatusWithinTolerance ControlStatus = "WITHIN_TOLERANCE"
	StatusMismatch        ControlStatus = "MISMATCH"
)

// Tolerance is an explicitly configured allowance. A non-zero tolerance needs a reason (FR-49.2).
type Tolerance struct {
	Amount decimal.Decimal
	Reason string
}

// ControlTotal compares a legacy (source) value with the Passage (target) value at one key.
type ControlTotal struct {
	Name      string
	Key       map[string]string
	Source    decimal.Decimal
	Target    decimal.Decimal
	Delta     decimal.Decimal
	Tolerance Tolerance
	Status    ControlStatus
}

// NewControlTotal evaluates a total. A zero-tolerance comparison is MATCH only on an exact
// equal; a configured tolerance without a reason is rejected.
func NewControlTotal(name string, key map[string]string, source, target decimal.Decimal, tol Tolerance) (ControlTotal, error) {
	if tol.Amount.IsNegative() {
		return ControlTotal{}, fmt.Errorf("%w: tolerance must be >= 0", ErrInvalid)
	}
	if tol.Amount.IsPositive() && strings.TrimSpace(tol.Reason) == "" {
		return ControlTotal{}, fmt.Errorf("%w: tolerance %s needs a reason", ErrInvalid, tol.Amount)
	}
	delta := target.Sub(source)
	status := StatusMatch
	switch {
	case delta.IsZero():
		status = StatusMatch
	case delta.Abs().LessThanOrEqual(tol.Amount):
		status = StatusWithinTolerance
	default:
		status = StatusMismatch
	}
	return ControlTotal{Name: name, Key: key, Source: source, Target: target, Delta: delta, Tolerance: tol, Status: status}, nil
}

// Keyed is one record reduced to the fields a control total hashes.
type Keyed struct {
	LegacyID string
	Amount   decimal.Decimal
}

// HashRecords returns the sha256 of the records sorted by legacy id, as "id=amount" lines.
// The same records in any order produce the same hash (FR-49.2 open_items hash).
func HashRecords(records []Keyed) string {
	lines := make([]string, len(records))
	for i, r := range records {
		lines[i] = r.LegacyID + "=" + r.Amount.StringFixed(6)
	}
	sort.Strings(lines)
	sum := sha256.Sum256([]byte(strings.Join(lines, "\n")))
	return hex.EncodeToString(sum[:])
}

// SumBy groups amounts by a key string and returns the subtotal per key.
func SumBy(keys []string, amounts []decimal.Decimal) (map[string]decimal.Decimal, error) {
	if len(keys) != len(amounts) {
		return nil, fmt.Errorf("%w: keys and amounts differ in length", ErrInvalid)
	}
	out := make(map[string]decimal.Decimal)
	for i, k := range keys {
		out[k] = out[k].Add(amounts[i])
	}
	return out, nil
}

// TrialBalanceBalanced reports whether debits equal credits (balance rule, 9.49.7 BR-49.2 family).
func TrialBalanceBalanced(debits, credits decimal.Decimal) error {
	if !debits.Equal(credits) {
		return fmt.Errorf("%w: trial balance debit %s differs from credit %s", ErrInvalid, debits, credits)
	}
	return nil
}

// AnyTotal is the total name that sets the tolerance of every amount total of a pack that has no
// tolerance of its own.
const AnyTotal = "*"

// toleranceTotals lists, per pack, the control totals a configured tolerance applies to: the money
// totals, each an amount in one currency (its key names the currency). A tolerance is the absolute
// delta allowed on each such total, in that total's currency; deltas are never added up across
// currencies. Counts and quantities always compare exactly, and so do the project_wip measures
// (FR-49.12 requires 0.00, and they add up a project's lines without a currency key).
var toleranceTotals = map[Pack][]string{
	PackOpenItems:  {"open_items.amount", "open_items.amount_by_account"},
	PackGLBalances: {"gl_balances.balance", "gl_balances.trial_balance", ControlAccountName},
	PackOnhand:     {OnhandValueName, OnhandEntityName, OnhandAccountName},
	PackOpenOrders: {OrdersValueName},
	PackBankBal:    {BankBalanceName, BankEntityName},
}

// ToleranceTotals returns the control totals of the pack a tolerance can be set for, or none.
func ToleranceTotals(p Pack) []string { return append([]string(nil), toleranceTotals[p]...) }

func tolerable(name string) bool {
	pack, _, _ := strings.Cut(name, ".")
	for _, n := range toleranceTotals[Pack(pack)] {
		if n == name {
			return true
		}
	}
	return false
}

// ToleranceRule is one configured tolerance, `migration.tolerance.<pack>` (FR-49.2): the allowance
// for a named money total of a pack, or for all its money totals when Total is AnyTotal.
type ToleranceRule struct {
	Pack      Pack
	Total     string
	Amount    decimal.Decimal
	Reason    string
	Version   int
	UpdatedBy string
	UpdatedAt time.Time
}

// Validate refuses a rule with an unknown pack, a pack with no money totals, a total that is not one
// of the pack's money totals, a negative amount, more than 6 decimals, or a positive amount without
// a reason.
func (t ToleranceRule) Validate() error {
	if !knownPack(t.Pack) {
		return fmt.Errorf("%w: %q", ErrUnknownPack, t.Pack)
	}
	names := toleranceTotals[t.Pack]
	if len(names) == 0 {
		return fmt.Errorf("%w: %s has no money totals a tolerance applies to; its totals compare at 0.00", ErrInvalid, t.Pack)
	}
	if t.Total != AnyTotal && (!strings.HasPrefix(t.Total, string(t.Pack)+".") || !tolerable(t.Total)) {
		return fmt.Errorf("%w: total %q is not a money total of %s; a tolerance is set for %s or %s", ErrInvalid, t.Total, t.Pack, strings.Join(names, ", "), AnyTotal)
	}
	if t.Amount.Exponent() < -6 {
		return fmt.Errorf("%w: tolerance has at most 6 decimals", ErrInvalid)
	}
	_, err := NewControlTotal(t.Total, nil, decimal.Zero, decimal.Zero, Tolerance{Amount: t.Amount, Reason: t.Reason})
	return err
}

// ToleranceSet holds the configured tolerances by pack and total name.
type ToleranceSet map[string]ToleranceRule

func toleranceKey(p Pack, total string) string { return string(p) + "|" + total }

// NewToleranceSet indexes rules.
func NewToleranceSet(rules []ToleranceRule) ToleranceSet {
	s := make(ToleranceSet, len(rules))
	for _, r := range rules {
		s[toleranceKey(r.Pack, r.Total)] = r
	}
	return s
}

// For returns the tolerance of one total: zero unless the total is a money total keyed by a
// currency, else the rule naming it, else the pack's AnyTotal rule, else zero. The pack is the
// part of the name before the first dot.
func (s ToleranceSet) For(name string, key map[string]string) Tolerance {
	if len(s) == 0 || key["currency"] == "" || !tolerable(name) {
		return Tolerance{}
	}
	pack, _, _ := strings.Cut(name, ".")
	if r, ok := s[toleranceKey(Pack(pack), name)]; ok {
		return Tolerance{Amount: r.Amount, Reason: r.Reason}
	}
	if r, ok := s[toleranceKey(Pack(pack), AnyTotal)]; ok {
		return Tolerance{Amount: r.Amount, Reason: r.Reason}
	}
	return Tolerance{}
}

// applyTolerances evaluates every total of the run again with the configured tolerances, so a delta
// inside the allowance is WITHIN_TOLERANCE and the run can reconcile. It runs once, when the run
// reconciles; afterwards each total keeps the tolerance it was stored with.
func (r *PackRun) applyTolerances() error {
	if len(r.Tolerances) == 0 {
		return nil
	}
	for i, t := range r.Totals {
		next, err := NewControlTotal(t.Name, t.Key, t.Source, t.Target, r.Tolerances.For(t.Name, t.Key))
		if err != nil {
			return err
		}
		r.Totals[i] = next
	}
	return nil
}

// storedTolerance is the tolerance the run's totals of this name were computed with when the run
// reconciled, or zero. A release that creates a total takes it, so a tolerance configured after the
// run started never reaches the run.
func (r *PackRun) storedTolerance(name string) Tolerance {
	for _, t := range r.Totals {
		if t.Name == name {
			return t.Tolerance
		}
	}
	return Tolerance{}
}

func knownPack(p Pack) bool {
	for _, k := range Packs {
		if k == p {
			return true
		}
	}
	return false
}
