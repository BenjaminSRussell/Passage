package domain

import (
	"fmt"

	"github.com/shopspring/decimal"
)

// PackLevel is one packaging level with its quantity of the base unit (FR-31.17).
// Levels run each, inner, case, pallet and quantities must not decrease upward.
type PackLevel struct {
	Name        string          `json:"name"`
	QtyPerLevel decimal.Decimal `json:"qtyPerLevel"`
	LPType      string          `json:"lpType,omitempty"`
}

// PackagingSpec is a packaging specification for an item or an item group,
// optionally restricted to one customer. Exactly one of ItemID and ItemGroup is set.
type PackagingSpec struct {
	ID              string      `json:"id"`
	Version         int         `json:"version"`
	ItemID          string      `json:"itemId,omitempty"`
	ItemGroup       string      `json:"itemGroup,omitempty"`
	CustomerID      string      `json:"customerId,omitempty"`
	Levels          []PackLevel `json:"levels"`
	ConsumeMaterial bool        `json:"consumeMaterial"`
	MaterialItem    string      `json:"materialItem,omitempty"`
	LabelTemplate   string      `json:"labelTemplate,omitempty"`
	StackingLimit   int         `json:"stackingLimit,omitempty"`
}

// Validate checks the shape of a specification.
func (s PackagingSpec) Validate() error {
	if s.ID == "" {
		return fmt.Errorf("packaging spec id is required")
	}
	if (s.ItemID == "") == (s.ItemGroup == "") {
		return fmt.Errorf("packaging spec %q needs exactly one of item or item group", s.ID)
	}
	if len(s.Levels) == 0 {
		return fmt.Errorf("packaging spec %q needs at least one level", s.ID)
	}
	if s.ConsumeMaterial && s.MaterialItem == "" {
		return fmt.Errorf("packaging spec %q consumes material but names none", s.ID)
	}
	if s.StackingLimit < 0 {
		return fmt.Errorf("packaging spec %q stacking limit must not be negative", s.ID)
	}
	var prev decimal.Decimal
	for i, l := range s.Levels {
		if l.Name == "" || !l.QtyPerLevel.IsPositive() {
			return fmt.Errorf("packaging spec %q level %d needs a name and a positive quantity", s.ID, i)
		}
		if i > 0 && l.QtyPerLevel.LessThan(prev) {
			return fmt.Errorf("packaging spec %q level %s is smaller than the level below", s.ID, l.Name)
		}
		prev = l.QtyPerLevel
	}
	return nil
}

// Level returns the named packaging level.
func (s PackagingSpec) Level(name string) (PackLevel, bool) {
	for _, l := range s.Levels {
		if l.Name == name {
			return l, true
		}
	}
	return PackLevel{}, false
}

// matchRung returns the rung of the determination order (1 = most specific, 4 =
// least) that spec s occupies for the given item, group and customer; 0 is no match.
func matchRung(s PackagingSpec, itemID, itemGroup, customerID string) int {
	if itemID != "" && s.ItemID == itemID {
		if customerID != "" && s.CustomerID == customerID {
			return 1
		}
		if s.CustomerID == "" {
			return 2
		}
	}
	if itemGroup != "" && s.ItemGroup == itemGroup {
		if customerID != "" && s.CustomerID == customerID {
			return 3
		}
		if s.CustomerID == "" {
			return 4
		}
	}
	return 0
}

// DetermineSpec picks the packaging specification for a line by most-specific
// match: item+customer, item, item group+customer, item group (FR-31.17). Within
// one rung the lowest id wins, so the result is deterministic. ok is false when
// nothing matches; the caller decides whether packaging_spec_required blocks work.
func DetermineSpec(specs []PackagingSpec, itemID, itemGroup, customerID string) (PackagingSpec, bool) {
	var best PackagingSpec
	bestRung := 0
	for _, s := range specs {
		r := matchRung(s, itemID, itemGroup, customerID)
		if r == 0 {
			continue
		}
		if bestRung == 0 || r < bestRung || (r == bestRung && s.ID < best.ID) {
			best, bestRung = s, r
		}
	}
	return best, bestRung != 0
}

// RoundUpToLP rounds qty up to a whole number of the named level (the
// round_up_to_lp strategy). Quantity zero stays zero.
func RoundUpToLP(qty decimal.Decimal, spec PackagingSpec, level string) (decimal.Decimal, error) {
	l, ok := spec.Level(level)
	if !ok {
		return decimal.Zero, fmt.Errorf("packaging spec %q has no level %q", spec.ID, level)
	}
	if qty.IsNegative() {
		return decimal.Zero, fmt.Errorf("quantity must not be negative")
	}
	return qty.Div(l.QtyPerLevel).Ceil().Mul(l.QtyPerLevel), nil
}
