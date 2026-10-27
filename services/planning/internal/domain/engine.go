// Package domain holds the master-planning engine: low-level codes, FIFO
// pegging, lot sizing, planned orders, explosion of BOMs and action messages.
// It is pure: inputs are a snapshot, outputs are plain values. Time is measured
// in integer days from the plan's as-of date (today = 0 in the caller's frame,
// but any integer works).
package domain

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"

	"github.com/shopspring/decimal"
)

const (
	CoverageRequirement = "requirement"
	CoveragePeriod      = "period"
	CoverageMinMax      = "minmax"
	CoverageManual      = "manual"

	OrderPurchase   = "purchase"
	OrderProduction = "production"
	OrderTransfer   = "transfer"

	RefSales    = "sales-line"
	RefForecast = "forecast"
	RefOnHand   = "on-hand"
	RefSafety   = "safety-stock"
	RefBOM      = "bom-line"
	RefSurplus  = "surplus"
	RefPlanned  = "planned-order"

	ActionDelay    = "delay"
	ActionPostpone = "postpone"
	ActionAdvance  = "advance"
	ActionShortage = "shortage"

	PrioritySales     = 1
	PriorityForecast  = 2
	PrioritySafety    = 3
	PriorityDependent = 4

	// MaxSplits caps how many orders one requirement may split into (FR-22.4).
	MaxSplits = 1000
)

var (
	ErrInvalidItem   = errors.New("planning: invalid item")
	ErrBOMCycle      = errors.New("planning: BOM cycle")
	ErrTooManySplits = errors.New("planning: lot split limit exceeded")
)

// Requirement is one demand or receipt on an item. Date is a day index.
type Requirement struct {
	Ref      string
	RefType  string
	Date     int
	Qty      decimal.Decimal
	Priority int
}

// BOMLine is one component of an item; Qty is per unit of the parent.
type BOMLine struct {
	ItemID string
	Qty    decimal.Decimal
}

// Item is the planning snapshot of one item at one site.
type Item struct {
	ID                         string
	Coverage                   string
	PeriodDays                 int
	MarginDays                 int
	PositiveDays, NegativeDays int
	ReorderMin, ReorderMax     decimal.Decimal
	OnHand, SafetyStock        decimal.Decimal
	OrderType                  string
	LeadTimeDays               int
	MinOrderQty, MultipleQty   decimal.Decimal
	MaxOrderQty, StandardQty   decimal.Decimal
	Demand, Receipts           []Requirement
	Components                 []BOMLine
}

// PlannedOrder is a proposed purchase, production or transfer order.
type PlannedOrder struct {
	Number       string
	ItemID       string
	OrderType    string
	Qty          decimal.Decimal
	OrderDate    int
	DeliveryDate int
	FuturesDate  *int
	DelayDays    int
	LotRule      string
}

// Peg links one unit of supply to the demand it covers (ReqTransCov).
type Peg struct {
	ItemID, SupplyRef, DemandRef, DemandRefType string
	DemandDate                                  int
	Qty                                         decimal.Decimal
}

// Message is an action or delay message for the planner.
type Message struct {
	Action, ItemID, SupplyRef, DemandRef string
	SuggestedDate, DelayDays             int
	Qty                                  decimal.Decimal
}

// Result is the output of one run.
type Result struct {
	Orders   []PlannedOrder
	Pegging  []Peg
	Messages []Message
	Hash     string
}

type layer struct {
	ref, refType string
	date         int
	priority     int
	qty          decimal.Decimal
}

type runState struct {
	today int
	index map[string]*Item
	res   Result
	seq   int
}

type ledger struct {
	rs    *runState
	item  *Item
	avail []layer // FIFO supply not yet pegged
	unmet []layer // FIFO demand not yet covered
	// pending holds planned supply that lands after the walk's current day.
	pending []layer
	pegs    []Peg
}

