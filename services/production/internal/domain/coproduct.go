package domain

import (
	"fmt"

	"github.com/shopspring/decimal"
)

// CodeCoProductExceeds is the error of a co-product RAF line over its planned quantity.
const CodeCoProductExceeds = "PRODUCTION_COPRODUCT_EXCEEDS_PLANNED"

// Types of an output line besides the product (FR-21.14, BR-9.12): a co-product
// takes its share of the order's cost by quantity; a by-product carries no cost
// allocation and is received at zero cost (its burden methods arrive in wave 24).
const (
	CoProductTypeCo = "co-product"
	CoProductTypeBy = "by-product"
)

// CoProduct is a co-product or by-product declared on an order (FR-21.14): an
// item the order yields besides its product, QtyPer per unit of the product.
type CoProduct struct {
	LineNo      string          `json:"lineNo"`
	Type        string          `json:"type"`
	ItemID      string          `json:"itemId"`
	QtyPer      decimal.Decimal `json:"qtyPer"`
	QtyPlanned  decimal.Decimal `json:"qtyPlanned"`
	QtyReported decimal.Decimal `json:"qtyReported"`
}

// PlanCoProducts sets the planned quantity of each declared co-product for orderQty
// units of the product (rounded up to the quantity scale, as BR-21.2).
func PlanCoProducts(cos []CoProduct, orderQty decimal.Decimal) ([]CoProduct, error) {
	if !orderQty.IsPositive() {
		return nil, &Error{Code: CodeInvalidInput, Detail: "order quantity must be positive"}
	}
	out := make([]CoProduct, len(cos))
	seen := map[string]bool{}
	for i, c := range cos {
		if c.LineNo == "" || seen[c.LineNo] {
			return nil, &Error{Code: CodeInvalidInput, Detail: fmt.Sprintf("co-product line %q is empty or listed twice", c.LineNo)}
		}
		seen[c.LineNo] = true
		if c.Type == "" {
			c.Type = CoProductTypeCo
		}
		if c.Type != CoProductTypeCo && c.Type != CoProductTypeBy {
			return nil, &Error{Code: CodeInvalidInput, Detail: fmt.Sprintf("co-product line %s: type must be co-product or by-product", c.LineNo)}
		}
		if !c.QtyPer.IsPositive() {
			return nil, &Error{Code: CodeInvalidInput, Detail: fmt.Sprintf("co-product line %s: quantity per unit must be positive", c.LineNo)}
		}
		c.QtyPlanned, c.QtyReported = CeilQty(c.QtyPer.Mul(orderQty)), decimal.Zero
		out[i] = c
	}
	return out, nil
}

// CarriesCost reports whether the line takes a share of the order's cost: a
// co-product does, a by-product does not (BR-9.12).
func (c CoProduct) CarriesCost() bool { return c.Type != CoProductTypeBy }

// CostBase is the quantity over which an order's planned cost is spread when it
// also yields co-products: the product's order quantity plus every co-product's
// planned quantity (FR-21.14 allocates by quantity only, so every output unit
// carries the same planned cost; unit-of-measure conversion arrives with wave 24).
// By-products are left out: they carry no cost.
func CostBase(orderQty decimal.Decimal, cos []CoProduct) decimal.Decimal {
	base := orderQty
	for _, c := range cos {
		if c.CarriesCost() {
			base = base.Add(c.QtyPlanned)
		}
	}
	return base
}

// SettleBasis returns the order and reported quantities Settle works with: the
// product's plus the co-products', so the share of planned cost earned by output
// is the share of the cost base reported. By-products count in neither.
func SettleBasis(orderQty, rafQty decimal.Decimal, cos []CoProduct) (decimal.Decimal, decimal.Decimal) {
	reported := rafQty
	for _, c := range cos {
		if c.CarriesCost() {
			reported = reported.Add(c.QtyReported)
		}
	}
	return CostBase(orderQty, cos), reported
}

// CheckCoProductReport refuses a co-product receipt beyond the planned quantity
// plus the RAF tolerance (FR-21.7 applied to FR-21.14).
func CheckCoProductReport(c CoProduct, qty, tolerancePct decimal.Decimal) error {
	if !qty.IsPositive() {
		return &Error{Code: CodeInvalidInput, Detail: fmt.Sprintf("co-product line %s: quantity must be positive", c.LineNo)}
	}
	limit := c.QtyPlanned.Mul(one.Add(tolerancePct.Div(hundred)))
	if c.QtyReported.Add(qty).GreaterThan(limit) {
		return &Error{Code: CodeCoProductExceeds, Detail: fmt.Sprintf("co-product line %s: %s reported plus %s exceeds planned %s plus tolerance %s%%",
			c.LineNo, c.QtyReported, qty, c.QtyPlanned, tolerancePct)}
	}
	return nil
}

// ProportionalCoProduct is the quantity of a co-product still due when the product
// has reported qtyAfter of orderQty: its planned quantity scaled by that share,
// less what was reported, never negative.
func ProportionalCoProduct(c CoProduct, orderQty, qtyAfter decimal.Decimal) decimal.Decimal {
	due := c.QtyPlanned.Mul(qtyAfter).Div(orderQty).Round(QtyScale).Sub(c.QtyReported)
	if due.IsNegative() {
		return decimal.Zero
	}
	return due
}
