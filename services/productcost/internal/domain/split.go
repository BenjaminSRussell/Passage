// Package domain holds the pure costing rules of wave 39: cost roll-up, mass
// roll, variance decomposition, and the money arithmetic around them. It does
// no I/O; every function is deterministic for its inputs.
package domain

import (
	"sort"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/money"
)

// Cost components of the default structure (costing.componentStructure).
const (
	CompDM     = "DM"
	CompDL     = "DL"
	CompIndMat = "IND-MAT"
	CompIndLab = "IND-LAB"
)

// UnitScale is the stored precision of unit costs (BR-39.1).
const UnitScale = 6

// MoneyScale is the posting precision when no currency is given.
const MoneyScale = 2

// PostingScale is the number of decimals postings round to (BR-39.1): the
// ISO 4217 minor unit of currency (0 for JPY, 3 for KWD), or MoneyScale when
// the currency is empty.
func PostingScale(currency string) int32 {
	if currency == "" {
		return MoneyScale
	}
	return money.MinorUnits(currency)
}

// Split is a cost component split: component code to amount per unit.
// A zero-value Decimal is zero, so missing keys read as zero.
type Split map[string]decimal.Decimal

// Total returns the sum of all components.
func (s Split) Total() decimal.Decimal {
	t := decimal.Zero
	for _, v := range s {
		t = t.Add(v)
	}
	return t
}

// AddScaled adds o multiplied by f into s.
func (s Split) AddScaled(o Split, f decimal.Decimal) {
	for c, v := range o {
		s[c] = s[c].Add(v.Mul(f))
	}
}

// Add adds v to component c.
func (s Split) Add(c string, v decimal.Decimal) { s[c] = s[c].Add(v) }

// Components returns the component codes in sorted order.
func (s Split) Components() []string {
	keys := make([]string, 0, len(s))
	for c := range s {
		keys = append(keys, c)
	}
	sort.Strings(keys)
	return keys
}

// Rounded returns a copy with every component rounded to scale decimals.
func (s Split) Rounded(scale int32) Split {
	out := make(Split, len(s))
	for c, v := range s {
		out[c] = v.Round(scale)
	}
	return out
}

// Clone returns an independent copy of s.
func (s Split) Clone() Split {
	out := make(Split, len(s))
	for c, v := range s {
		out[c] = v
	}
	return out
}

// sortedKeys returns the keys of a string-keyed map in sorted order.
func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