// Run plans every item. Items are processed level by level (low-level code
// ascending, then ID) so dependent demand from a parent is complete before the
// child is netted. The input slices are not modified.
func Run(today int, in []Item) (Result, error) {
	items := make([]Item, len(in))
	copy(items, in)
	index := make(map[string]*Item, len(items))
	for i := range items {
		it := &items[i]
		it.Demand = slices.Clone(it.Demand)
		it.Receipts = slices.Clone(it.Receipts)
		it.Components = slices.Clone(it.Components)
		if err := validate(it); err != nil {
			return Result{}, err
		}
		if _, dup := index[it.ID]; dup {
			return Result{}, fmt.Errorf("%w: duplicate id %q", ErrInvalidItem, it.ID)
		}
		index[it.ID] = it
	}
	llc, err := lowLevelCodes(items, index)
	if err != nil {
		return Result{}, err
	}
	order := make([]*Item, 0, len(items))
	for i := range items {
		order = append(order, &items[i])
	}
	sort.SliceStable(order, func(a, b int) bool {
		if llc[order[a].ID] != llc[order[b].ID] {
			return llc[order[a].ID] < llc[order[b].ID]
		}
		return order[a].ID < order[b].ID
	})
	rs := &runState{today: today, index: index}
	for _, it := range order {
		if err := rs.plan(it); err != nil {
			return Result{}, err
		}
	}
	rs.propagateFutures(order)
	sort.Slice(rs.res.Orders, func(a, b int) bool { return rs.res.Orders[a].Number < rs.res.Orders[b].Number })
	sort.Slice(rs.res.Pegging, func(a, b int) bool { return pegLess(rs.res.Pegging[a], rs.res.Pegging[b]) })
	sort.Slice(rs.res.Messages, func(a, b int) bool { return msgLess(rs.res.Messages[a], rs.res.Messages[b]) })
	rs.res.Messages = dedupeMessages(rs.res.Messages)
	rs.res.Hash = hashResult(rs.res)
	return rs.res, nil
}

func validate(it *Item) error {
	switch it.Coverage {
	case CoverageRequirement, CoveragePeriod, CoverageMinMax, CoverageManual:
	default:
		return fmt.Errorf("%w: %q coverage %q", ErrInvalidItem, it.ID, it.Coverage)
	}
	switch it.OrderType {
	case OrderPurchase, OrderProduction, OrderTransfer:
	default:
		return fmt.Errorf("%w: %q order type %q", ErrInvalidItem, it.ID, it.OrderType)
	}
	if it.ID == "" || it.LeadTimeDays < 0 || it.MarginDays < 0 || it.OnHand.IsNegative() || it.SafetyStock.IsNegative() {
		return fmt.Errorf("%w: %q lead time, margin, on-hand or safety stock", ErrInvalidItem, it.ID)
	}
	for _, d := range it.Demand {
		if d.Ref == "" || !d.Qty.IsPositive() {
			return fmt.Errorf("%w: %q demand %q needs a ref and a positive qty", ErrInvalidItem, it.ID, d.Ref)
		}
	}
	for _, r := range it.Receipts {
		if r.Ref == "" || !r.Qty.IsPositive() {
			return fmt.Errorf("%w: %q receipt %q needs a ref and a positive qty", ErrInvalidItem, it.ID, r.Ref)
		}
	}
	for _, c := range it.Components {
		if !c.Qty.IsPositive() {
			return fmt.Errorf("%w: %q component %q needs a positive qty per unit", ErrInvalidItem, it.ID, c.ItemID)
		}
	}
	return nil
}

// LowLevelCodes returns LLC(item) = longest path from a root through the BOM.
func lowLevelCodes(items []Item, index map[string]*Item) (map[string]int, error) {
	indeg := make(map[string]int, len(items))
	for i := range items {
		indeg[items[i].ID] += 0
		for _, c := range items[i].Components {
			if index[c.ItemID] == nil {
				return nil, fmt.Errorf("%w: component %q of %q", ErrInvalidItem, c.ItemID, items[i].ID)
			}
			indeg[c.ItemID]++
		}
	}
	llc := make(map[string]int, len(items))
	var queue []string
	for i := range items {
		if indeg[items[i].ID] == 0 {
			queue = append(queue, items[i].ID)
		}
	}
	done := 0
	for len(queue) > 0 {
		id := queue[0]
		queue = queue[1:]
		done++
		for _, c := range index[id].Components {
			if llc[id]+1 > llc[c.ItemID] {
				llc[c.ItemID] = llc[id] + 1
			}
			indeg[c.ItemID]--
			if indeg[c.ItemID] == 0 {
				queue = append(queue, c.ItemID)
			}
		}
	}
	if done != len(items) {
		return nil, ErrBOMCycle
	}
	return llc, nil
}

