package domain

import (
	"fmt"

	"github.com/shopspring/decimal"
)

// Basis values for a layer's unrealized profit (FR-40.16).
const (
	BasisGroupCost      = "GroupCost"
	BasisMarginFallback = "MarginFallback"
)

// ICLayer is a remaining quantity of one intercompany receipt held by the buying
// entity. Costs are in the receipt currency. GroupUnitCost is the supplier's group
// cost; it is used only when GroupCostKnown is set.
type ICLayer struct {
	ID             string // receipt layer reference for drill
	Quantity       decimal.Decimal
	LegalUnitCost  decimal.Decimal
	GroupUnitCost  decimal.Decimal
	GroupCostKnown bool
}

// LayerProfit is the unrealized profit of one layer and the basis used, so the
// elimination detail can list every layer for drill.
type LayerProfit struct {
	Index  int
	ID     string
	Profit decimal.Decimal
	Basis  string
}

// UnrealizedProfit is the E3 profit in inventory across the layers. FallbackCount
// counts layers that used the seller margin fallback, which raises CNS-0908 as a warning.
type UnrealizedProfit struct {
	Layers        []LayerProfit
	Total         decimal.Decimal
	FallbackCount int
}

// UnrealizedProfitLayers computes, per layer, remaining quantity × (legal unit cost
// − group unit cost) (FR-40.16). A layer without a group cost falls back to the
// seller's margin (BR-40.7) applied to the layer's legal value. Each layer rounds to
// the currency minor unit (BR-40.9).
func UnrealizedProfitLayers(layers []ICLayer, currency string, sellerMarginPct decimal.Decimal) (UnrealizedProfit, error) {
	if len(currency) != 3 {
		return UnrealizedProfit{}, invalid("currency must be an ISO 4217 code")
	}
	if sellerMarginPct.IsNegative() || sellerMarginPct.GreaterThan(decimal.NewFromInt(100)) {
		return UnrealizedProfit{}, invalid("seller margin must be between 0 and 100 percent")
	}
	out := UnrealizedProfit{Total: decimal.Zero}
	for i, l := range layers {
		if l.Quantity.IsNegative() || l.LegalUnitCost.IsNegative() {
			return UnrealizedProfit{}, invalid(fmt.Sprintf("layer %d has a negative quantity or cost", i))
		}
		if l.GroupCostKnown && l.GroupUnitCost.IsNegative() {
			return UnrealizedProfit{}, invalid(fmt.Sprintf("layer %d has a negative group cost", i))
		}
		lp := LayerProfit{Index: i, ID: l.ID}
		if l.GroupCostKnown {
			lp.Profit = l.Quantity.Mul(l.LegalUnitCost.Sub(l.GroupUnitCost))
			lp.Basis = BasisGroupCost
		} else {
			lp.Profit = l.Quantity.Mul(l.LegalUnitCost).Mul(sellerMarginPct).Div(decimal.NewFromInt(100))
			lp.Basis = BasisMarginFallback
			out.FallbackCount++
		}
		lp.Profit = round(lp.Profit, currency)
		out.Total = out.Total.Add(lp.Profit)
		out.Layers = append(out.Layers, lp)
	}
	return out, nil
}

// Warning returns the CNS-0908 warning when any layer used the margin fallback,
// and nil otherwise. It does not fail the elimination.
func (u UnrealizedProfit) Warning() *Error {
	if u.FallbackCount == 0 {
		return nil
	}
	return &Error{Code: CodeGroupCostUnknown, Detail: fmt.Sprintf("group cost unknown for %d layers; margin fallback used", u.FallbackCount)}
}
