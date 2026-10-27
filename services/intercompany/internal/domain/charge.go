package domain

import (
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/money"
)

// ChargePricing is the priced intercompany charge (FR-40.13): the lending
// entity's cost, the price charged to the borrower and the IC margin.
type ChargePricing struct {
	Cost   money.Amount
	Price  money.Amount
	Margin money.Amount
}

// PriceCharge prices a cost-plus charge: cost = quantity × unit cost, price =
// cost × (1 + markup), margin = price − cost. Each amount rounds once.
func PriceCharge(quantity, unitCost, markupPct decimal.Decimal, currency string) (ChargePricing, error) {
	if len(currency) != 3 {
		return ChargePricing{}, invalid("currency must be an ISO 4217 code")
	}
	if !quantity.IsPositive() || unitCost.IsNegative() || markupPct.IsNegative() {
		return ChargePricing{}, invalid("quantity must be positive, unit cost and markup must not be negative")
	}
	cost := money.Amount{Value: quantity.Mul(unitCost), Currency: currency}.Round()
	price := cost.Mul(decimal.NewFromInt(1).Add(markupPct.Div(decimal.NewFromInt(100))))
	margin, err := price.Add(cost.Neg())
	if err != nil {
		return ChargePricing{}, err
	}
	return ChargePricing{Cost: cost, Price: price, Margin: margin}, nil
}