func reqLess(a, b Requirement) bool {
	if a.Date != b.Date {
		return a.Date < b.Date
	}
	if a.Priority != b.Priority {
		return a.Priority < b.Priority
	}
	return a.Ref < b.Ref
}

func (rs *runState) plan(item *Item) error {
	demands := make([]Requirement, 0, len(item.Demand)+1)
	for _, d := range item.Demand {
		d.Date = max(d.Date, rs.today)
		demands = append(demands, d)
	}
	if item.SafetyStock.IsPositive() {
		demands = append(demands, Requirement{Ref: RefSafety + ":" + item.ID, RefType: RefSafety, Date: rs.today, Qty: item.SafetyStock, Priority: PrioritySafety})
	}
	sort.SliceStable(demands, func(a, b int) bool { return reqLess(demands[a], demands[b]) })
	receipts := slices.Clone(item.Receipts)
	sort.SliceStable(receipts, func(a, b int) bool {
		if receipts[a].Date != receipts[b].Date {
			return receipts[a].Date < receipts[b].Date
		}
		return receipts[a].Ref < receipts[b].Ref
	})

	L := &ledger{rs: rs, item: item}
	if item.OnHand.IsPositive() {
		L.addSupply(layer{ref: RefOnHand + ":" + item.ID, refType: RefOnHand, date: rs.today, qty: item.OnHand})
	}
	if item.Coverage == CoverageMinMax {
		// BR-22.1 step 5: a min/max item is checked at today even with no events,
		// so a stock level below minimum always reorders. Orders created here are
		// counted as supply by the walk below.
		if err := L.check(rs.today, nil, nil); err != nil {
			return err
		}
	}
	di, ri := 0, 0
	for {
		t, ok := nextTime(demands, di, receipts, ri, L.pending)
		if !ok {
			break
		}
		for ri < len(receipts) && receipts[ri].Date <= t {
			r := receipts[ri]
			L.addSupply(layer{ref: r.Ref, refType: r.RefType, date: r.Date, qty: r.Qty})
			ri++
		}
		L.releasePending(t)
		for di < len(demands) && demands[di].Date <= t {
			d := demands[di]
			L.addDemand(layer{ref: d.Ref, refType: d.RefType, date: d.Date, priority: d.Priority, qty: d.Qty})
			di++
		}
		if err := L.check(t, demands[di:], receipts[ri:]); err != nil {
			return err
		}
	}
	L.finish(receipts)
	rs.res.Pegging = append(rs.res.Pegging, L.pegs...)
	return nil
}

func nextTime(demands []Requirement, di int, receipts []Requirement, ri int, pending []layer) (int, bool) {
	best, found := 0, false
	consider := func(t int) {
		if !found || t < best {
			best, found = t, true
		}
	}
	if di < len(demands) {
		consider(demands[di].Date)
	}
	if ri < len(receipts) {
		consider(receipts[ri].Date)
	}
	for _, p := range pending {
		consider(p.date)
	}
	return best, found
}

func (L *ledger) releasePending(t int) {
	var keep []layer
	var due []layer
	for _, p := range L.pending {
		if p.date <= t {
			due = append(due, p)
		} else {
			keep = append(keep, p)
		}
	}
	L.pending = keep
	for _, p := range due {
		L.addSupply(p)
	}
}

func (L *ledger) peg(supplyRef string, d layer, qty decimal.Decimal) {
	L.pegs = append(L.pegs, Peg{ItemID: L.item.ID, SupplyRef: supplyRef, DemandRef: d.ref, DemandRefType: d.refType, DemandDate: d.date, Qty: qty})
}

// addSupply covers the oldest unmet demand first, the rest becomes available.
func (L *ledger) addSupply(s layer) {
	s = L.cover(s)
	if s.qty.IsPositive() {
		L.avail = append(L.avail, s)
	}
}

