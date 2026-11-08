package domain

import (
	"fmt"

	"github.com/shopspring/decimal"
)

// Partial delivery policies of a customer (PRD §5.27.13 partial_delivery_default).
const (
	PartialAllowed      = "ALLOWED"
	PartialCompleteOnly = "COMPLETE_ONLY"
	PartialOneShipment  = "ONE_SHIPMENT"
)

// Sources of a resolved delivery tolerance.
const (
	ToleranceCustomer     = "CUSTOMER"
	ToleranceAllCustomers = "ALL_CUSTOMERS"
	ToleranceDefault      = "DEFAULT"
)

// DeliveryTolerance is how far a shipment may differ from the ordered quantity
// and whether the order may ship in parts: over-delivery and under-delivery in
// percent of the ordered quantity (0 to 100, 2 places) and the partial delivery
// policy (BR-27.9, BR-28.7, FR-27.6).
type DeliveryTolerance struct {
	OverdeliveryPct  decimal.Decimal
	UnderdeliveryPct decimal.Decimal
	PartialDelivery  string
}

// DefaultTolerance is what a product and customer without a rule get: exact
// quantities, partial shipments allowed.
func DefaultTolerance() DeliveryTolerance {
	return DeliveryTolerance{PartialDelivery: PartialAllowed}
}

// ValidPartialDelivery reports whether p names a partial delivery policy.
func ValidPartialDelivery(p string) bool {
	switch p {
	case PartialAllowed, PartialCompleteOnly, PartialOneShipment:
		return true
	}
	return false
}

func validPct(p decimal.Decimal) bool {
	return !p.IsNegative() && !p.GreaterThan(decimal.NewFromInt(100)) && p.Round(2).Equal(p)
}

// Validate returns the field errors of the tolerance keyed by JSON name.
func (t DeliveryTolerance) Validate() map[string]string {
	f := map[string]string{}
	if !validPct(t.OverdeliveryPct) {
		f["overdeliveryPct"] = "must be between 0 and 100 with at most 2 decimal places"
	}
	if !validPct(t.UnderdeliveryPct) {
		f["underdeliveryPct"] = "must be between 0 and 100 with at most 2 decimal places"
	}
	if !ValidPartialDelivery(t.PartialDelivery) {
		f["partialDelivery"] = fmt.Sprintf("must be %s, %s or %s", PartialAllowed, PartialCompleteOnly, PartialOneShipment)
	}
	return f
}

// Range is the quantity a shipment of an ordered quantity may carry and still
// count as complete: no less than ordered less the under-delivery tolerance and
// no more than ordered plus the over-delivery tolerance, both cut to 6 places
// the way the agreement ceiling is (the upper bound never above the exact
// figure, the lower bound never below it).
func (t DeliveryTolerance) Range(ordered decimal.Decimal) (min, max decimal.Decimal) {
	hundred := decimal.NewFromInt(100)
	max = ordered.Mul(hundred.Add(t.OverdeliveryPct)).Div(hundred).Truncate(6)
	min = ordered.Mul(hundred.Sub(t.UnderdeliveryPct)).Div(hundred).RoundCeil(6)
	return min, max
}

// ToleranceRule is a stored rule for one product: CustomerID nil applies to
// every customer of the legal entity.
type ToleranceRule struct {
	ID         string
	CustomerID *string
	DeliveryTolerance
}

// ResolveTolerance picks the rule that governs customerID: its own rule
// replaces the all-customers rule (as for allocation quotas and listings), and
// without either the default applies. It returns the rule id ("" for the
// default) and the source.
func ResolveTolerance(rules []ToleranceRule, customerID string) (DeliveryTolerance, string, string) {
	var all *ToleranceRule
	for i := range rules {
		r := rules[i]
		switch {
		case r.CustomerID != nil && *r.CustomerID == customerID:
			return r.DeliveryTolerance, r.ID, ToleranceCustomer
		case r.CustomerID == nil && all == nil:
			all = &rules[i]
		}
	}
	if all != nil {
		return all.DeliveryTolerance, all.ID, ToleranceAllCustomers
	}
	return DefaultTolerance(), "", ToleranceDefault
}
