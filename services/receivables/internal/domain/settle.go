package domain

import (
	"fmt"
	"net/http"
	"time"

	"github.com/shopspring/decimal"
)

// Error codes from the wave 30 message catalog (§5.30.18).
const (
	CodeApplyMismatch     = "AR-0001"
	CodeItemLocked        = "AR-0002"
	CodeWriteOffTolerance = "AR-0006"
)

// Error is a domain rule violation with a stable catalog code.
type Error struct {
	Status int
	Code   string
	Detail string
}

func (e *Error) Error() string { return fmt.Sprintf("%s: %s", e.Code, e.Detail) }

func applyErr(detail string) *Error {
	return &Error{Status: http.StatusBadRequest, Code: CodeApplyMismatch, Detail: detail}
}

// Allocation is one settlement of a payment against one open item. Open is the
// item's open amount read under lock. Amount is cash applied; CashDiscount and
// WriteOff also reduce the item.
type Allocation struct {
	OpenItemID   string
	Open         decimal.Decimal
	Amount       decimal.Decimal
	CashDiscount decimal.Decimal
	WriteOff     decimal.Decimal
	// ItemAmount is the item's original amount; the write-off tolerance is a
	// share of it (BR-30.4). Zero falls back to Open.
	ItemAmount decimal.Decimal
	// DiscountDate is the last day the cash discount is available, nil when none.
	DiscountDate *time.Time
	// DiscountAvailable is the item's cash discount amount (terms); a settlement
	// may not take more.
	DiscountAvailable decimal.Decimal
	// ItemCurrencyAmount is set for a cross-currency settlement (an item in another currency than the payment): what the
	// allocation takes off the item, in the item's currency. Amount is then the cash in the payment currency. Such an
	// allocation carries no cash discount and no write-off.
	ItemCurrencyAmount decimal.Decimal
}

// CrossCurrency reports whether the allocation settles an item in another currency than the payment.
func (a Allocation) CrossCurrency() bool { return a.ItemCurrencyAmount.IsPositive() }

// ReductionOf is what the allocation takes off the open item, in the item's currency.
func (a Allocation) ReductionOf() decimal.Decimal {
	if a.CrossCurrency() {
		return a.ItemCurrencyAmount
	}
	return a.Amount.Add(a.CashDiscount).Add(a.WriteOff)
}

// NewOpen is the open amount left after the allocation.
func (a Allocation) NewOpen() decimal.Decimal { return a.Open.Sub(a.ReductionOf()) }

// WriteOffTolerance is BR-30.4: the larger of 5.00 and 0.5% of the item.
func WriteOffTolerance(open decimal.Decimal) decimal.Decimal {
	floor := decimal.RequireFromString("5.00")
	pct := open.Abs().Mul(decimal.RequireFromString("0.005")).Round(2)
	if pct.GreaterThan(floor) {
		return pct
	}
	return floor
}

// ValidateApply enforces the apply rules: settlements plus residual equal the
// unapplied payment amount (AR-0001), no item is over-settled, cash discounts
// are only taken when the value date is on or before the discount date, and
// write-offs stay within tolerance (AR-0006). It is pure: callers supply the
// open amounts read under lock.
func ValidateApply(valueDate time.Time, unapplied decimal.Decimal, allocs []Allocation, residual decimal.Decimal) error {
	if residual.IsNegative() {
		return applyErr("on-account residual must not be negative")
	}
	total := residual
	seen := map[string]bool{}
	for _, a := range allocs {
		if a.OpenItemID == "" {
			return applyErr("every settlement needs an open item")
		}
		if seen[a.OpenItemID] {
			return applyErr("open item " + a.OpenItemID + " appears twice")
		}
		seen[a.OpenItemID] = true
		if a.Amount.IsNegative() || a.CashDiscount.IsNegative() || a.WriteOff.IsNegative() {
			return applyErr("settlement amounts must not be negative")
		}
		if a.CrossCurrency() && (a.CashDiscount.IsPositive() || a.WriteOff.IsPositive()) {
			return applyErr("settlement " + a.OpenItemID + " is in another currency than the payment and takes no cash discount or write-off")
		}
		if a.CrossCurrency() && !a.Amount.IsPositive() {
			return applyErr("settlement " + a.OpenItemID + " needs the cash applied in the payment currency")
		}
		if !a.ReductionOf().IsPositive() {
			return applyErr("settlement " + a.OpenItemID + " moves no money")
		}
		if a.ReductionOf().GreaterThan(a.Open) {
			return applyErr("settlement " + a.OpenItemID + " exceeds its open amount")
		}
		if a.CashDiscount.IsPositive() && (a.DiscountDate == nil || valueDate.After(*a.DiscountDate)) {
			return applyErr("cash discount on " + a.OpenItemID + " is not available on the value date")
		}
		if a.CashDiscount.GreaterThan(a.DiscountAvailable) {
			return applyErr(fmt.Sprintf("cash discount %s on %s exceeds the %s the terms allow", a.CashDiscount, a.OpenItemID, a.DiscountAvailable))
		}
		base := a.ItemAmount
		if !base.IsPositive() {
			base = a.Open
		}
		if tol := WriteOffTolerance(base); a.WriteOff.GreaterThan(tol) {
			return &Error{Status: http.StatusUnprocessableEntity, Code: CodeWriteOffTolerance,
				Detail: fmt.Sprintf("write-off %s exceeds tolerance %s", a.WriteOff, tol)}
		}
		total = total.Add(a.Amount)
	}
	if !total.Equal(unapplied) {
		return applyErr(fmt.Sprintf("settlements plus residual %s must equal the payment amount %s", total, unapplied))
	}
	return nil
}

// ApplyStatus is the open item status after a settlement (BR-30.4).
func ApplyStatus(newOpen decimal.Decimal) string {
	if newOpen.IsZero() {
		return "SETTLED"
	}
	return "PARTIALLY_SETTLED"
}
