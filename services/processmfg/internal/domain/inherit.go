package domain

import (
	"time"

	"github.com/shopspring/decimal"
)

// CodeMissingSender is the PMF-0007 warning: a sender batch has no value for
// the attribute, so the receiver attribute is left empty (FR-24.15).
const CodeMissingSender = "PMF-0007"

// Aggregation is how a receiver attribute is derived from its sender batches
// (FR-24.15).
type Aggregation string

const (
	AggCopy            Aggregation = "copy"
	AggMin             Aggregation = "min"
	AggMax             Aggregation = "max"
	AggWeightedAverage Aggregation = "weighted-average"
	AggEarliestDate    Aggregation = "earliest-date"
	AggLatestDate      Aggregation = "latest-date"
	AggFixed           Aggregation = "fixed"
)

// Value is one attribute value. At most one field is set for a populated value;
// all nil means the value is missing.
type Value struct {
	Num  *decimal.Decimal
	Text *string
	Date *time.Time
}

// Empty reports whether no field is set.
func (v Value) Empty() bool { return v.Num == nil && v.Text == nil && v.Date == nil }

// Sender is one sender batch's value for an attribute and the quantity consumed
// from that batch (the weight for weighted-average).
type Sender struct {
	BatchID string
	Value   Value
	Qty     decimal.Decimal
}

// Rule is one inheritance rule for one receiver attribute. Fixed is used only by
// AggFixed. Decimals is the rounding for weighted averages (BR-24.11); a negative
// value selects the default of 4, and at most MaxDecimals is allowed.
type Rule struct {
	Attribute   string
	Aggregation Aggregation
	Decimals    int32
	Fixed       Value
}

// DefaultDecimals and MaxDecimals bound the rounding of a weighted average
// (BR-24.11: the attribute's decimals, default 4).
const (
	DefaultDecimals int32 = 4
	MaxDecimals     int32 = 10
)

// Derived is the outcome of one derivation. Missing lists the sender batches that
// had no value; the derivation log records them (PMF-0007).
type Derived struct {
	Value   Value
	Missing []string
}

// Derive applies a rule to its senders (FR-24.15, BR-24.11). Senders without a
// value of the rule's kind go to Missing and are ignored by the aggregation. copy
// needs one distinct value among the senders that have one.
func Derive(rule Rule, senders []Sender) (Derived, error) {
	if rule.Aggregation == AggFixed {
		if rule.Fixed.Empty() {
			return Derived{}, errorf(CodeInvalid, "a fixed rule needs a fixed value")
		}
		return Derived{Value: rule.Fixed}, nil
	}
	switch rule.Aggregation {
	case AggCopy, AggMin, AggMax, AggWeightedAverage, AggEarliestDate, AggLatestDate:
	default:
		return Derived{}, errorf(CodeInvalid, "unknown aggregation %q", rule.Aggregation)
	}
	dec := rule.Decimals
	if dec < 0 {
		dec = DefaultDecimals
	}
	if dec > MaxDecimals {
		return Derived{}, errorf(CodeInvalid, "decimals must be at most %d", MaxDecimals)
	}
	var out Derived
	var have []Sender
	for _, s := range senders {
		if hasKind(rule.Aggregation, s.Value) {
			have = append(have, s)
		} else {
			out.Missing = append(out.Missing, s.BatchID)
		}
	}
	if len(have) == 0 {
		return out, nil
	}
	switch rule.Aggregation {
	case AggCopy:
		for _, s := range have[1:] {
			if !sameValue(have[0].Value, s.Value) {
				return Derived{}, errorf(CodeInvalid, "copy of %s needs a single sender value", rule.Attribute)
			}
		}
		out.Value = have[0].Value
	case AggMin, AggMax:
		best := have[0].Value
		for _, s := range have[1:] {
			c := s.Value.Num.Cmp(*best.Num)
			if (rule.Aggregation == AggMin && c < 0) || (rule.Aggregation == AggMax && c > 0) {
				best = s.Value
			}
		}
		out.Value = best
	case AggWeightedAverage:
		sum, qty := decimal.Zero, decimal.Zero
		for _, s := range have {
			if !s.Qty.IsPositive() {
				return Derived{}, errorf(CodeInvalid, "sender %s has no consumed quantity", s.BatchID)
			}
			sum = sum.Add(s.Value.Num.Mul(s.Qty))
			qty = qty.Add(s.Qty)
		}
		avg := sum.DivRound(qty, 24).Round(dec)
		out.Value = Value{Num: &avg}
	case AggEarliestDate, AggLatestDate:
		best := *have[0].Value.Date
		for _, s := range have[1:] {
			d := *s.Value.Date
			if (rule.Aggregation == AggEarliestDate && d.Before(best)) || (rule.Aggregation == AggLatestDate && d.After(best)) {
				best = d
			}
		}
		out.Value = Value{Date: &best}
	}
	return out, nil
}

// hasKind reports whether v carries the value kind the aggregation consumes.
func hasKind(agg Aggregation, v Value) bool {
	switch agg {
	case AggMin, AggMax, AggWeightedAverage:
		return v.Num != nil
	case AggEarliestDate, AggLatestDate:
		return v.Date != nil
	default:
		return !v.Empty()
	}
}

// sameValue compares two populated values of the same kind.
func sameValue(a, b Value) bool {
	switch {
	case a.Num != nil && b.Num != nil:
		return a.Num.Equal(*b.Num)
	case a.Text != nil && b.Text != nil:
		return *a.Text == *b.Text
	case a.Date != nil && b.Date != nil:
		return a.Date.Equal(*b.Date)
	}
	return false
}
