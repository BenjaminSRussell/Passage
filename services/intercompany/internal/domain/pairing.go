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

// Partner pairs a seller entity (customer side) with a buyer entity (vendor
// side): CustomerAccount is the IC customer in the seller entity that stands
// for the buyer, VendorAccount the IC vendor in the buyer entity that stands for
// the seller (FR-40.1).
type Partner struct {
	SellerEntity    string
	BuyerEntity     string
	CustomerAccount string
	VendorAccount   string
	Active          bool
	Actions         ActionPolicy
	Pricing         PricingPolicy
	MarkupPct       decimal.Decimal
	Tolerance       Tolerance
}

// maxMarkupPct bounds the cost-plus markup of a pairing.
var maxMarkupPct = decimal.NewFromInt(1000)

// ValidatePartner checks the pricing and tolerance rules of a pairing.
func ValidatePartner(p Partner) error {
	switch {
	case p.SellerEntity == "" || p.BuyerEntity == "":
		return invalid("seller and buyer entities are required")
	case p.SellerEntity == p.BuyerEntity:
		return invalid("seller and buyer must be different entities")
	case p.Pricing != PricePriceList && p.Pricing != PriceCostPlus && p.Pricing != PriceTable:
		return invalid("unknown pricing policy " + string(p.Pricing))
	case p.MarkupPct.IsNegative() || p.MarkupPct.GreaterThan(maxMarkupPct):
		return invalid("markup must be between 0 and 1000 percent")
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
