package domain

import (
	"sort"

	"github.com/shopspring/decimal"
)

// VersionOrder is a planned order as the version diff sees it. Dates are
// ISO calendar days (YYYY-MM-DD) so they sort as text. VendorID is empty when
// the order has no vendor.
type VersionOrder struct {
	Number       string
	ItemID       string
	OrderType    string
	Qty          decimal.Decimal
	OrderDate    string
	DeliveryDate string
	VendorID     string
}

// OrderChange pairs the same logical planned order in two versions.
type OrderChange struct {
	From     VersionOrder
	To       VersionOrder
	QtyDelta decimal.Decimal
}

// VersionDiff is the planned-order difference between two plan versions
// (FR-22.14). Added orders exist only in the later version, removed only in
// the earlier one.
type VersionDiff struct {
	Added     []VersionOrder
	Removed   []VersionOrder
	Changed   []OrderChange
	Unchanged int
	// QtyDelta is the net change of planned quantity per order type (to − from).
	QtyDelta map[string]decimal.Decimal
}

type diffKey struct{ item, orderType, delivery string }

// cmpOrder orders planned orders by quantity, order date, vendor and number.
func cmpOrder(a, b VersionOrder) int {
	if c := a.Qty.Cmp(b.Qty); c != 0 {
		return c
	}
	if a.OrderDate != b.OrderDate {
		if a.OrderDate < b.OrderDate {
			return -1
		}
		return 1
	}
	if a.VendorID != b.VendorID {
		if a.VendorID < b.VendorID {
			return -1
		}
		return 1
	}
	return 0
}

// DiffVersions compares the planned orders of two versions. Order numbers
// are per version and carry no identity, so orders match on item, order type
// and delivery day. Within one key, orders equal in quantity, order date and
// vendor pair first and are unchanged; the rest pair up in quantity order and
// are changed, and the leftovers are added or removed. The result does not
// depend on the order of the inputs.
func DiffVersions(from, to []VersionOrder) VersionDiff {
	group := func(os []VersionOrder) map[diffKey][]VersionOrder {
		m := map[diffKey][]VersionOrder{}
		for _, o := range os {
			k := diffKey{o.ItemID, o.OrderType, o.DeliveryDate}
			m[k] = append(m[k], o)
		}
		for _, l := range m {
			sort.Slice(l, func(a, b int) bool {
				if c := cmpOrder(l[a], l[b]); c != 0 {
					return c < 0
				}
				return l[a].Number < l[b].Number
			})
		}
		return m
	}
	fm, tm := group(from), group(to)
	keys := map[diffKey]bool{}
	for k := range fm {
		keys[k] = true
	}
	for k := range tm {
		keys[k] = true
	}
	ordered := make([]diffKey, 0, len(keys))
	for k := range keys {
		ordered = append(ordered, k)
	}
	sort.Slice(ordered, func(a, b int) bool {
		x, y := ordered[a], ordered[b]
		if x.item != y.item {
			return x.item < y.item
		}
		if x.orderType != y.orderType {
			return x.orderType < y.orderType
		}
		return x.delivery < y.delivery
	})
	d := VersionDiff{Added: []VersionOrder{}, Removed: []VersionOrder{}, Changed: []OrderChange{}, QtyDelta: map[string]decimal.Decimal{}}
	add := func(t string, q decimal.Decimal) { d.QtyDelta[t] = d.QtyDelta[t].Add(q) }
	for _, k := range ordered {
		// Both lists are sorted by cmpOrder, so one merge pass finds every
		// exact match and keeps the unmatched orders in sorted order.
		f, t := fm[k], tm[k]
		var fl, tl []VersionOrder
		i, j := 0, 0
		for i < len(f) && j < len(t) {
			switch c := cmpOrder(f[i], t[j]); {
			case c == 0:
				d.Unchanged++
				i++
				j++
			case c < 0:
				fl = append(fl, f[i])
				i++
			default:
				tl = append(tl, t[j])
				j++
			}
		}
		fl, tl = append(fl, f[i:]...), append(tl, t[j:]...)
		n := min(len(fl), len(tl))
		for i := 0; i < n; i++ {
			delta := tl[i].Qty.Sub(fl[i].Qty)
			d.Changed = append(d.Changed, OrderChange{From: fl[i], To: tl[i], QtyDelta: delta})
			add(k.orderType, delta)
		}
		for _, o := range fl[n:] {
			d.Removed = append(d.Removed, o)
			add(k.orderType, o.Qty.Neg())
		}
		for _, o := range tl[n:] {
			d.Added = append(d.Added, o)
			add(k.orderType, o.Qty)
		}
	}
	return d
}
