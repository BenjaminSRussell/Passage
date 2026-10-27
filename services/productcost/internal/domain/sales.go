package domain

import (
	"errors"

	"github.com/shopspring/decimal"
)

// CalcTypeSalesOrder is the calculation type of a sales order estimate (FR-39.16).
const CalcTypeSalesOrder = "SalesOrder"

// SalesEstimate is the cost and margin of one configured or make-to-order line
// at its order quantity (FR-39.16).
type SalesEstimate struct {
	ItemID          string          `json:"itemId"`
	CalculationType string          `json:"calculationType"`
	Status          string          `json:"status"`
	Quantity        decimal.Decimal `json:"quantity"`
	Unit            decimal.Decimal `json:"unitCost"`
	Cost            decimal.Decimal `json:"cost"`
	Split           Split           `json:"split"`
	NetAmount       decimal.Decimal `json:"netAmount"`
	Margin          decimal.Decimal `json:"margin"`
	MarginPct       decimal.Decimal `json:"marginPct"`
	Diagnostics     []Diagnostic    `json:"diagnostics,omitempty"`
}

// SalesOrderEstimate rolls itemID at the order quantity and returns its cost,
// split and margin against the net line amount. The root item's costing lot is
// the order quantity, so setup is spread over the order (BR-39.5). The cost and
// its split are rounded to scale decimals (PostingScale) and the split sums to
// the cost, residual to DM (BR-39.1). Margin is net amount − estimated cost;
// margin % is margin ÷ net amount, to two decimals.
func SalesOrderEstimate(cat Catalog, itemID string, qty, net decimal.Decimal, scale int32, opts RollOptions) (SalesEstimate, error) {
	if !qty.IsPositive() {
		return SalesEstimate{}, errors.New("quantity must be positive")
	}
	if net.IsNegative() {
		return SalesEstimate{}, errors.New("net amount must not be negative")
	}
	if net.IsZero() {
		return SalesEstimate{}, errors.New("net amount must be positive to compute margin percent")
	}
	root, ok := cat[itemID]
	if !ok {
		return SalesEstimate{}, errors.New("item " + itemID + " is not in the catalog")
	}
	root.CostingLot = qty
	local := make(Catalog, len(cat))
	for k, v := range cat {
		local[k] = v
	}
	local[itemID] = root
	r, _ := RollOne(local, itemID, opts)

	est := SalesEstimate{
		ItemID: itemID, CalculationType: CalcTypeSalesOrder, Status: r.Status,
		Quantity: qty, NetAmount: net, Split: Split{}, Diagnostics: r.Diagnostics,
	}
	if r.Status != StatusPending {
		return est, nil
	}
	combined := r.Combined()
	est.Unit = combined.Total()
	est.Split, est.Cost = IssueCOGS(qty, combined, scale)
	est.Margin = net.Sub(est.Cost)
	est.MarginPct = est.Margin.Mul(decimal.NewFromInt(100)).Div(net).Round(MoneyScale)
	return est, nil
}
