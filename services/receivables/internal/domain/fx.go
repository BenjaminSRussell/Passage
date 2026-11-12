package domain

import (
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/money"
)

// Default realized FX accounts (BR-30.4); a legal entity overrides them in its FX setting.
const (
	AccountFXGain = "7100"
	AccountFXLoss = "7200"
)

// RealizedFX is the realized exchange difference of one cross-currency settlement, in the functional currency.
// PaymentValue is what the cash is worth on the value date, ItemValue what the settled part of the item was carried at when it was
// booked; Difference is PaymentValue less ItemValue, positive for a gain (more functional currency came in than was booked).
type RealizedFX struct {
	PaymentValue decimal.Decimal
	ItemValue    decimal.Decimal
	Difference   decimal.Decimal
}

// ComputeRealizedFX prices a settlement: cash is the amount applied in the payment currency, itemAmount what it took off the item in
// the item currency, paymentRate and itemRate convert those currencies to the functional one (the rate of the value date and of the
// item's document date). Both values are rounded to the functional currency's minor unit before they are compared, so the gain
// or loss is a whole number of minor units.
func ComputeRealizedFX(functional string, cash, paymentRate, itemAmount, itemRate decimal.Decimal) RealizedFX {
	mu := money.MinorUnits(functional)
	pv := cash.Mul(paymentRate).Round(mu)
	iv := itemAmount.Mul(itemRate).Round(mu)
	return RealizedFX{PaymentValue: pv, ItemValue: iv, Difference: pv.Sub(iv)}
}

// FXLines are the lines of the realized-FX journal in the functional currency: a gain credits the gain account and debits 1200
// (the cash was booked to 1200 at the payment-date value, above what the item was carried at), a loss does the opposite.
// It returns nil for a zero difference.
func FXLines(fx RealizedFX, gainAccount, lossAccount string) []Posting {
	switch {
	case fx.Difference.IsPositive():
		return []Posting{{Account: AccountReceivable, Debit: fx.Difference}, {Account: gainAccount, Credit: fx.Difference}}
	case fx.Difference.IsNegative():
		return []Posting{{Account: lossAccount, Debit: fx.Difference.Neg()}, {Account: AccountReceivable, Credit: fx.Difference.Neg()}}
	}
	return nil
}

// FXTolerance is how far a cross-currency item amount may be from what the cash or credit is worth in the item's currency: the
// larger of amount (in the item's currency) and percent of the item amount, rounded to cents. Without an entity setting
// (both nil) it is BR-30.4's write-off tolerance, the larger of 5.00 and 0.5%. A set value of zero is allowed: it demands
// the exact market value.
func FXTolerance(itemAmount decimal.Decimal, amount, percent *decimal.Decimal) decimal.Decimal {
	if amount == nil && percent == nil {
		return WriteOffTolerance(itemAmount)
	}
	floor, pct := decimal.Zero, decimal.Zero
	if amount != nil {
		floor = *amount
	}
	if percent != nil {
		pct = itemAmount.Abs().Mul(*percent).Div(decimal.NewFromInt(100)).Round(2)
	}
	return decimal.Max(floor, pct)
}
