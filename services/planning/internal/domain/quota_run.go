package domain

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/shopspring/decimal"
)

// RunQuota is one stored quota arrangement as a plan run reads it (FR-22.17).
// Booked holds the firmed and approved planned orders already allocated to its
// sources, so a per-period maximum counts what earlier runs and firming gave a
// source in the period, not only the orders of this run.
type RunQuota struct {
	ArrangementID string
	Spec          QuotaArrangementSpec
	Booked        []QuotaBooking
}

// QuotaBooking is the quantity of one order allocated to a source, by the
// order's delivery day.
type QuotaBooking struct {
	SourceID string
	Day      time.Time
	Qty      decimal.Decimal
}

// QuotaOutcome counts what ApplyQuota did, for the run's reporting.
type QuotaOutcome struct {
	// Allocated is the number of planned orders given to a source, Split the
	// number of extra orders created by splitting, Unassigned the orders of an
	// item with an arrangement that no source could take (PLAN-0009).
	Allocated, Split, Unassigned int
	// UnassignedOrders are the numbers of those orders, in delivery order.
	UnassignedOrders []string
}

// ActionNoQuotaSource is the PLAN-0009 message: a planned order of an item with a
// quota arrangement valid on its delivery day that no source of the arrangement
// could take (FR-22.17). The order stays as planned, without a source. SupplyRef is
// the order, DemandRef the arrangement id, SuggestedDate the delivery day (the
// period the arrangement had no usable source in) and Qty the order quantity.
const ActionNoQuotaSource = "no-quota-source"

// sourceOrderType maps a quota source type to the planned order type it supplies.
var sourceOrderType = map[string]string{SourceVendor: OrderPurchase, SourceProduction: OrderProduction, SourceTransfer: OrderTransfer}

// ApplyQuota allocates the planned orders of a run to the sources of the quota
// arrangement that is valid on the order's delivery day (FR-22.17, BR-22.12).
// asOf is day 0. Orders are taken in delivery order, so the ratings move as
// the run allocates; a source's allocated quantity starts from the stored one.
// A source takes only orders of its own type (vendor: purchase; production
// version: production; warehouse: transfer). A source with a per-period
// maximum takes no more than its remaining quantity in the period (counted in
// periods of its length from the arrangement's validity start), after the
// booked orders of that period. Without split, a source whose remainder cannot
// take the whole order is passed over; with split, a share above a source's
// remainder is cut to it and the rest is divided over the other sources. A
// split arrangement divides an order over several sources: the order becomes
// one order per source, numbered "<number>-<n>" (never "/", which marks a
// bom-line demand reference), the pegging of the order is divided over them in
// order, and the bom-line demand a split production order drives is divided
// over the parts in proportion to their quantities. An order no source can take stays as
// planned, without a source, and raises one PLAN-0009 message (ActionNoQuotaSource).
//
// The plan hash is recomputed when an order was allocated or a message raised, so
// it identifies the orders and messages as stored (as the scope filter does); it
// is unchanged otherwise.
func ApplyQuota(asOf time.Time, res Result, quotas []RunQuota) (Result, QuotaOutcome, error) {
	var out QuotaOutcome
	if len(quotas) == 0 || len(res.Orders) == 0 {
		return res, out, nil
	}
	byItem := map[string][]*runQuotaState{}
	for _, q := range quotas {
		st := &runQuotaState{RunQuota: q, usage: map[string]map[int]decimal.Decimal{}, allocated: map[string]decimal.Decimal{}}
		periodDays := map[string]*int{}
		for _, s := range q.Spec.Sources {
			st.allocated[s.ID] = s.Allocated
			periodDays[s.ID] = s.PeriodDays
		}
		for _, b := range q.Booked {
			pd, known := periodDays[b.SourceID]
			if !known || pd == nil || b.Day.Before(q.Spec.ValidFrom) || b.Day.After(q.Spec.ValidTo) {
				continue
			}
			st.use(b.SourceID, st.periodOf(*pd, b.Day), b.Qty)
		}
		byItem[q.Spec.ItemID] = append(byItem[q.Spec.ItemID], st)
	}
	order := make([]int, len(res.Orders))
	for i := range order {
		order[i] = i
	}
	slices.SortStableFunc(order, func(a, b int) int {
		if d := res.Orders[a].DeliveryDate - res.Orders[b].DeliveryDate; d != 0 {
			return d
		}
		return compareStrings(res.Orders[a].Number, res.Orders[b].Number)
	})
	assigned := map[int][]quotaShare{}
	var unassigned []Message
	for _, i := range order {
		o := res.Orders[i]
		day := asOf.AddDate(0, 0, o.DeliveryDate)
		for _, st := range byItem[o.ItemID] {
			if day.Before(st.Spec.ValidFrom) || day.After(st.Spec.ValidTo) {
				continue
			}
			shares, ok, err := st.allocate(o, day)
			if err != nil {
				return res, out, err
			}
			if ok {
				assigned[i] = shares
				out.Allocated++
				out.Split += len(shares) - 1
			} else {
				out.Unassigned++
				out.UnassignedOrders = append(out.UnassignedOrders, o.Number)
				unassigned = append(unassigned, Message{Action: ActionNoQuotaSource, ItemID: o.ItemID, SupplyRef: o.Number,
					DemandRef: st.ArrangementID, SuggestedDate: o.DeliveryDate, Qty: o.Qty})
			}
			break
		}
	}
	next := Result{Pegging: slices.Clone(res.Pegging), Messages: res.Messages, Net: res.Net, Hash: res.Hash}
	splitPegs := map[string][]string{}
	for i, o := range res.Orders {
		shares, ok := assigned[i]
		if !ok {
			next.Orders = append(next.Orders, o)
			continue
		}
		if len(shares) == 1 {
			next.Orders = append(next.Orders, shares[0].stamp(o))
			continue
		}
		for n, sh := range shares {
			c := sh.stamp(o)
			c.Number = splitNumber(o.Number, n)
			c.Qty = sh.qty
			next.Orders = append(next.Orders, c)
			splitPegs[o.Number] = append(splitPegs[o.Number], c.Number)
		}
	}
	if len(splitPegs) > 0 {
		pegs, err := dividePegs(next.Pegging, res.Orders, assigned, splitPegs)
		if err != nil {
			return res, out, err
		}
		next.Pegging = pegs
	}
	if len(unassigned) > 0 {
		msgs := append(slices.Clone(res.Messages), unassigned...)
		slices.SortStableFunc(msgs, func(a, b Message) int {
			switch {
			case msgLess(a, b):
				return -1
			case msgLess(b, a):
				return 1
			}
			return 0
		})
		next.Messages = dedupeMessages(msgs)
	}
	if out.Allocated > 0 || len(unassigned) > 0 {
		next.Hash = hashQuotaResult(next)
	}
	return next, out, nil
}

