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
	RefBuffer   = "buffer"

	ActionDelay    = "delay"
	ActionPostpone = "postpone"
	ActionAdvance  = "advance"
	ActionShortage = "shortage"
	ActionCancel   = "cancel"
	ActionDecrease = "decrease"
	ActionIncrease = "increase"
	// ActionRedZone is the PLAN-0010 message: a DDMRP buffer is in its red zone (FR-22.16).
	ActionRedZone = "red-zone"
	// ActionSalesOnTop is the PLAN-0015 information message: a sales line under
	// assemble-to-order-final found less forecast to consume than its quantity,
	// so the excess is planned on top of the forecast left (BR-22.13, FR-22.19).
	ActionSalesOnTop = "sales-on-top"
	// ActionLotSplit is the PLAN-0005 warning: a requirement above the item's
	// maximum order quantity was split into several planned orders (FR-22.4).
	// delay_days carries the number of orders and qty the maximum order quantity.
	ActionLotSplit = "lot-split"

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
	// Gross marks a forecast planned gross (mts-gross, FR-22.19): it is netted
	// against expected receipts and planned supply but not against on-hand, so
	// the plan covers the forecast whatever stock there is.
	Gross bool
}

// BOMLine is one component of an item; Qty is per unit of the parent.
type BOMLine struct {
	ItemID string
	Qty    decimal.Decimal
}

// Item is the planning snapshot of one item at one site.
type Item struct {
	ID         string
	Coverage   string
	PeriodDays int
	MarginDays int
	// IssueMarginDays moves each demand of the item earlier by that many working
	// days (FR-22.5); ReorderMarginDays adds working days to the lead time of its
	// planned orders, so they are released earlier. Safety stock is not an issue.
	IssueMarginDays, ReorderMarginDays int
	PositiveDays, NegativeDays         int
	// FreezeDays, ActionDays and FuturesDays are the time fences of BR-22.4 in
	// days from today. FreezeDays 0 freezes nothing; ActionDays and
	// FuturesDays 0 mean the fence is unlimited.
	FreezeDays, ActionDays, FuturesDays int
	// CoverageDays is the coverage time fence of FR-22.2 in days from today.
	// Existing supply dated at or beyond it that is not (fully) pegged to
	// demand raises Cancel or Decrease (BR-22.1 step 10). 0 raises neither.
	CoverageDays int
	// FirmingDays is the firming time fence of FR-22.2 in days from today; 0
	// means no fence. With AutoFirm set, a planned order whose order date
	// falls inside it is firmed by the run itself (BR-22.4).
	FirmingDays            int
	AutoFirm               bool
	ReorderMin, ReorderMax decimal.Decimal
	OnHand, SafetyStock    decimal.Decimal
	// SafetyPolicy is SafetyToday (also when empty) or SafetyFirstIssue: the
	// safety stock is a demand at today or at the item's first issue date (BR-22.5).
	SafetyPolicy             string
	OrderType                string
	LeadTimeDays             int
	MinOrderQty, MultipleQty decimal.Decimal
	MaxOrderQty, StandardQty decimal.Decimal
	Demand, Receipts         []Requirement
	Components               []BOMLine
	// SalesOnTop lists the sales lines (Date a day index, Qty the excess) that
	// an assemble-to-order-final strategy could not consume from the forecast;
	// the run raises one PLAN-0015 message for each. They do not change netting.
	SalesOnTop []Requirement
}

// leadTotal is the lead time plus the reorder margin (FR-22.5).
func (it *Item) leadTotal() int { return it.LeadTimeDays + it.ReorderMarginDays }

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
	// AutoFirmed marks an order the firming time fence firms during the run
	// (BR-22.4); the store firms it in the run's transaction.
	AutoFirmed bool
	// VendorID, QuotaArrangementID and QuotaSourceID are set by ApplyQuota when
	// a quota arrangement allocated the order to a source (FR-22.17).
	VendorID, QuotaArrangementID, QuotaSourceID string
	// PlanningPriority and BufferZone are set by AddBufferResults on an order
	// that replenishes a DDMRP buffer (FR-22.16, BR-22.11): the buffer's
	// net-flow position over its top of green, and the zone it is in.
	PlanningPriority *decimal.Decimal
	BufferZone       string
	// WarehouseID is set by SplitWarehouses on an order of a warehouse planning
	// scope (FR-22.18); it is empty for an order of the site scope.
	WarehouseID string
}

