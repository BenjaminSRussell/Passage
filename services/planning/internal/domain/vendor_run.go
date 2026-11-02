package domain

import (
	"crypto/sha256"
	"fmt"
	"slices"
	"sort"
)

// ActionNoVendor is the PLAN-0008 message: a planned purchase has no vendor,
// neither from a quota source nor from the item coverage (FR-22.3, FR-22.5).
const ActionNoVendor = "no-vendor"

// ApplyDefaultVendors gives every planned purchase order that has no vendor yet
// the default vendor of its item's coverage (FR-22.3, FR-22.5). A vendor that a
// quota source already set (ApplyQuota runs first) is kept. An item whose
// purchase orders are left without any vendor raises one PLAN-0008 warning,
// on its earliest vendorless order, with the item's vendorless quantity; the
// run still plans the orders ("using none"). Production and transfer orders
// take no vendor. Messages stay sorted and the plan hash is recomputed when
// anything changed, covering each vendor given and each message added.
func ApplyDefaultVendors(res Result, vendors map[string]string) Result {
	if len(res.Orders) == 0 {
		return res
	}
	orders := slices.Clone(res.Orders)
	h := sha256.New()
	h.Write([]byte(res.Hash))
	changed := false
	totals := map[string]Message{}
	var items []string
	for i := range orders {
		o := &orders[i]
		if o.OrderType != OrderPurchase || o.VendorID != "" {
			continue
		}
		if v := vendors[o.ItemID]; v != "" {
			o.VendorID = v
			fmt.Fprintf(h, "V|%s|%s\n", o.Number, v)
			changed = true
			continue
		}
		m, seen := totals[o.ItemID]
		if !seen {
			items = append(items, o.ItemID)
			m = Message{Action: ActionNoVendor, ItemID: o.ItemID, SupplyRef: o.Number, SuggestedDate: o.OrderDate}
		}
		if o.OrderDate < m.SuggestedDate || (o.OrderDate == m.SuggestedDate && o.Number < m.SupplyRef) {
			m.SupplyRef, m.SuggestedDate = o.Number, o.OrderDate
		}
		m.Qty = m.Qty.Add(o.Qty)
		totals[o.ItemID] = m
	}
	if !changed && len(items) == 0 {
		return res
	}
	res.Orders = orders
	if len(items) > 0 {
		sort.Strings(items)
		msgs := slices.Clone(res.Messages)
		for _, id := range items {
			m := totals[id]
			msgs = append(msgs, m)
			fmt.Fprintf(h, "M|%s|%s|%s|%s|%d|%d|%s\n", m.Action, m.ItemID, m.SupplyRef, m.DemandRef, m.SuggestedDate, m.DelayDays, m.Qty)
		}
		sort.SliceStable(msgs, func(a, b int) bool { return msgLess(msgs[a], msgs[b]) })
		res.Messages = dedupeMessages(msgs)
	}
	res.Hash = fmt.Sprintf("%x", h.Sum(nil))
	return res
}
