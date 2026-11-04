package domain

import (
	"fmt"

	"github.com/shopspring/decimal"
)

// Message catalog codes (PRD 4.22.18) of the actions that have one.
const (
	CodePostpone   = "PLAN-0006"
	CodeDelay      = "PLAN-0007"
	CodeLotSplit   = "PLAN-0005"
	CodeNoVendor   = "PLAN-0008"
	CodeRedZone    = "PLAN-0010"
	CodeSalesOnTop = "PLAN-0015"
)

// MessageView is what the catalog needs to render a stored message.
type MessageView struct {
	Action, ItemID, SupplyRef, DemandRef string
	// Date is the suggested date, YYYY-MM-DD.
	Date string
	Days int
	Qty  string
	// Priority is the planning priority of the buffer order, a decimal string; empty when none.
	Priority string
}

// CatalogCode returns the catalog code of an action, or "" for an action the
// catalog has no entry for (advance, shortage, cancel, decrease and increase
// are action messages of FR-22.7 without a catalog line).
func CatalogCode(action string) string {
	switch action {
	case ActionPostpone:
		return CodePostpone
	case ActionDelay:
		return CodeDelay
	case ActionLotSplit:
		return CodeLotSplit
	case ActionNoVendor:
		return CodeNoVendor
	case ActionRedZone:
		return CodeRedZone
	case ActionSalesOnTop:
		return CodeSalesOnTop
	}
	return ""
}

// CatalogText renders the user text of 4.22.18 for a message, "" when the
// action has no catalog entry. The item is named by its id, as the service
// holds no item master. A red-zone priority is shown as a percentage of the
// top of green (0.5306 reads 53.06), a quantity without trailing zeros, and a
// delayed order that covers no demand (safety stock) is named by its supply.
func CatalogText(m MessageView) string {
	switch m.Action {
	case ActionPostpone:
		return fmt.Sprintf("Supply %s can be postponed to %s.", m.SupplyRef, m.Date)
	case ActionDelay:
		ref := m.DemandRef
		if ref == "" {
			ref = m.SupplyRef
		}
		return fmt.Sprintf("Demand %s will be delayed %d days (expected %s).", ref, m.Days, m.Date)
	case ActionLotSplit:
		return fmt.Sprintf("Requirement for %s splits into %d orders (max %s).", m.ItemID, m.Days, qtyText(m.Qty))
	case ActionNoVendor:
		return fmt.Sprintf("No vendor for planned purchase of %s; using none.", m.ItemID)
	case ActionRedZone:
		pct := "0"
		if p, err := decimal.NewFromString(m.Priority); err == nil {
			pct = p.Mul(decimal.NewFromInt(100)).Round(2).String()
		}
		return fmt.Sprintf("Buffer for %s is in red zone (priority %s%%).", m.ItemID, pct)
	case ActionSalesOnTop:
		qty := m.Qty
		if q, err := decimal.NewFromString(qty); err == nil {
			qty = q.String()
		}
		return fmt.Sprintf("Sales demand %s exceeds consumable forecast by %s; added on top.", m.DemandRef, qty)
	}
	return ""
}

// qtyText writes a decimal string without trailing zeros; text that is not a
// decimal is returned as it is.
func qtyText(s string) string {
	if q, err := decimal.NewFromString(s); err == nil {
		return q.String()
	}
	return s
}