// cover pegs s to the oldest unmet demand and returns what is left of s.
func (L *ledger) cover(s layer) layer {
	for len(L.unmet) > 0 && s.qty.IsPositive() {
		u := &L.unmet[0]
		take := decimal.Min(u.qty, s.qty)
		L.peg(s.ref, *u, take)
		u.qty = u.qty.Sub(take)
		s.qty = s.qty.Sub(take)
		if !u.qty.IsPositive() {
			L.unmet = L.unmet[1:]
		}
	}
	return s
}

// addDemand consumes the oldest available supply first, the rest is unmet.
func (L *ledger) addDemand(d layer) {
	for len(L.avail) > 0 && d.qty.IsPositive() {
		a := &L.avail[0]
		take := decimal.Min(a.qty, d.qty)
		L.peg(a.ref, d, take)
		a.qty = a.qty.Sub(take)
		d.qty = d.qty.Sub(take)
		if !a.qty.IsPositive() {
			L.avail = L.avail[1:]
		}
	}
	if d.qty.IsPositive() {
		L.unmet = append(L.unmet, d)
	}
}

func sumQty(ls []layer) decimal.Decimal {
	total := decimal.Zero
	for _, l := range ls {
		total = total.Add(l.qty)
	}
	return total
}

func (L *ledger) check(t int, rest, receipts []Requirement) error {
	it := L.item
	switch it.Coverage {
	case CoverageManual:
		for _, u := range L.unmet {
			L.rs.res.Messages = append(L.rs.res.Messages, Message{Action: ActionShortage, ItemID: it.ID, DemandRef: u.ref, SuggestedDate: u.date, Qty: u.qty})
		}
		L.unmet = nil
		return nil
	case CoverageRequirement, CoveragePeriod:
		need := sumQty(L.unmet)
		if !need.IsPositive() {
			return nil
		}
		if it.NegativeDays > 0 {
			// FR-22.2 negative days (AX semantics): supply already scheduled
			// within NegativeDays after the shortage is waited for instead of
			// planning a new order; finish raises Advance for it.
			limit := t + it.NegativeDays
			for _, r := range receipts {
				if r.Date > limit {
					break
				}
				need = need.Sub(r.Qty)
			}
			for _, p := range L.pending {
				if p.date <= limit {
					need = need.Sub(p.qty)
				}
			}
			if !need.IsPositive() {
				return nil
			}
		}
		if it.Coverage == CoveragePeriod && it.PeriodDays > 1 {
			// FR-22.2 / BR-22.1 step 6: one order covers the net requirement of
			// the whole period, so demand later in the window is added and supply
			// already scheduled inside the window is netted against it. The
			// order never covers less than what is short today.
			limit := t + it.PeriodDays - 1
			window := need
			for _, r := range rest {
				if r.Date > limit {
					break
				}
				window = window.Add(r.Qty)
			}
			for _, r := range receipts {
				if r.Date <= limit {
					window = window.Sub(r.Qty)
				}
			}
			for _, p := range L.pending {
				if p.date <= limit {
					window = window.Sub(p.qty)
				}
			}
			need = decimal.Max(need, window)
		}
		return L.order(t, need)
	case CoverageMinMax:
		// Planned supply that is still in transit counts towards the stock
		// level, so a min/max item is not reordered twice for one shortfall.
		pab := sumQty(L.avail).Add(sumQty(L.pending)).Sub(sumQty(L.unmet))
		if !pab.LessThan(it.ReorderMin) {
			return nil
		}
		target := decimal.Max(it.ReorderMax, it.ReorderMin)
		return L.order(t, target.Sub(pab))
	}
	return nil
}

func (L *ledger) unmetSafetyOnly() bool {
	if len(L.unmet) == 0 {
		return false
	}
	for _, u := range L.unmet {
		if u.refType != RefSafety {
			return false
		}
	}
	return true
}