// hashQuotaResult is the plan hash of a result with quota applied: the engine
// hash lines plus one line per allocated order, so two runs whose orders went
// to other sources do not report equal plans.
func hashQuotaResult(r Result) string {
	h := sha256.New()
	h.Write([]byte(hashResult(r)))
	for _, o := range r.Orders {
		if o.QuotaSourceID != "" {
			fmt.Fprintf(h, "Q|%s|%s|%s|%s\n", o.Number, o.QuotaArrangementID, o.QuotaSourceID, o.VendorID)
		}
	}
	return fmt.Sprintf("%x", h.Sum(nil))
}

type runQuotaState struct {
	RunQuota
	allocated map[string]decimal.Decimal
	usage     map[string]map[int]decimal.Decimal // source id -> period index -> quantity
}

type quotaShare struct {
	qty                 decimal.Decimal
	arrangement, source string
	vendor              string
}

func (s quotaShare) stamp(o PlannedOrder) PlannedOrder {
	o.QuotaArrangementID, o.QuotaSourceID, o.VendorID = s.arrangement, s.source, s.vendor
	return o
}

// period is the index of the source's period that holds day.
func (st *runQuotaState) period(src QuotaSourceSpec, day time.Time) int {
	if src.PeriodDays == nil {
		return 0
	}
	return st.periodOf(*src.PeriodDays, day)
}

func (st *runQuotaState) periodOf(periodDays int, day time.Time) int {
	return int(day.Sub(st.Spec.ValidFrom)/(24*time.Hour)) / periodDays
}

func (st *runQuotaState) use(sourceID string, period int, qty decimal.Decimal) {
	if st.usage[sourceID] == nil {
		st.usage[sourceID] = map[int]decimal.Decimal{}
	}
	st.usage[sourceID][period] = st.usage[sourceID][period].Add(qty)
}

func (st *runQuotaState) remaining(src QuotaSourceSpec, day time.Time) (decimal.Decimal, bool) {
	if src.MaxQtyPerPeriod == nil {
		return decimal.Zero, false
	}
	return src.MaxQtyPerPeriod.Sub(st.usage[src.ID][st.period(src, day)]), true
}

// allocate gives one order to the arrangement's sources; ok is false when no
// source can take it.
func (st *runQuotaState) allocate(o PlannedOrder, day time.Time) ([]quotaShare, bool, error) {
	byID := map[string]QuotaSourceSpec{}
	out := map[string]bool{} // sources this order cannot use, or whose share is fixed
	for _, s := range st.Spec.Sources {
		byID[s.ID] = s
		if sourceOrderType[s.Type] != o.OrderType {
			out[s.ID] = true
		}
	}
	// Capped water filling: a share above a source's remainder in the period
	// is fixed at that remainder (dropped when below the minimum split) and the
	// rest of the order is divided again over the other sources. Raising the
	// level for the rest never lowers what a fixed source would take, so each
	// source is fixed at most once.
	var fixed []QuotaAssignment
	left := o.Qty
	for attempt := 0; attempt <= len(byID); attempt++ {
		var cands []QuotaSource
		for _, s := range st.Spec.Sources {
			if out[s.ID] {
				continue
			}
			if rem, capped := st.remaining(s, day); capped && (!rem.IsPositive() || (!st.Spec.Split && rem.LessThan(left))) {
				continue
			}
			c := s.Calculator()
			c.Allocated = st.allocated[s.ID]
			cands = append(cands, c)
		}
		if len(cands) == 0 {
			return nil, false, nil
		}
		as, err := Allocate(cands, left, st.Spec.Split, st.Spec.MinSplitQty)
		if errors.Is(err, ErrNoUsableSource) {
			return nil, false, nil
		}
		if err != nil {
			return nil, false, err
		}
		over := -1
		for i, a := range as {
			if rem, capped := st.remaining(byID[a.SourceID], day); capped && a.Qty.GreaterThan(rem) {
				over = i
				break
			}
		}
		if over < 0 {
			return st.book(append(fixed, as...), day), true, nil
		}
		id := as[over].SourceID
		out[id] = true
		if rem, _ := st.remaining(byID[id], day); !rem.LessThan(st.Spec.MinSplitQty) {
			fixed = append(fixed, QuotaAssignment{SourceID: id, Qty: rem})
			left = left.Sub(rem)
		}
	}
	return nil, false, nil
}

