package domain

import (
	"fmt"
	"time"

	"github.com/shopspring/decimal"
)

// Rebate bases and accrual periods (FR-26.11).
const (
	BasisNetAmount = "NET_AMOUNT"
	BasisQuantity  = "QUANTITY"
	PeriodMonth    = "MONTH"
	PeriodQuarter  = "QUARTER"
	PeriodYear     = "YEAR"
)

// RebateAgreement is a rebate agreement. A CUSTOMER agreement names exactly one of
// CustomerID and CustomerGroupID; a VENDOR agreement (FR-26.15) names VendorID only.
// Tiers pay RatePct of net value (NET_AMOUNT: invoiced net, or vendor-invoiced net) or
// AmountPerUnit per unit (QUANTITY: invoiced, or received, quantity).
type RebateAgreement struct {
	ID              string
	LegalEntityID   string
	Name            string
	PartyRole       string
	CustomerID      string
	CustomerGroupID string
	VendorID        string
	// ProductID or ProductGroupID (at most one) limits the agreement to invoice lines of that
	// product or product group; both empty means every line (FR-26.11).
	ProductID      string
	ProductGroupID string
	Basis          string
	Period         string
	Tiers          []Tier
	Currency       string
	ValidFrom      time.Time
	ValidTo        time.Time
	AccrualAccount string
	ExpenseAccount string
	// Status is ACTIVE or CLOSED; Version is the optimistic lock (1 on create, +1 per revise or close).
	Status  string
	Version int
}

// Rebate agreement statuses and the problem codes of the lifecycle (FR-26.11).
const (
	AgreementActive = "ACTIVE"
	AgreementClosed = "CLOSED"
	// CodeAgreementEndsEarly: the end date falls before an invoice already accrued.
	CodeAgreementEndsEarly = "PRC-0048"
	// CodeOutOfProductScope: the invoice line is not of the agreement's product or product group.
	CodeOutOfProductScope = "PRC-0070"
	// CodeAgreementHasAccruals: tiers cannot change once the agreement has accruals.
	CodeAgreementHasAccruals = "PRC-0049"
)

// ValidateAgreement checks an agreement before it is stored (FR-26.11, FR-26.15, BR-26.8).
func ValidateAgreement(a RebateAgreement) *Error {
	fields := map[string]string{}
	switch PartyRoleOf(a) {
	case PartyCustomer:
		if a.VendorID != "" {
			fields["vendorId"] = "a customer rebate agreement takes no vendorId"
		}
		if (a.CustomerID == "") == (a.CustomerGroupID == "") {
			fields["customer"] = "exactly one of customerId or customerGroupId is required"
		}
	case PartyVendor:
		if a.VendorID == "" {
			fields["vendorId"] = "a vendor rebate agreement requires vendorId"
		}
		if a.CustomerID != "" || a.CustomerGroupID != "" {
			fields["customer"] = "a vendor rebate agreement takes no customerId or customerGroupId"
		}
	default:
		fields["partyRole"] = "must be CUSTOMER or VENDOR"
	}
	if a.ProductID != "" && a.ProductGroupID != "" {
		fields["productScope"] = "at most one of productId or productGroupId"
	}
	if a.Basis != BasisNetAmount && a.Basis != BasisQuantity {
		fields["basis"] = "must be NET_AMOUNT or QUANTITY"
	}
	switch a.Period {
	case PeriodMonth, PeriodQuarter, PeriodYear:
	default:
		fields["period"] = "must be MONTH, QUARTER or YEAR"
	}
	if len(a.Currency) != 3 {
		fields["currency"] = "must be an ISO 4217 code"
	}
	if a.ValidFrom.IsZero() || a.ValidTo.IsZero() || dayOf(a.ValidFrom) > dayOf(a.ValidTo) {
		fields["validity"] = "validFrom must be on or before validTo"
	}
	if len(a.Tiers) == 0 {
		fields["tiers"] = "at least one tier is required"
	}
	for i, t := range a.Tiers {
		if t.From.IsNegative() {
			fields["tiers"] = fmt.Sprintf("tier %d: threshold must not be negative", i+1)
		}
		if i > 0 && !t.From.GreaterThan(a.Tiers[i-1].From) {
			fields["tiers"] = fmt.Sprintf("tier %d: thresholds must be strictly increasing", i+1)
		}
		switch a.Basis {
		case BasisNetAmount:
			if !t.RatePct.IsPositive() || t.RatePct.GreaterThan(decimal.NewFromInt(100)) || !t.AmountPerUnit.IsZero() {
				fields["tiers"] = fmt.Sprintf("tier %d: NET_AMOUNT tiers take a rate from 0 to 100 and no amount per unit", i+1)
			}
		case BasisQuantity:
			if !t.AmountPerUnit.IsPositive() || !t.RatePct.IsZero() {
				fields["tiers"] = fmt.Sprintf("tier %d: QUANTITY tiers take an amount per unit and no rate", i+1)
			}
		}
	}
	if len(fields) == 0 {
		return nil
	}
	return &Error{Code: CodeValidation, Detail: "rebate agreement is invalid", Fields: fields}
}

