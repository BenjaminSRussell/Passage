package domain

import "github.com/shopspring/decimal"

// TransferSupply is the open quantity a transfer order line holds in the
// projection while it is unfinished (BR-16.13): OnOrder is demand at the
// shipping warehouse (the ON_ORDER bucket), Ordered is the expected receipt at
// the receiving warehouse (the ORDERED bucket). Planning reads both.
type TransferSupply struct {
	OnOrder decimal.Decimal
	Ordered decimal.Decimal
}

// SupplyActive reports whether an order in status holds supply: a DRAFT is
// invisible, and a cancelled, deleted or fully received order has none left.
func SupplyActive(status string) bool {
	switch status {
	case TransferCreated, TransferShipped, TransferPartial:
		return true
	}
	return false
}

// Supply is the supply of line l on order o. The shipping demand falls as the
// line ships and the expected receipt as it is received; a line closed short
// expects nothing more. Over-shipping or over-receiving within tolerance never
// makes either negative.
func (o TransferOrder) Supply(l TransferLine) TransferSupply {
	if !SupplyActive(o.Status) || l.Closed {
		return TransferSupply{}
	}
	return TransferSupply{OnOrder: nonNegative(l.Qty.Sub(l.ShippedQty)), Ordered: nonNegative(l.Qty.Sub(l.ReceivedQty))}
}

func nonNegative(d decimal.Decimal) decimal.Decimal {
	if d.IsNegative() {
		return decimal.Zero
	}
	return d
}