// book records the assignments of one order on the run's running allocation
// and period usage and returns them as shares, sorted by source id.
func (st *runQuotaState) book(as []QuotaAssignment, day time.Time) []quotaShare {
	slices.SortFunc(as, func(a, b QuotaAssignment) int { return compareStrings(a.SourceID, b.SourceID) })
	shares := make([]quotaShare, 0, len(as))
	for _, a := range as {
		var src QuotaSourceSpec
		for _, s := range st.Spec.Sources {
			if s.ID == a.SourceID {
				src = s
				break
			}
		}
		sh := quotaShare{qty: a.Qty, arrangement: st.ArrangementID, source: src.ID}
		if src.Type == SourceVendor {
			sh.vendor = src.VendorID
		}
		shares = append(shares, sh)
		st.allocated[src.ID] = st.allocated[src.ID].Add(a.Qty)
		if src.MaxQtyPerPeriod != nil {
			st.use(src.ID, st.period(src, day), a.Qty)
		}
	}
	return shares
}

// splitNumber is the number of part n (from 0) of a split order.
func splitNumber(number string, n int) string { return fmt.Sprintf("%s-%d", number, n+1) }

// dividePegs replaces the pegs of each split order by pegs of its parts: the
// parts take the order's pegs in order, each up to its own quantity, so the
// pegged total per part never exceeds its quantity and nothing is lost. A
// component peg to the bom-line demand of a split order ("<number>/<item>")
// is divided over the parts in proportion to their quantities and names the
// part's bom line, so an explanation still reaches the part that drives it.
func dividePegs(pegs []Peg, orders []PlannedOrder, assigned map[int][]quotaShare, parts map[string][]string) ([]Peg, error) {
	qtyOf := map[string]decimal.Decimal{}
	total := map[string]decimal.Decimal{}
	for i, o := range orders {
		if sh, ok := assigned[i]; ok && len(sh) > 1 {
			total[o.Number] = o.Qty
			for n, s := range sh {
				qtyOf[splitNumber(o.Number, n)] = s.qty
			}
		}
	}
	filled := map[string]decimal.Decimal{}
	var out []Peg
	for _, p := range pegs {
		var bySupply []Peg
		if names, split := parts[p.SupplyRef]; split {
			left := p.Qty
			for n, name := range names {
				room := qtyOf[name].Sub(filled[name])
				take := decimal.Min(left, room)
				if n == len(names)-1 {
					take = left // rounding cannot leave a peg unplaced
				}
				if !take.IsPositive() {
					continue
				}
				q := p
				q.SupplyRef, q.Qty = name, take
				bySupply = append(bySupply, q)
				filled[name] = filled[name].Add(take)
				left = left.Sub(take)
			}
		} else {
			bySupply = []Peg{p}
		}
		for _, q := range bySupply {
			if q.DemandRefType == RefBOM {
				driver := driverOf(q.DemandRef)
				if names, split := parts[driver]; split {
					out = append(out, divideBOMPeg(q, driver, names, qtyOf, total[driver])...)
					continue
				}
			}
			out = append(out, q)
		}
	}
	slices.SortFunc(out, func(a, b Peg) int {
		switch {
		case pegLess(a, b):
			return -1
		case pegLess(b, a):
			return 1
		}
		return 0
	})
	return out, nil
}

// divideBOMPeg splits a component peg to the bom-line demand of a split order
// over its parts by their share of the order; the last part takes the
// rounding remainder.
func divideBOMPeg(p Peg, driver string, names []string, qtyOf map[string]decimal.Decimal, total decimal.Decimal) []Peg {
	var out []Peg
	left := p.Qty
	for n, name := range names {
		take := left
		if n < len(names)-1 && total.IsPositive() {
			take = decimal.Min(left, p.Qty.Mul(qtyOf[name]).Div(total).Round(6))
		}
		if !take.IsPositive() {
			continue
		}
		q := p
		q.DemandRef, q.Qty = name+p.DemandRef[len(driver):], take
		out = append(out, q)
		left = left.Sub(take)
	}
	return out
}