// order creates planned orders covering need at day t (BR-22.1 steps 6-9).
func (L *ledger) order(t int, need decimal.Decimal) error {
	it := L.item
	today := L.rs.today
	lots, rule, err := LotSize(need, it.MinOrderQty, it.MultipleQty, it.MaxOrderQty, it.StandardQty)
	if err != nil {
		return err
	}
	safetyOnly := L.unmetSafetyOnly()
	for _, q := range lots {
		L.rs.seq++
		number := fmt.Sprintf("PLN-%06d", L.rs.seq)
		delivery := t - it.MarginDays
		orderDate := delivery - it.LeadTimeDays
		if safetyOnly {
			// BR-22.5: the safety-stock order is dated today + lead time.
			delivery, orderDate = today+it.LeadTimeDays, today
		}
		po := PlannedOrder{Number: number, ItemID: it.ID, OrderType: it.OrderType, Qty: q, OrderDate: orderDate, DeliveryDate: delivery, LotRule: rule}
		if orderDate < today {
			// BR-22.1 step 7: the order cannot be placed in the past, so it is
			// expected at today + lead time. The delay message is raised by
			// propagateFutures once delays from components are known.
			futures := today + it.LeadTimeDays
			po.FuturesDate = &futures
			po.DelayDays = futures - delivery
		}
		L.rs.res.Orders = append(L.rs.res.Orders, po)
		supply := layer{ref: number, refType: RefPlanned, date: delivery, qty: q}
		switch {
		case delivery <= t:
			L.addSupply(supply)
		case safetyOnly:
			// The safety-stock order replenishes the safety demand it was
			// created for, so it is pegged to it now. Only its surplus waits for
			// the delivery date; otherwise a later demand would see the safety
			// shortfall as still open and order it a second time.
			if rest := L.cover(supply); rest.qty.IsPositive() {
				L.pending = append(L.pending, rest)
			}
		default:
			L.pending = append(L.pending, supply)
		}
		if it.OrderType == OrderProduction {
			for _, c := range it.Components {
				child := L.rs.index[c.ItemID]
				child.Demand = append(child.Demand, Requirement{Ref: number + "/" + c.ItemID, RefType: RefBOM, Date: orderDate, Qty: c.Qty.Mul(q), Priority: PriorityDependent})
			}
		}
	}
	return nil
}

// finish pegs leftover supply to surplus and raises receipt postpone/advance messages.
func (L *ledger) finish(receipts []Requirement) {
	it := L.item
	for _, a := range L.avail {
		if a.qty.IsPositive() {
			L.peg(a.ref, layer{ref: RefSurplus, refType: RefSurplus, date: a.date}, a.qty)
		}
	}
	for _, r := range receipts {
		earliest, earliestRef, found := 0, "", false
		for _, p := range L.pegs {
			if p.SupplyRef != r.Ref || p.DemandRefType == RefSurplus {
				continue
			}
			if !found || p.DemandDate < earliest {
				earliest, earliestRef, found = p.DemandDate, p.DemandRef, true
			}
		}
		if !found {
			continue
		}
		suggested := earliest - it.MarginDays
		if it.PositiveDays > 0 && earliest-r.Date > it.PositiveDays {
			L.rs.res.Messages = append(L.rs.res.Messages, Message{Action: ActionPostpone, ItemID: it.ID, SupplyRef: r.Ref, DemandRef: earliestRef, SuggestedDate: suggested, Qty: r.Qty})
		} else if r.Date > earliest {
			// The receipt was waited for (negative days) and arrives after the
			// demand it covers: suggest bringing it forward to the demand date.
			L.rs.res.Messages = append(L.rs.res.Messages, Message{Action: ActionAdvance, ItemID: it.ID, SupplyRef: r.Ref, DemandRef: earliestRef, SuggestedDate: suggested, DelayDays: r.Date - earliest, Qty: r.Qty})
		}
	}
}

// driverOf returns the planned order that a bom-line demand ref belongs to:
// dependent demand is referenced as "<planned order>/<component>".
func driverOf(ref string) string {
	if i := strings.Index(ref, "/"); i >= 0 {
		return ref[:i]
	}
	return ref
}

