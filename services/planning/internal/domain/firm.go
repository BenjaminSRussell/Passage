package domain

import (
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"

	"github.com/shopspring/decimal"
)

// Firming groupings (FR-22.10, parameter "Firming grouping" in 4.22.13).
const (
	GroupNone       = "none"
	GroupVendor     = "vendor"
	GroupVendorDate = "vendor-date"
	GroupVendorWeek = "vendor-week"

	// DefaultGrouping is the 4.22.13 default.
	DefaultGrouping = GroupVendorDate
)

// ErrInvalidGrouping is returned for an unknown firming grouping.
var ErrInvalidGrouping = errors.New("planning: invalid firming grouping")

// FirmCandidate is one planned order offered for firming. DeliveryDay is the
// delivery date as days since 1970-01-01, so vendor-week groups by calendar
// week (Monday to Sunday) whatever the plan's as-of date.
type FirmCandidate struct {
	ID          string
	OrderType   string
	SiteID      string
	VendorID    string
	DeliveryDay int
	Qty         decimal.Decimal
}

// FirmGroup is one target order to create: a purchase order whose lines are
// the grouped planned orders, or one production or transfer order per planned
// order. DeliveryDay is the earliest delivery of the group; IDs are sorted.
type FirmGroup struct {
	OrderType   string
	SiteID      string
	VendorID    string
	DeliveryDay int
	IDs         []string
	Qty         decimal.Decimal
}

// ValidGrouping reports whether mode is a firming grouping.
func ValidGrouping(mode string) bool {
	switch mode {
	case GroupNone, GroupVendor, GroupVendorDate, GroupVendorWeek:
		return true
	}
	return false
}

// GroupForFirming partitions planned orders into target orders. Purchase
// orders of one site and vendor merge per mode: all of them (vendor), per
// delivery day (vendor-date) or per calendar week (vendor-week); with none
// every order is its own target. Production and transfer orders never merge.
// The result does not depend on input order: groups are sorted by order type,
// site, vendor, delivery day and first planned order id.
func GroupForFirming(cands []FirmCandidate, mode string) ([]FirmGroup, error) {
	if !ValidGrouping(mode) {
		return nil, fmt.Errorf("%w: %q", ErrInvalidGrouping, mode)
	}
	byKey := map[string]*FirmGroup{}
	keys := []string{}
	for _, c := range cands {
		key := groupKey(c, mode)
		g, ok := byKey[key]
		if !ok {
			g = &FirmGroup{OrderType: c.OrderType, SiteID: c.SiteID, VendorID: c.VendorID, DeliveryDay: c.DeliveryDay, Qty: decimal.Zero}
			byKey[key] = g
			keys = append(keys, key)
		}
		g.IDs = append(g.IDs, c.ID)
		g.Qty = g.Qty.Add(c.Qty)
		g.DeliveryDay = min(g.DeliveryDay, c.DeliveryDay)
	}
	out := make([]FirmGroup, 0, len(keys))
	for _, k := range keys {
		g := byKey[k]
		sort.Strings(g.IDs)
		out = append(out, *g)
	}
	slices.SortFunc(out, func(a, b FirmGroup) int {
		if c := strings.Compare(a.OrderType, b.OrderType); c != 0 {
			return c
		}
		if c := strings.Compare(a.SiteID, b.SiteID); c != 0 {
			return c
		}
		if c := strings.Compare(a.VendorID, b.VendorID); c != 0 {
			return c
		}
		if a.DeliveryDay != b.DeliveryDay {
			return a.DeliveryDay - b.DeliveryDay
		}
		return strings.Compare(a.IDs[0], b.IDs[0])
	})
	return out, nil
}

// groupKey returns the target-order key of a candidate. Orders that never
// merge are keyed by their own id.
func groupKey(c FirmCandidate, mode string) string {
	if c.OrderType != OrderPurchase || mode == GroupNone {
		return "one|" + c.ID
	}
	switch mode {
	case GroupVendor:
		return fmt.Sprintf("vendor|%s|%s", c.SiteID, c.VendorID)
	case GroupVendorWeek:
		return fmt.Sprintf("week|%s|%s|%d", c.SiteID, c.VendorID, isoWeek(c.DeliveryDay))
	default:
		return fmt.Sprintf("date|%s|%s|%d", c.SiteID, c.VendorID, c.DeliveryDay)
	}
}

// isoWeek numbers the Monday-to-Sunday week of an epoch day. 1970-01-01 was a
// Thursday, so shifting by 3 days puts every Monday at the start of a week.
func isoWeek(day int) int {
	d := day + 3
	q := d / 7
	if d%7 < 0 {
		q--
	}
	return q
}
