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
	ItemID            string
	Day               int
	Gross             decimal.Decimal
	ScheduledReceipts decimal.Decimal
	PlannedReceipts   decimal.Decimal
	PAB               decimal.Decimal
	// Net is the day's own shortfall before planned receipts:
	// max(0, GR − max(PAB(t−1), 0) − SR). A shortage of an earlier day that
	// stayed uncovered is not counted again.
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
// stock as a demand at today, BR-22.1 step 4), its receipts, and the planned
// orders delivered to it (orders must be this item's only). Nothing is dated
// before today: what is past due counts at today. PAB(t) = PAB(t−1) + SR(t) +
// PR(t) − GR(t), opening at on-hand.
func (rs *runState) netRows(it *Item, orders []PlannedOrder) []NetRow {
	type bucket struct{ gross, sr, pr decimal.Decimal }
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
	}
	if it.SafetyStock.IsPositive() {
		b := at(rs.demandDate(rs.today))
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
	pab := it.OnHand
	for _, day := range sorted {
		b := days[day]
		net := decimal.Max(decimal.Zero, b.gross.Sub(decimal.Max(pab, decimal.Zero)).Sub(b.sr))
		pab = pab.Add(b.sr).Add(b.pr).Sub(b.gross)
		rows = append(rows, NetRow{ItemID: it.ID, Day: day, Gross: b.gross, ScheduledReceipts: b.sr, PlannedReceipts: b.pr, PAB: pab, Net: net})
	}
	return rows
}