// propagateFutures raises the futures/delay messages of BR-22.1 step 7 and
// propagates each delay upward through the pegging (BR-22.2 "Propagation"):
// a component that arrives late delays the start of the planned production
// order it is pegged to, and a planned order that arrives late delays the
// independent demand (sales line, forecast) it covers. Items are visited from
// the highest low-level code down, so every component's delay is final before
// its parents are read.
func (rs *runState) propagateFutures(levels []*Item) {
	orders := rs.res.Orders
	byNumber := make(map[string]int, len(orders))
	byItem := make(map[string][]int, len(levels))
	for i, o := range orders {
		byNumber[o.Number] = i
		byItem[o.ItemID] = append(byItem[o.ItemID], i)
	}
	pegsOf := make(map[string][]Peg, len(orders))
	for _, p := range rs.res.Pegging {
		if p.DemandRefType != RefSurplus {
			pegsOf[p.SupplyRef] = append(pegsOf[p.SupplyRef], p)
		}
	}
	type lateDemand struct {
		peg   Peg
		ready int
	}
	late := map[string]lateDemand{}
	var lateKeys []string
	for i := len(levels) - 1; i >= 0; i-- {
		it := levels[i]
		for _, oi := range byItem[it.ID] {
			o := &orders[oi]
			if o.FuturesDate == nil {
				continue
			}
			ready := *o.FuturesDate + it.MarginDays
			for _, p := range pegsOf[o.Number] {
				if ready <= p.DemandDate {
					continue
				}
				switch p.DemandRefType {
				case RefSafety:
					// Safety stock is a buffer, not a customer date: no delay.
				case RefBOM:
					pi, ok := byNumber[driverOf(p.DemandRef)]
					if !ok {
						continue
					}
					parent := &orders[pi]
					delivery := ready + rs.index[parent.ItemID].LeadTimeDays
					if parent.FuturesDate == nil || delivery > *parent.FuturesDate {
						parent.FuturesDate = &delivery
						parent.DelayDays = delivery - parent.DeliveryDate
					}
				default:
					key := it.ID + "\x00" + p.DemandRef
					prev, seen := late[key]
					if !seen {
						lateKeys = append(lateKeys, key)
					}
					if !seen || ready > prev.ready {
						late[key] = lateDemand{peg: p, ready: ready}
					}
				}
			}
		}
	}
	for _, o := range orders {
		if o.FuturesDate == nil || o.DelayDays <= 0 {
			continue
		}
		demandRef := ""
		if pegs := pegsOf[o.Number]; len(pegs) > 0 {
			demandRef = pegs[0].DemandRef
		}
		rs.res.Messages = append(rs.res.Messages, Message{Action: ActionDelay, ItemID: o.ItemID, SupplyRef: o.Number, DemandRef: demandRef,
			SuggestedDate: *o.FuturesDate, DelayDays: o.DelayDays, Qty: o.Qty})
	}
	for _, key := range lateKeys {
		l := late[key]
		rs.res.Messages = append(rs.res.Messages, Message{Action: ActionDelay, ItemID: l.peg.ItemID, SupplyRef: l.peg.SupplyRef, DemandRef: l.peg.DemandRef,
			SuggestedDate: l.ready, DelayDays: l.ready - l.peg.DemandDate, Qty: l.peg.Qty})
	}
}

// LotSize applies minimum, standard, multiple and maximum order quantities
// (FR-22.4). A quantity above the maximum splits into ceil(q/max) orders.
func LotSize(need, minQty, multiple, maxQty, standard decimal.Decimal) ([]decimal.Decimal, string, error) {
	q := need
	var rules []string
	if minQty.IsPositive() && q.LessThan(minQty) {
		q = minQty
		rules = append(rules, "min")
	}
	if standard.IsPositive() {
		q = roundUp(q, standard)
		rules = append(rules, "standard")
	}
	if multiple.IsPositive() {
		q = roundUp(q, multiple)
		rules = append(rules, "multiple")
	}
	name := "lot-for-lot"
	if len(rules) > 0 {
		name = strings.Join(rules, "+")
	}
	if !maxQty.IsPositive() || q.LessThanOrEqual(maxQty) {
		return []decimal.Decimal{q}, name, nil
	}
	n := q.Div(maxQty).Ceil()
	if n.GreaterThan(decimal.NewFromInt(MaxSplits)) {
		return nil, "", ErrTooManySplits
	}
	count := int(n.IntPart())
	lots := make([]decimal.Decimal, 0, count)
	rest := q
	for i := 0; i < count-1; i++ {
		lots = append(lots, maxQty)
		rest = rest.Sub(maxQty)
	}
	lots = append(lots, rest)
	return lots, name + "+max", nil
}

