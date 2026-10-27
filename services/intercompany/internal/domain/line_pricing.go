package domain

import (
	"time"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/money"
)

// CategoryItem is the transfer-price category of intercompany sales lines; the
// resource role of the row carries the item number (FR-40.4, FR-40.13).
const CategoryItem = "Item"

// Pricing basis sources recorded on the link (FR-40.4).
const (
	SourcePartnerPolicy = "partner-policy"
	SourceTransferTable = "transfer-price-table"
)

// LineInput is one intercompany line to price. Prices are per unit in Currency.
type LineInput struct {
	Item      string
	Currency  string
	On        time.Time
	Quantity  decimal.Decimal
	ListPrice decimal.Decimal
	UnitCost  decimal.Decimal
}

// PricingBasis records how a line was priced. It is what the IC document link
// keeps as pricing_basis and what the transfer-pricing evidence exports.
type PricingBasis struct {
	Policy        PricingPolicy
	Method        string // PriceList, Cost, CostPlus or Table
	MarkupPct     decimal.Decimal
	Source        string // partner-policy or transfer-price-table
	EffectiveFrom time.Time
}

// LinePrice is the resolved unit price and extended amount of one IC line.
type LinePrice struct {
	UnitPrice decimal.Decimal
	Extended  money.Amount
	Basis     PricingBasis
}

// ResolveLinePrice prices one intercompany line from the pairing's pricing
// policy (FR-40.4): PriceList takes the agreed list price, CostPlus the unit
// cost with the pairing's markup, and TransferPriceTable the row for the item
// effective on the line date (BR-40.12; none fails with IC-0821). The unit
// price is kept at full precision and the extended amount is rounded once to
// the currency minor unit.
func ResolveLinePrice(p Partner, table []TransferPrice, in LineInput) (LinePrice, error) {
	switch {
	case len(in.Currency) != 3:
		return LinePrice{}, invalid("line currency must be an ISO 4217 code")
	case in.Item == "":
		return LinePrice{}, invalid("item is required")
	case !in.Quantity.IsPositive():
		return LinePrice{}, invalid("quantity must be above 0")
	case in.ListPrice.IsNegative(), in.UnitCost.IsNegative():
		return LinePrice{}, invalid("prices must not be negative")
	}
	basis := PricingBasis{Policy: p.Pricing, Source: SourcePartnerPolicy}
	var unit decimal.Decimal
	switch p.Pricing {
	case PricePriceList:
		unit = in.ListPrice
		basis.Method = string(PricePriceList)
	case PriceCostPlus:
		unit = withMarkup(in.UnitCost, p.MarkupPct)
		basis.Method = TPCostPlus
		basis.MarkupPct = p.MarkupPct
	case PriceTable:
		tp, err := ResolveTransferPrice(table, p.SellerEntity, p.BuyerEntity, CategoryItem, in.Item, in.On)
		if err != nil {
			return LinePrice{}, err
		}
		if tp.Currency != in.Currency {
			return LinePrice{}, invalid("transfer price currency " + tp.Currency + " does not match the line currency " + in.Currency)
		}
		basis.Source = SourceTransferTable
		basis.Method = tp.Method
		basis.EffectiveFrom = tp.EffectiveFrom
		switch tp.Method {
		case TPCost:
			unit = in.UnitCost
		case TPCostPlus:
			unit = withMarkup(in.UnitCost, tp.MarkupPct)
			basis.MarkupPct = tp.MarkupPct
		case TPTable:
			unit = tp.UnitPrice
		default:
			return LinePrice{}, invalid("unknown transfer price method " + tp.Method)
		}
	default:
		return LinePrice{}, invalid("unknown pricing policy " + string(p.Pricing))
	}
	ext := money.Amount{Value: in.Quantity.Mul(unit), Currency: in.Currency}.Round()
	return LinePrice{UnitPrice: unit, Extended: ext, Basis: basis}, nil
}

// withMarkup returns cost plus markupPct percent.
func withMarkup(cost, markupPct decimal.Decimal) decimal.Decimal {
	return cost.Mul(decimal.NewFromInt(1).Add(markupPct.Div(decimal.NewFromInt(100))))
}