// PeriodKey names the accrual period a posting date falls in: 2026-10, 2026-Q4 or 2026.
func PeriodKey(period string, day time.Time) string {
	switch period {
	case PeriodQuarter:
		return fmt.Sprintf("%04d-Q%d", day.Year(), (int(day.Month())-1)/3+1)
	case PeriodYear:
		return fmt.Sprintf("%04d", day.Year())
	}
	return fmt.Sprintf("%04d-%02d", day.Year(), int(day.Month()))
}

// PeriodTarget is the accrual a period owes at its cumulative basis (BR-26.8). The tier
// in force at the cumulative value (NET_AMOUNT) or quantity (QUANTITY) pays its rate on
// the cumulative value or its amount per unit on the cumulative quantity. Rounded once.
// A credit that drops the period below every threshold reduces the target, and the
// difference is posted as a delta.
func PeriodTarget(a RebateAgreement, cumValue, cumQty decimal.Decimal) decimal.Decimal {
	if a.Basis == BasisQuantity {
		t, ok := TierFor(a.Tiers, cumQty)
		if !ok {
			return decimal.Zero
		}
		return roundMinor(cumQty.Mul(t.AmountPerUnit), a.Currency)
	}
	t, ok := TierFor(a.Tiers, cumValue)
	if !ok {
		return decimal.Zero
	}
	return roundMinor(cumValue.Mul(t.RatePct).Shift(-2), a.Currency)
}

// PeriodState is the cumulative basis and the accrual already booked for one agreement
// in one period.
type PeriodState struct {
	Value   decimal.Decimal
	Qty     decimal.Decimal
	Accrued decimal.Decimal
}

// Accrue adds one invoice line to a period and returns the new state and the amount
// this invoice accrues (FR-26.11, BR-26.8). The amount is the period target at the new
// cumulative basis less what the period already accrued, so when the cumulative basis
// crosses a tier the whole period is recomputed at the new rate and the delta lands on
// this invoice. A credit (negative value or quantity) can produce a negative accrual.
// The amounts accrued in a period always sum to PeriodTarget at its cumulative basis.
func Accrue(a RebateAgreement, prior PeriodState, value, qty decimal.Decimal) (PeriodState, decimal.Decimal) {
	next := PeriodState{Value: prior.Value.Add(value), Qty: prior.Qty.Add(qty)}
	next.Accrued = PeriodTarget(a, next.Value, next.Qty)
	return next, next.Accrued.Sub(prior.Accrued)
}

// InProductScope reports whether an invoice line of productID in productGroupID falls in the
// agreement's product scope. An agreement without a scope takes every line; a scoped one takes
// only a line that names the same product (or product group), so a line that omits the key
// does not match (FR-26.11).
func InProductScope(a RebateAgreement, productID, productGroupID string) bool {
	switch {
	case a.ProductID != "":
		return productID == a.ProductID
	case a.ProductGroupID != "":
		return productGroupID == a.ProductGroupID
	}
	return true
}

// ScopeDescription names the agreement's product scope for a refusal message.
func ScopeDescription(a RebateAgreement) string {
	if a.ProductID != "" {
		return "product " + a.ProductID
	}
	return "product group " + a.ProductGroupID
}
