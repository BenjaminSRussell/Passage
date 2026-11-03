package domain

import (
	"sort"

	"github.com/shopspring/decimal"
)

// NetRow is one day of the time-phased net requirements view of an item
// (FR-22.12, the MD04 columns): gross requirements (GR), scheduled receipts
// (SR), planned receipts (PR), the projected available balance (PAB) and the
// net requirement. Day is a plan day index. Only days with movement are
// listed; PAB carries over the quiet days between them.
type NetRow struct {
	ItemID string
	// WarehouseID is the warehouse scope of ItemID, empty for the site scope.
	WarehouseID       string
	Day               int
	Gross             decimal.Decimal
	ScheduledReceipts decimal.Decimal
	PlannedReceipts   decimal.Decimal
	PAB               decimal.Decimal
	// Net is the day's own shortfall before planned receipts:
	// max(0, GR − max(PAB(t−1), 0) − SR). A shortage of an earlier day that
	// stayed uncovered is not counted again. A gross demand (mts-gross
	// forecast, Requirement.Gross) is not covered by the on-hand the item
	// opened with, as in the netting, so the on-hand left over by the other
	// demand does not count against it.
	Net decimal.Decimal
}

// netView builds the view for every item, ordered by item id then day.
func (rs *runState) netView(items []*Item) []NetRow {
	ordersOf := make(map[string][]PlannedOrder, len(items))
	for _, o := range rs.res.Orders {
		ordersOf[o.ItemID] = append(ordersOf[o.ItemID], o)
	}
	sorted := make([]*Item, len(items))
	copy(sorted, items)
	sort.Slice(sorted, func(a, b int) bool { return sorted[a].ID < sorted[b].ID })
	var out []NetRow
	for _, it := range sorted {
		out = append(out, rs.netRows(it, ordersOf[it.ID])...)
	}
	return out
}

// netRows builds the view of one item from the dates the netting used: its
// final demand (with the dependent demand of the explosion and the safety
// stock as a demand at today or, with policy first-issue, on the first demand
// date, BR-22.1 step 4 and BR-22.5), its receipts, and the planned
// orders delivered to it (orders must be this item's only). Nothing is dated
// before today: what is past due counts at today. PAB(t) = PAB(t−1) + SR(t) +
// PR(t) − GR(t), opening at on-hand.
func (rs *runState) netRows(it *Item, orders []PlannedOrder) []NetRow {
	type bucket struct{ gross, grossOnly, sr, pr decimal.Decimal }
	days := map[int]*bucket{}
	at := func(day int) *bucket {
		b, ok := days[day]
		if !ok {
			b = &bucket{}
			days[day] = b
		}
		return b
	}
	for _, d := range it.Demand {
		b := at(rs.demandDate(d.Date))
		b.gross = b.gross.Add(d.Qty)
		if d.Gross {
			b.grossOnly = b.grossOnly.Add(d.Qty)
		}
	}
	if it.SafetyStock.IsPositive() {
		b := at(rs.safetyDate(it))
		b.gross = b.gross.Add(it.SafetyStock)
	}
	for _, r := range it.Receipts {
		b := at(max(rs.supplyDate(r.Date), rs.today))
		b.sr = b.sr.Add(r.Qty)
	}
	for _, o := range orders {
		b := at(max(o.DeliveryDate, rs.today))
		b.pr = b.pr.Add(o.Qty)
	}
	if len(days) == 0 {
		return nil
	}
	sorted := make([]int, 0, len(days))
	for day := range days {
		sorted = append(sorted, day)
	}
	sort.Ints(sorted)
	rows := make([]NetRow, 0, len(sorted))
	// PAB is split into the opening on-hand not yet used (oh), which only
	// net demand draws on, and the rest; without gross demand this is
	// max(0, GR − max(PAB(t−1), 0) − SR) exactly.
	oh, rest := it.OnHand, decimal.Zero
	for _, day := range sorted {
		b := days[day]
		fromOnHand := decimal.Min(oh, b.gross.Sub(b.grossOnly))
		oh = oh.Sub(fromOnHand)
		open := b.gross.Sub(fromOnHand)
		net := decimal.Max(decimal.Zero, open.Sub(decimal.Max(rest, decimal.Zero)).Sub(b.sr))
		rest = rest.Add(b.sr).Add(b.pr).Sub(open)
		pab := oh.Add(rest)
		rows = append(rows, NetRow{ItemID: it.ID, Day: day, Gross: b.gross, ScheduledReceipts: b.sr, PlannedReceipts: b.pr, PAB: pab, Net: net})
	}
	return rows
}
