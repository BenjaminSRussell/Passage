package domain

import (
	"sort"
	"time"

	"github.com/shopspring/decimal"
)

// ReleaseLine is a confirmed sales order line offered to the warehouse (FR-28.1).
type ReleaseLine struct {
	OrderID           string
	OrderLineID       string
	CustomerID        string
	LegalEntityID     string
	WarehouseID       string
	ProductID         string
	Unit              string
	Qty               decimal.Decimal
	ConfirmedShipDate time.Time
	ATPOK             bool
	Held              bool
	// CarrierCode (one per order) and Zone (the line's pick zone) are what the
	// picking workbench filters and groups on (FR-28.13). Both are optional.
	CarrierCode string
	Zone        string
	// TrackingRequired marks a line whose product carries a batch or serial
	// tracking dimension (BR-28.10): it cannot post without tracking entries.
	TrackingRequired bool
	// OrderType is the sales order type carried by order.confirmed (JOURNAL,
	// ORDER, SUBSCRIPTION, RETURNED); empty reads as ORDER. A JOURNAL order
	// skips credit check and ATP in sales and must never ship (BR-27.7).
	OrderType string
	// Priority is the order priority, 1 (first) to 9; 0 reads as DefaultPriority.
	// Every line of one order names the same one.
	Priority int
	// ATPQty is the quantity available to promise for the line now. When the
	// warehouse allows partial release (release_requires_full_atp false) and it
	// is below Qty, the line releases for ATPQty and the rest stays open for a
	// later run. Nil means the caller gave only ATPOK.
	ATPQty *decimal.Decimal
	// Partial is set by Release when Qty was cut to the ATP; OrderQty is then the
	// order line quantity the caller offered.
	Partial  bool
	OrderQty decimal.Decimal
}

// DefaultPriority is the priority of an order that names none.
const DefaultPriority = 5

// Order types of a sales order (order.confirmed.v1 order_type).
const OrderTypeJournal = "JOURNAL"

// ValidOrderType reports whether t is an order type sales can send; empty is ORDER.
func ValidOrderType(t string) bool {
	switch t {
	case "", "ORDER", OrderTypeJournal, "SUBSCRIPTION", "RETURNED":
		return true
	}
	return false
}

// NormalOrderType returns the stored order type, ORDER when none was given.
func NormalOrderType(t string) string {
	if t == "" {
		return "ORDER"
	}
	return t
}

// ReleaseRule carries the warehouse release parameters.
type ReleaseRule struct {
	Today          time.Time
	HorizonDays    int
	RequireFullATP bool
}

// ReleaseGroup is one shipment to create: lines of one order, one warehouse and one ship date.
type ReleaseGroup struct {
	OrderID     string
	WarehouseID string
	ShipDate    time.Time
	Priority    int
	Lines       []ReleaseLine
}

// Skip explains why a line was not released.
type Skip struct {
	OrderLineID string `json:"orderLineId"`
	Reason      string `json:"reason"`
}

// Skip reasons.
const (
	SkipHeld          = "held"
	SkipBeyondHorizon = "beyond_horizon"
	SkipATPShort      = "atp_short"
	SkipNonPositive   = "non_positive_qty"
	// SkipAlreadyReleased marks a line that is already on a shipment that is
	// not cancelled; the store applies it, because only it can see shipments.
	SkipAlreadyReleased = "already_released"
	// SkipDropShip marks a line with an active (not cancelled) drop-ship: the
	// vendor ships it, so it is not picked from stock (FR-28.7).
	SkipDropShip = "drop_ship"
	// SkipOrderRemainderCancelled marks a line whose kept quantity (what sales left after
	// cancelling the remainder of the order) is zero or already on live shipments: nothing is
	// left to release.
	SkipOrderRemainderCancelled = "order_remainder_cancelled"
	// SkipJournalOrder marks a line of a JOURNAL order: it is a record, not
	// goods, and never ships.
	SkipJournalOrder = "journal_order"
)

// Release applies the release rules to candidate lines. Held lines, lines
// beyond the horizon, lines short of ATP (when full ATP is required) and
// non-positive quantities are skipped with a reason. The rest are grouped by
// (order, warehouse, ship date) and the groups are ordered deterministically.
func Release(lines []ReleaseLine, rule ReleaseRule) ([]ReleaseGroup, []Skip) {
	today := dateOnly(rule.Today)
	horizon := today.AddDate(0, 0, rule.HorizonDays)
	groups := map[string]*ReleaseGroup{}
	var skipped []Skip
	for _, l := range lines {
		shipDate := dateOnly(l.ConfirmedShipDate)
		reason := ""
		switch {
		case l.OrderType == OrderTypeJournal:
			reason = SkipJournalOrder
		case l.Held:
			reason = SkipHeld
		case !l.Qty.IsPositive():
			reason = SkipNonPositive
		case shipDate.After(horizon):
			reason = SkipBeyondHorizon
		case l.ATPQty != nil && !l.ATPQty.IsPositive():
			reason = SkipATPShort
		case rule.RequireFullATP && !atpCovers(l):
			reason = SkipATPShort
		}
		if reason != "" {
			skipped = append(skipped, Skip{OrderLineID: l.OrderLineID, Reason: reason})
			continue
		}
		l.OrderQty = l.Qty
		if !rule.RequireFullATP && l.ATPQty != nil && l.ATPQty.LessThan(l.Qty) {
			l.Qty, l.Partial = *l.ATPQty, true
		}
		if l.Priority == 0 {
			l.Priority = DefaultPriority
		}
		key := l.OrderID + "|" + l.WarehouseID + "|" + shipDate.Format("2006-01-02")
		g, ok := groups[key]
		if !ok {
			g = &ReleaseGroup{OrderID: l.OrderID, WarehouseID: l.WarehouseID, ShipDate: shipDate, Priority: l.Priority}
			groups[key] = g
		}
		g.Lines = append(g.Lines, l)
	}
	keys := make([]string, 0, len(groups))
	for k := range groups {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := make([]ReleaseGroup, 0, len(keys))
	for _, k := range keys {
		out = append(out, *groups[k])
	}
	// Higher priority orders (1 is first) get their shipments first, then the
	// earlier ship date; the key order above breaks the remaining ties.
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Priority != out[j].Priority {
			return out[i].Priority < out[j].Priority
		}
		return out[i].ShipDate.Before(out[j].ShipDate)
	})
	return out, skipped
}

// atpCovers reports whether the line's ATP covers its whole quantity: the
// quantity when the caller gave one, else the ATPOK flag.
func atpCovers(l ReleaseLine) bool {
	if l.ATPQty != nil {
		return !l.ATPQty.LessThan(l.Qty)
	}
	return l.ATPOK
}

func dateOnly(t time.Time) time.Time {
	y, m, d := t.UTC().Date()
	return time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
}
