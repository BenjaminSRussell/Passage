package domain

import (
	"fmt"

	"github.com/shopspring/decimal"
)

// PricingPolicy is how intercompany lines are priced (FR-40.1, FR-40.4).
type PricingPolicy string

const (
	PricePriceList PricingPolicy = "PriceList"
	PriceCostPlus  PricingPolicy = "CostPlus"
	PriceTable     PricingPolicy = "TransferPriceTable"
)

// ActionPolicy holds the FR-40.1 action flags for one pairing.
type ActionPolicy struct {
	AutoCreate          bool
	AutoConfirm         bool
	AutoPostPackingSlip bool
	AutoPostInvoice     bool
}

// Tolerance is the BR-40.3 change tolerance for a linked sales order. Quantity
// changes are never tolerated by default.
type Tolerance struct {
	PricePct decimal.Decimal
	DateDays int
}

// DefaultTolerance is the default policy: price ±0.5 %, date ±2 days.
func DefaultTolerance() Tolerance {
	return Tolerance{PricePct: decimal.New(5, -1), DateDays: 2}
}

// Partner pairs a seller entity (customer side) with a buyer entity (vendor side).
type Partner struct {
	SellerEntity string
	BuyerEntity  string
	Active       bool
	Actions      ActionPolicy
	Pricing      PricingPolicy
	MarkupPct    decimal.Decimal
	Tolerance    Tolerance
}

// ValidatePartner checks a pairing before it is stored.
func ValidatePartner(p Partner) error {
	switch {
	case p.SellerEntity == "" || p.BuyerEntity == "":
		return invalid("seller and buyer entities are required")
	case p.SellerEntity == p.BuyerEntity:
		return invalid("seller and buyer must be different entities")
	case p.Pricing != PricePriceList && p.Pricing != PriceCostPlus && p.Pricing != PriceTable:
		return invalid("unknown pricing policy " + string(p.Pricing))
	case p.MarkupPct.IsNegative():
		return invalid("markup must not be negative")
	case p.Tolerance.PricePct.IsNegative() || p.Tolerance.PricePct.GreaterThan(decimal.NewFromInt(100)):
		return invalid("price tolerance must be between 0 and 100 percent")
	case p.Tolerance.DateDays < 0 || p.Tolerance.DateDays > 30:
		return invalid("date tolerance must be 0 to 30 days")
	}
	return nil
}

// ActivePartner returns the single active pairing for the direction seller to
// buyer. One active pairing per entity pair and direction is an invariant.
func ActivePartner(partners []Partner, seller, buyer string) (Partner, error) {
	var found []Partner
	for _, p := range partners {
		if p.Active && p.SellerEntity == seller && p.BuyerEntity == buyer {
			found = append(found, p)
		}
	}
	switch len(found) {
	case 1:
		return found[0], nil
	case 0:
		return Partner{}, &Error{Code: CodeNoPolicy, Detail: fmt.Sprintf("no active IC partner policy between %s and %s", seller, buyer)}
	default:
		return Partner{}, invalid(fmt.Sprintf("%d active pairings exist for %s to %s", len(found), seller, buyer))
	}
}

// PriceLine prices one intercompany line (FR-40.4): the list price for
// PriceList, unit cost plus markup for CostPlus. A transfer price table is a
// later slice and fails with IC-0821.
func PriceLine(p Partner, listPrice, unitCost decimal.Decimal) (decimal.Decimal, error) {
	switch p.Pricing {
	case PricePriceList:
		return listPrice, nil
	case PriceCostPlus:
		return unitCost.Mul(decimal.NewFromInt(1).Add(p.MarkupPct.Div(decimal.NewFromInt(100)))), nil
	}
	return decimal.Zero, &Error{Code: CodeTransferPrice, Detail: "no transfer price for this line"}
}

// WithinTolerance reports whether a change to a linked sales order updates the
// mirror silently (BR-40.3). Quantity must be unchanged; price may move by the
// tolerance percentage and the date by the tolerance days.
func WithinTolerance(t Tolerance, oldQty, newQty, oldPrice, newPrice decimal.Decimal, dateShiftDays int) bool {
	if !oldQty.Equal(newQty) {
		return false
	}
	if oldPrice.IsZero() {
		if !newPrice.IsZero() {
			return false
		}
	} else {
		move := oldPrice.Sub(newPrice).Abs().Div(oldPrice).Mul(decimal.NewFromInt(100))
		if move.GreaterThan(t.PricePct) {
			return false
		}
	}
	if dateShiftDays < 0 {
		dateShiftDays = -dateShiftDays
	}
	return dateShiftDays <= t.DateDays
}
