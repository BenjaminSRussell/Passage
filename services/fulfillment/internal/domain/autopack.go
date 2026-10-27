package domain

import (
	"errors"

	"github.com/shopspring/decimal"
)

var (
	// ErrContainerWeight means one unit alone exceeds the rule's max weight (FUL-0013).
	ErrContainerWeight = errors.New("container max weight exceeded by a single unit")
	// ErrSerialFraction means a serial-tracked line has a fractional quantity.
	ErrSerialFraction = errors.New("serial-tracked units cannot be split")
	// ErrRuleInvalid means a packing rule has a non-positive quantity or weight.
	ErrRuleInvalid = errors.New("packing rule quantities must be positive")
)

// PackRule decides the container for a product (FR-28.15). A customer-specific
// rule wins over the general rule for the product (BR-26.1 style).
type PackRule struct {
	ProductID       string
	CustomerID      string // empty applies to every customer
	ContainerType   string
	QtyPerContainer decimal.Decimal
	MaxWeightKg     decimal.Decimal
	TareKg          decimal.Decimal
}

// PackLine is a picked, not yet packed quantity to place into containers.
type PackLine struct {
	ShipmentLineID string
	ProductID      string
	Qty            decimal.Decimal
	UnitWeightKg   decimal.Decimal
	Serial         bool
}

// PlannedLine is the quantity of one shipment line placed in a planned container.
type PlannedLine struct {
	ShipmentLineID string
	Qty            decimal.Decimal
}

// PlannedContainer is one container the auto-pack proposes.
type PlannedContainer struct {
	ContainerType string
	TareKg        decimal.Decimal
	GrossKg       decimal.Decimal
	Lines         []PlannedLine
}

// AutoPackPlan is the proposal: containers in creation order and the lines
// that no rule covers, which stay unpacked and are flagged (FUL-0012).
type AutoPackPlan struct {
	Containers []PlannedContainer
	Unpacked   []string
}

type ruleKey struct{ product, customer string }

type box struct {
	PlannedContainer
	units  decimal.Decimal
	weight decimal.Decimal
}

func (b *box) add(lineID string, qty, unitKg decimal.Decimal) {
	b.Lines = append(b.Lines, PlannedLine{ShipmentLineID: lineID, Qty: qty})
	b.units = b.units.Add(qty)
	b.weight = b.weight.Add(qty.Mul(unitKg))
}

func (r PackRule) validate() error {
	if r.ContainerType == "" || !r.QtyPerContainer.IsPositive() || !r.MaxWeightKg.IsPositive() || r.TareKg.IsNegative() {
		return ErrRuleInvalid
	}
	return nil
}

// SelectRule returns the rule for a product and customer, preferring a rule
// scoped to that customer over the product's general rule.
func SelectRule(rules []PackRule, productID, customerID string) (PackRule, bool) {
	var general PackRule
	found := false
	for _, r := range rules {
		if r.ProductID != productID {
			continue
		}
		if r.CustomerID != "" && r.CustomerID == customerID {
			return r, true
		}
		if r.CustomerID == "" {
			general, found = r, true
		}
	}
	return general, found
}

// AutoPack places picked lines into containers (BR-28.11). Lines with a more
// specific rule are placed first. A container closes when its quantity per
// container or its max weight is reached; serial-tracked units are never split.
// Lines without a rule are returned as Unpacked.
func AutoPack(lines []PackLine, rules []PackRule, customerID string) (AutoPackPlan, error) {
	type todo struct {
		line     PackLine
		rule     PackRule
		specific bool
	}
	var plan AutoPackPlan
	var work []todo
	for _, l := range lines {
		if !l.Qty.IsPositive() {
			continue
		}
		r, ok := SelectRule(rules, l.ProductID, customerID)
		if !ok {
			plan.Unpacked = append(plan.Unpacked, l.ShipmentLineID)
			continue
		}
		if err := r.validate(); err != nil {
			return AutoPackPlan{}, err
		}
		if l.Serial && !l.Qty.Equal(l.Qty.Floor()) {
			return AutoPackPlan{}, ErrSerialFraction
		}
		work = append(work, todo{line: l, rule: r, specific: r.CustomerID != ""})
	}
	// Most specific rule first; the input order is kept within each group.
	stableSort(work, func(a, b todo) bool { return a.specific && !b.specific })

	open := map[ruleKey]*box{}
	var boxes []*box
	for _, t := range work {
		key := ruleKey{t.rule.ProductID, t.rule.CustomerID}
		remaining := t.line.Qty
		for remaining.IsPositive() {
			b := open[key]
			if b == nil {
				b = &box{PlannedContainer: PlannedContainer{
					ContainerType: t.rule.ContainerType,
					TareKg:        t.rule.TareKg,
					Lines:         []PlannedLine{},
				}, weight: t.rule.TareKg}
				open[key] = b
				boxes = append(boxes, b)
			}
			take := minDec(remaining, t.rule.QtyPerContainer.Sub(b.units))
			if t.line.UnitWeightKg.IsPositive() {
				room := t.rule.MaxWeightKg.Sub(b.weight).Div(t.line.UnitWeightKg).Floor()
				take = minDec(take, room)
			}
			if t.line.Serial {
				take = take.Floor()
			}
			if !take.IsPositive() {
				if b.units.IsZero() {
					return AutoPackPlan{}, ErrContainerWeight
				}
				delete(open, key) // full: close it and start a new container
				continue
			}
			b.add(t.line.ShipmentLineID, take, t.line.UnitWeightKg)
			remaining = remaining.Sub(take)
		}
	}
	for _, b := range boxes {
		b.GrossKg = b.weight
		plan.Containers = append(plan.Containers, b.PlannedContainer)
	}
	return plan, nil
}

func minDec(a, b decimal.Decimal) decimal.Decimal {
	if a.LessThan(b) {
		return a
	}
	return b
}

// stableSort is an insertion sort: stable and dependency-free, and the inputs are short.
func stableSort[T any](xs []T, less func(a, b T) bool) {
	for i := 1; i < len(xs); i++ {
		for j := i; j > 0 && less(xs[j], xs[j-1]); j-- {
			xs[j], xs[j-1] = xs[j-1], xs[j]
		}
	}
}