// Peg links one unit of supply to the demand it covers (ReqTransCov).
type Peg struct {
	ItemID, SupplyRef, DemandRef, DemandRefType string
	DemandDate                                  int
	Qty                                         decimal.Decimal
	// WarehouseID is the warehouse scope of ItemID, empty for the site scope.
	WarehouseID string
}

// Message is an action or delay message for the planner.
type Message struct {
	Action, ItemID, SupplyRef, DemandRef string
	SuggestedDate, DelayDays             int
	Qty                                  decimal.Decimal
	// WarehouseID is the warehouse scope of ItemID, empty for the site scope.
	WarehouseID string
}

// Result is the output of one run.
type Result struct {
	Orders   []PlannedOrder
	Pegging  []Peg
	Messages []Message
	// Net is the time-phased net requirements view (FR-22.12) of every item,
	// ordered by item then day. It is derived from the orders and the snapshot,
	// so it is not part of Hash.
	Net  []NetRow
	Hash string
}

type layer struct {
	ref, refType string
	date         int
	priority     int
	qty          decimal.Decimal
	gross        bool // not netted against on-hand (Requirement.Gross)
}

type runState struct {
	today int
	cal   *Calendar // nil: every day is a working day
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
	// receipts are the item's existing supply sorted by date; increased maps
	// a receipt ref to its Increase message in rs.res.Messages (FR-22.7).
	receipts  []Requirement
	increased map[string]int
}

// Run plans every item with lead times and margins in calendar days. Items are
// processed level by level (low-level code ascending, then ID) so dependent
// demand from a parent is complete before the child is netted. The input
// slices are not modified.
func Run(today int, in []Item) (Result, error) {
	return RunWithCalendar(today, nil, in)
}