func roundUp(q, step decimal.Decimal) decimal.Decimal {
	return q.Div(step).Ceil().Mul(step)
}

// TreeNode is one node of the explain tree: a planned order with the demand it
// covers, or a leaf demand.
type TreeNode struct {
	Ref      string
	RefType  string
	Qty      decimal.Decimal
	Children []TreeNode
}

// PegLine is a peg row as the store returns it for a supply reference.
type PegLine struct {
	DemandRef, DemandRefType string
	Qty                      decimal.Decimal
}

// BuildTree expands a planned order into the demand it serves. A bom-line demand
// is expanded through the planned order that drives it, to depth levels.
func BuildTree(ref string, depth int, pegsOf func(string) []PegLine) TreeNode {
	node := TreeNode{Ref: ref, RefType: RefPlanned, Qty: decimal.Zero}
	if depth <= 0 {
		return node
	}
	for _, p := range pegsOf(ref) {
		child := TreeNode{Ref: p.DemandRef, RefType: p.DemandRefType, Qty: p.Qty}
		if p.DemandRefType == RefBOM {
			sub := BuildTree(driverOf(p.DemandRef), depth-1, pegsOf)
			sub.Qty = p.Qty
			child = sub
		}
		node.Children = append(node.Children, child)
	}
	return node
}

// pegLess and msgLess are total orders over every field, so the sorted output
// (and the plan hash over it) does not depend on the sort algorithm.
func pegLess(a, b Peg) bool {
	if a.SupplyRef != b.SupplyRef {
		return a.SupplyRef < b.SupplyRef
	}
	if a.DemandRef != b.DemandRef {
		return a.DemandRef < b.DemandRef
	}
	if a.DemandDate != b.DemandDate {
		return a.DemandDate < b.DemandDate
	}
	if a.ItemID != b.ItemID {
		return a.ItemID < b.ItemID
	}
	if a.DemandRefType != b.DemandRefType {
		return a.DemandRefType < b.DemandRefType
	}
	return a.Qty.LessThan(b.Qty)
}

func msgLess(a, b Message) bool {
	if a.Action != b.Action {
		return a.Action < b.Action
	}
	if a.SupplyRef != b.SupplyRef {
		return a.SupplyRef < b.SupplyRef
	}
	if a.DemandRef != b.DemandRef {
		return a.DemandRef < b.DemandRef
	}
	if a.ItemID != b.ItemID {
		return a.ItemID < b.ItemID
	}
	if a.SuggestedDate != b.SuggestedDate {
		return a.SuggestedDate < b.SuggestedDate
	}
	if a.DelayDays != b.DelayDays {
		return a.DelayDays < b.DelayDays
	}
	return a.Qty.LessThan(b.Qty)
}

// dedupeMessages drops exact repeats from sorted messages: a top-level planned
// order's own delay and the delay of the one demand it covers are the same line.
func dedupeMessages(in []Message) []Message {
	out := in[:0]
	for i, m := range in {
		if i > 0 && !msgLess(in[i-1], m) && !msgLess(m, in[i-1]) {
			continue
		}
		out = append(out, m)
	}
	return out
}

// hashResult fingerprints orders, pegging and messages in canonical order (FR-22.14).
func hashResult(r Result) string {
	h := sha256.New()
	for _, o := range r.Orders {
		futures := ""
		if o.FuturesDate != nil {
			futures = fmt.Sprint(*o.FuturesDate)
		}
		fmt.Fprintf(h, "O|%s|%s|%s|%s|%d|%d|%s|%d|%s\n", o.Number, o.ItemID, o.OrderType, o.Qty, o.OrderDate, o.DeliveryDate, futures, o.DelayDays, o.LotRule)
	}
	for _, p := range r.Pegging {
		fmt.Fprintf(h, "P|%s|%s|%s|%d|%s\n", p.SupplyRef, p.DemandRef, p.DemandRefType, p.DemandDate, p.Qty)
	}
	for _, m := range r.Messages {
		fmt.Fprintf(h, "M|%s|%s|%s|%s|%d|%d|%s\n", m.Action, m.ItemID, m.SupplyRef, m.DemandRef, m.SuggestedDate, m.DelayDays, m.Qty)
	}
	return fmt.Sprintf("%x", h.Sum(nil))
}
