package domain

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"

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