// RunWithCalendar plans like Run with the site work calendar cal (FR-22.5,
// BR-22.8): lead times and margins count working days, a demand on a closed
// day moves forward and a supply date on a closed day moves backward to the
// nearest open day. A nil calendar counts calendar days.
func RunWithCalendar(today int, cal *Calendar, in []Item) (Result, error) {
	if cal != nil {
		if err := cal.Validate(); err != nil {
			return Result{}, err
		}
	}
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
	rs := &runState{today: today, cal: cal, index: index}
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
	rs.res.Net = rs.netView(order)
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
	if it.ID == "" || it.LeadTimeDays < 0 || it.MarginDays < 0 || it.IssueMarginDays < 0 || it.ReorderMarginDays < 0 || it.OnHand.IsNegative() || it.SafetyStock.IsNegative() {
		return fmt.Errorf("%w: %q lead time, margin, on-hand or safety stock", ErrInvalidItem, it.ID)
	}
	switch it.SafetyPolicy {
	case "", SafetyToday, SafetyFirstIssue:
	default:
		return fmt.Errorf("%w: %q safety-stock policy %q", ErrInvalidItem, it.ID, it.SafetyPolicy)
	}
	if it.FreezeDays < 0 || it.ActionDays < 0 || it.FuturesDays < 0 || it.CoverageDays < 0 || it.FirmingDays < 0 {
		return fmt.Errorf("%w: %q time fences must not be negative", ErrInvalidItem, it.ID)
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
		d.Date = rs.issueDate(item, d.Date)
		demands = append(demands, d)
	}
	if item.SafetyStock.IsPositive() {
		demands = append(demands, Requirement{Ref: RefSafety + ":" + item.ID, RefType: RefSafety, Date: rs.safetyDate(item), Qty: item.SafetyStock, Priority: PrioritySafety})
	}
	sort.SliceStable(demands, func(a, b int) bool { return reqLess(demands[a], demands[b]) })
	receipts := slices.Clone(item.Receipts)
	for i := range receipts {
		receipts[i].Date = rs.supplyDate(receipts[i].Date)
	}
	sort.SliceStable(receipts, func(a, b int) bool {
		if receipts[a].Date != receipts[b].Date {
			return receipts[a].Date < receipts[b].Date
		}
		return receipts[a].Ref < receipts[b].Ref
	})

	L := &ledger{rs: rs, item: item, receipts: receipts}
	if item.OnHand.IsPositive() {
		L.addSupply(layer{ref: RefOnHand + ":" + item.ID, refType: RefOnHand, date: rs.today, qty: item.OnHand})
	}
	start := rs.demandDate(rs.today)
	if item.Coverage == CoverageMinMax {
		// BR-22.1 step 5: a min/max item is checked at today even with no events,
		// so a stock level below minimum always reorders. Orders created here are
		// counted as supply by the walk below. When something happens on or
		// before today, the walk checks today itself after netting it, so the
		// stock level is PAB after today's receipts and requirements.
		if t0, ok := nextTime(demands, 0, receipts, 0, nil); !ok || t0 > start {
			if err := L.check(start, nil, nil); err != nil {
				return err
			}
		}
	}
	di, ri := 0, 0
	for {
		t, ok := nextTime(demands, di, receipts, ri, L.pending)
		if !ok {
			break
		}
		if item.Coverage == CoverageMinMax && t < start {
			// An overdue receipt is part of today's stock level, not a check of
			// its own before today's requirements are seen.
			t = start
		}
		for ri < len(receipts) && receipts[ri].Date <= t {
			r := receipts[ri]
			L.addSupply(layer{ref: r.Ref, refType: r.RefType, date: r.Date, qty: r.Qty})
			ri++
		}
		L.releasePending(t)
		for di < len(demands) && demands[di].Date <= t {
			d := demands[di]
			L.addDemand(layer{ref: d.Ref, refType: d.RefType, date: d.Date, priority: d.Priority, qty: d.Qty, gross: d.Gross})
			di++
		}
		if err := L.check(t, demands[di:], receipts[ri:]); err != nil {
			return err
		}
	}
	L.finish(receipts)
	for _, o := range item.SalesOnTop {
		rs.res.Messages = append(rs.res.Messages, Message{Action: ActionSalesOnTop, ItemID: item.ID, DemandRef: o.Ref, SuggestedDate: o.Date, Qty: o.Qty})
	}
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

// addDemand consumes the oldest available supply first, the rest is unmet. A
// gross demand (Requirement.Gross) leaves the on-hand layer alone.
func (L *ledger) addDemand(d layer) {
	for i := 0; i < len(L.avail) && d.qty.IsPositive(); {
		a := &L.avail[i]
		if d.gross && a.refType == RefOnHand {
			i++
			continue
		}
		take := decimal.Min(a.qty, d.qty)
		L.peg(a.ref, d, take)
		a.qty = a.qty.Sub(take)
		d.qty = d.qty.Sub(take)
		if !a.qty.IsPositive() {
			L.avail = slices.Delete(L.avail, i, i+1)
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
		// Supply dated on or before netted is already netted against need (the
		// walk adds what is due by t; negative days subtract what is waited
		// for), so the period window below must not count it again.
		netted := t
		if it.NegativeDays > 0 {
			// FR-22.2 negative days (AX semantics): supply already scheduled
			// within NegativeDays after the shortage is waited for instead of
			// planning a new order; finish raises Advance for it.
			limit := t + it.NegativeDays
			netted = limit
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
			// order is the sum of the window's net requirements: the deepest
			// cumulative shortfall on any day of the window, so supply that
			// arrives after a requirement in the window does not cover it. The
			// order never covers less than what is short today.
			need = decimal.Max(need, L.windowShortfall(need, netted, t+it.PeriodDays-1, rest, receipts))
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

// windowShortfall walks the days up to limit from a shortfall of need and
// returns the largest cumulative shortfall: requirements in rest add to it,
// receipts and pending planned supply dated after netted (supply on or before
// it is already in need) reduce it from their own day on.
func (L *ledger) windowShortfall(need decimal.Decimal, netted, limit int, rest, receipts []Requirement) decimal.Decimal {
	type move struct {
		day int
		qty decimal.Decimal
	}
	var moves []move
	for _, r := range rest {
		if r.Date <= limit {
			moves = append(moves, move{r.Date, r.Qty})
		}
	}
	for _, r := range receipts {
		if r.Date > netted && r.Date <= limit {
			moves = append(moves, move{r.Date, r.Qty.Neg()})
		}
	}
	for _, p := range L.pending {
		if p.date > netted && p.date <= limit {
			moves = append(moves, move{p.date, p.qty.Neg()})
		}
	}
	sort.SliceStable(moves, func(a, b int) bool { return moves[a].day < moves[b].day })
	short, deepest := need, need
	for i := 0; i < len(moves); {
		day := moves[i].day
		for ; i < len(moves) && moves[i].day == day; i++ {
			short = short.Add(moves[i].qty)
		}
		deepest = decimal.Max(deepest, short)
	}
	return deepest
}

// increase raises Increase (FR-22.7) for a shortage at day t inside the
// freeze fence, where no planned order may be created (BR-22.4). The latest
// existing receipt dated on or before t is already fully pegged (FIFO), so
// raising its quantity by the shortage covers the demand on time; its date
// stays. Only a receipt inside the action fence is asked. Later shortages of
// the same receipt add to its one Increase message. A shortage of safety
// stock alone raises nothing: the buffer is not a customer need.
func (L *ledger) increase(t int) {
	need := decimal.Zero
	demandRef := ""
	for _, u := range L.unmet {
		if u.refType == RefSafety {
			continue
		}
		if demandRef == "" {
			demandRef = u.ref
		}
		need = need.Add(u.qty)
	}
	if !need.IsPositive() {
		return
	}
	var r *Requirement
	for i := range L.receipts {
		if L.receipts[i].Date > t {
			break
		}
		r = &L.receipts[i]
	}
	if r == nil || !withinFence(L.rs.today, r.Date, L.item.ActionDays) {
		return
	}
	if i, ok := L.increased[r.Ref]; ok {
		m := &L.rs.res.Messages[i]
		m.Qty = m.Qty.Add(need)
		return
	}
	if L.increased == nil {
		L.increased = map[string]int{}
	}
	L.increased[r.Ref] = len(L.rs.res.Messages)
	L.rs.res.Messages = append(L.rs.res.Messages, Message{Action: ActionIncrease, ItemID: L.item.ID, SupplyRef: r.Ref, DemandRef: demandRef, SuggestedDate: r.Date, Qty: need})
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
	if frozen(today, t, it.FreezeDays) {
		// BR-22.4: a requirement inside the freeze fence gets no planned
		// order. What is still uncovered is reported as a shortage, so every
		// demand is either pegged in full or has a message for the rest, and
		// an existing receipt that could cover it is asked to increase.
		L.increase(t)
		for _, u := range L.unmet {
			L.rs.res.Messages = append(L.rs.res.Messages, Message{Action: ActionShortage, ItemID: it.ID, DemandRef: u.ref, SuggestedDate: u.date, Qty: u.qty})
		}
		L.unmet = nil
		return nil
	}
	lots, rule, err := LotSize(need, it.MinOrderQty, it.MultipleQty, it.MaxOrderQty, it.StandardQty)
	if err != nil {
		return err
	}
	safetyOnly := L.unmetSafetyOnly()
	firstOrder := len(L.rs.res.Orders)
	for _, q := range lots {
		L.rs.seq++
		number := fmt.Sprintf("PLN-%06d", L.rs.seq)
		delivery := L.rs.back(t, it.MarginDays)
		orderDate := L.rs.back(delivery, it.leadTotal())
		if safetyOnly {
			// BR-22.5: the safety-stock order is dated today + lead time.
			orderDate = L.rs.demandDate(today)
			delivery = L.rs.forward(orderDate, it.leadTotal())
		}
		po := PlannedOrder{Number: number, ItemID: it.ID, OrderType: it.OrderType, Qty: q, OrderDate: orderDate, DeliveryDate: delivery, LotRule: rule,
			AutoFirmed: it.AutoFirm && insideFirmingFence(today, orderDate, it.FirmingDays)}
		if orderDate < today {
			// BR-22.1 step 7: the order cannot be placed in the past, so it is
			// expected at today + lead time. The delay message is raised by
			// propagateFutures once delays from components are known.
			futures := L.rs.forward(today, it.leadTotal())
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
		// A production order issues its components at the order date. So does a
		// planned transfer: a warehouse scope's transfer lists the site item as its
		// only component, which is the demand at the from-warehouse at the delivery
		// date less the transfer lead time (BR-22.7, FR-22.18).
		if it.OrderType == OrderProduction || it.OrderType == OrderTransfer {
			for _, c := range it.Components {
				child := L.rs.index[c.ItemID]
				child.Demand = append(child.Demand, Requirement{Ref: number + "/" + c.ItemID, RefType: RefBOM, Date: orderDate, Qty: c.Qty.Mul(q), Priority: PriorityDependent})
			}
		}
	}
	if len(lots) > 1 {
		// PLAN-0005: one warning per split requirement, naming its first order.
		L.rs.res.Messages = append(L.rs.res.Messages, Message{Action: ActionLotSplit, ItemID: it.ID, SupplyRef: L.rs.res.Orders[firstOrder].Number,
			SuggestedDate: t, DelayDays: len(lots), Qty: it.MaxOrderQty})
	}
	return nil
}

// finish pegs leftover supply to surplus and raises the receipt messages:
// postpone/advance for pegged supply, cancel/decrease for surplus supply
// beyond the coverage time fence.
func (L *ledger) finish(receipts []Requirement) {
	it := L.item
	for _, a := range L.avail {
		if a.qty.IsPositive() {
			L.peg(a.ref, layer{ref: RefSurplus, refType: RefSurplus, date: a.date}, a.qty)
		}
	}
	for _, r := range receipts {
		earliest, earliestRef, found := 0, "", false
		pegged := decimal.Zero
		for _, p := range L.pegs {
			if p.SupplyRef != r.Ref || p.DemandRefType == RefSurplus {
				continue
			}
			pegged = pegged.Add(p.Qty)
			if !found || p.DemandDate < earliest {
				earliest, earliestRef, found = p.DemandDate, p.DemandRef, true
			}
		}
		if !withinFence(L.rs.today, r.Date, it.ActionDays) {
			// BR-22.4: action messages only for supply inside the action fence.
			continue
		}
		// BR-22.1 step 10: unpegged supply beyond the coverage time fence
		// raises Cancel (nothing pegged) or Decrease (by the unpegged part).
		// Inside the fence surplus stays as it is (BR-22.2: PO-1's 400).
		if surplus := r.Qty.Sub(pegged); surplus.IsPositive() && beyondFence(L.rs.today, r.Date, it.CoverageDays) {
			if found {
				L.rs.res.Messages = append(L.rs.res.Messages, Message{Action: ActionDecrease, ItemID: it.ID, SupplyRef: r.Ref, DemandRef: earliestRef, SuggestedDate: r.Date, Qty: surplus})
			} else {
				L.rs.res.Messages = append(L.rs.res.Messages, Message{Action: ActionCancel, ItemID: it.ID, SupplyRef: r.Ref, SuggestedDate: r.Date, Qty: surplus})
			}
		}
		if !found {
			continue
		}
		suggested := L.rs.back(earliest, it.MarginDays)
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
			ready := rs.forward(*o.FuturesDate, it.MarginDays)
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
					// The component's issue margin is a cushion before the
					// parent starts (FR-22.5): the parent can start that many
					// working days after the component is ready.
					start := rs.forward(ready, it.IssueMarginDays)
					delivery := rs.forward(start, rs.index[parent.ItemID].leadTotal())
					if delivery <= parent.DeliveryDate {
						continue
					}
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
	// BR-22.4: futures (delay) messages only for requirements inside the
	// futures fence. The futures dates themselves still propagate.
	for _, o := range orders {
		if o.FuturesDate == nil || o.DelayDays <= 0 || !withinFence(rs.today, o.DeliveryDate, rs.index[o.ItemID].FuturesDays) {
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
		if !withinFence(rs.today, l.peg.DemandDate, rs.index[l.peg.ItemID].FuturesDays) {
			continue
		}
		rs.res.Messages = append(rs.res.Messages, Message{Action: ActionDelay, ItemID: l.peg.ItemID, SupplyRef: l.peg.SupplyRef, DemandRef: l.peg.DemandRef,
			SuggestedDate: l.ready, DelayDays: l.ready - l.peg.DemandDate, Qty: l.peg.Qty})
	}
}

// demandDate is the day a demand is netted on: never before today and, with a
// calendar, moved forward off a closed day (BR-22.8).
func (rs *runState) demandDate(day int) int {
	day = max(day, rs.today)
	if rs.cal == nil {
		return day
	}
	return rs.cal.OpenAfter(day)
}

// supplyDate moves a receipt on a closed day backward to the nearest open day
// (BR-22.8); without a calendar the day stays.
func (rs *runState) supplyDate(day int) int {
	if rs.cal == nil {
		return day
	}
	return rs.cal.OpenBefore(day)
}

// back offsets a supply-side day n working days earlier (calendar days
// without a calendar), as for a receipt margin or a lead time (FR-22.5).
func (rs *runState) back(day, n int) int {
	if rs.cal == nil {
		return day - n
	}
	return rs.cal.Back(day, n)
}

// forward offsets a day n working days later (calendar days without a
// calendar), as for the futures date today + lead time.
func (rs *runState) forward(day, n int) int {
	if rs.cal == nil {
		return day + n
	}
	return rs.cal.Forward(day, n)
}

// frozen reports whether day lies inside a freeze fence of n days from today.
// A freeze of 0 days freezes nothing.
func frozen(today, day, n int) bool {
	return n > 0 && day-today < n
}

// beyondFence reports whether day lies at or beyond a coverage time fence of n
// days from today. A fence of 0 days is off: nothing lies beyond it.
func beyondFence(today, day, n int) bool {
	return n > 0 && day-today >= n
}

// insideFirmingFence reports whether an order dated day must be placed inside
// the firming time fence of n days from today (BR-22.4). An order already past
// due is inside it; a fence of 0 days firms nothing.
func insideFirmingFence(today, day, n int) bool {
	return n > 0 && day-today < n
}

// withinFence reports whether day lies inside an action or futures fence of n
// days from today. A fence of 0 days is unlimited.
func withinFence(today, day, n int) bool {
	return n == 0 || day-today < n
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
		if o.AutoFirmed {
			// Only auto-firmed orders add a line, so hashes of runs without a
			// firming fence are unchanged.
			fmt.Fprintf(h, "F|%s\n", o.Number)
		}
	}
	for _, p := range r.Pegging {
		fmt.Fprintf(h, "P|%s|%s|%s|%d|%s\n", p.SupplyRef, p.DemandRef, p.DemandRefType, p.DemandDate, p.Qty)
	}
	for _, m := range r.Messages {
		fmt.Fprintf(h, "M|%s|%s|%s|%s|%d|%d|%s\n", m.Action, m.ItemID, m.SupplyRef, m.DemandRef, m.SuggestedDate, m.DelayDays, m.Qty)
	}
	return fmt.Sprintf("%x", h.Sum(nil))
}

// safetyDate is the day an item's safety stock is netted as a demand (BR-22.5):
// today, or with policy first-issue the earliest day of its other demands. An
// item with no other demand keeps the safety stock at today so the buffer is
// still built.
func (rs *runState) safetyDate(it *Item) int {
	day := rs.demandDate(rs.today)
	if it.SafetyPolicy != SafetyFirstIssue || len(it.Demand) == 0 {
		return day
	}
	first := rs.issueDate(it, it.Demand[0].Date)
	for _, d := range it.Demand[1:] {
		first = min(first, rs.issueDate(it, d.Date))
	}
	return first
}

// issueDate is the day a demand of it is netted on: the issue margin moves it
// that many working days earlier (FR-22.5), counted from the working day the
// demand would fall on without a margin, and never before today (BR-22.8).
func (rs *runState) issueDate(it *Item, day int) int {
	if it.IssueMarginDays > 0 {
		day = rs.back(rs.demandDate(day), it.IssueMarginDays)
	}
	return rs.demandDate(day)
}
