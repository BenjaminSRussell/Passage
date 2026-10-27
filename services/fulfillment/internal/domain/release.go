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
		case l.Held:
			reason = SkipHeld
		case !l.Qty.IsPositive():
			reason = SkipNonPositive
		case shipDate.After(horizon):
			reason = SkipBeyondHorizon
		case rule.RequireFullATP && !l.ATPOK:
			reason = SkipATPShort
		}
		if reason != "" {
			skipped = append(skipped, Skip{OrderLineID: l.OrderLineID, Reason: reason})
			continue
		}
		key := l.OrderID + "|" + l.WarehouseID + "|" + shipDate.Format("2006-01-02")
		g, ok := groups[key]
		if !ok {
			g = &ReleaseGroup{OrderID: l.OrderID, WarehouseID: l.WarehouseID, ShipDate: shipDate}
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
	return out, skipped
}

func dateOnly(t time.Time) time.Time {
	y, m, d := t.UTC().Date()
	return time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
}
