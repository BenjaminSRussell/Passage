package domain

import (
	"slices"
	"sort"
	"time"

	"github.com/shopspring/decimal"
)

// Kind is the direction of a value-bearing inventory transaction.
type Kind string

const (
	Receipt Kind = "RECEIPT"
	Issue   Kind = "ISSUE"
)

// Fallback values an unsettled issue at a cost when stock is short (BR-20.3).
type Fallback string

const (
	FallbackRunningAvg  Fallback = "RUNNING_AVG"
	FallbackStandard    Fallback = "STANDARD"
	FallbackLastReceipt Fallback = "LAST_RECEIPT"
)

// Txn is one receipt or issue of a product at a site, in quantity and account
// currency. Value is used for receipts only; issues are costed by the engine.
type Txn struct {
	ID            string
	Kind          Kind
	Qty           decimal.Decimal
	Value         decimal.Decimal
	FinancialDate time.Time
	PhysicalDate  time.Time
	// ValuationType is the split/batch valuation type (FR-20.16); empty when the
	// product is not split valued.
	ValuationType string
	// MarkedReceiptID, set on an issue, names the receipt the issue settles
	// against first (FR-20.8), under every method except STANDARD.
	MarkedReceiptID string
}

// Options configures one settlement run.
type Options struct {
	Method Method
	// Scale is the minor unit of the account currency; values are rounded
	// half-even to it per settlement row (BR-20.4).
	Scale int32
	// Standard is the standard unit cost; required for STANDARD and for the
	// STANDARD fallback.
	Standard decimal.Decimal
	// Fallback defaults to RUNNING_AVG.
	Fallback Fallback
}

// Settlement links an issue to the receipt layer or average pool it consumed.
type Settlement struct {
	Seq       int
	IssueID   string
	ReceiptID string // empty when settled against an average pool
	Pool      string // AVG:RUNNING, AVG:<YYYY-MM>, STANDARD; empty for receipt layers
	Qty       decimal.Decimal
	Value     decimal.Decimal
}

// Exception is the part of an issue that no receipt in the run covered. It is
// valued at the fallback unit cost captured when the issue was costed and is
// settled by a later receipt at the next close (BR-20.3).
type Exception struct {
	IssueID  string
	Qty      decimal.Decimal
	Value    decimal.Decimal
	Fallback Fallback
}

// Layer is a receipt with quantity still on hand after settlement.
type Layer struct {
	ReceiptID  string
	ReceivedOn time.Time
	Qty        decimal.Decimal
	Value      decimal.Decimal
}

// Result is the outcome of one settlement run.
type Result struct {
	Settlements []Settlement
	IssueCosts  map[string]decimal.Decimal
	Exceptions  []Exception
	// RemainingQty and RemainingValue are the stock left in receipts after
	// settlement. They are zero while exceptions remain; physical on-hand is
	// then the negative of the exception quantity.
	RemainingQty   decimal.Decimal
	RemainingValue decimal.Decimal
	// Layers lists the remaining receipt layers for the layer methods only.
	Layers []Layer
}

// Settle values the issues against the receipts under opt.Method. The result
// is a pure function of the multiset of inputs: transactions are ordered by
// (financial date, physical date, id) before any value is computed (BR-20.2).
func Settle(txns []Txn, opt Options) (Result, error) {
	if !opt.Method.Valid() {
		return Result{}, &Error{CodeInvalidTxn, "unknown costing method " + string(opt.Method)}
	}
	if opt.Fallback == "" {
		opt.Fallback = FallbackRunningAvg
	}
	switch opt.Fallback {
	case FallbackRunningAvg, FallbackLastReceipt:
	case FallbackStandard:
		if !opt.Standard.IsPositive() {
			return Result{}, &Error{CodeInvalidTxn, "STANDARD fallback needs a positive standard unit cost"}
		}
	default:
		return Result{}, &Error{CodeInvalidTxn, "unknown fallback " + string(opt.Fallback)}
	}
	if opt.Method == Standard && !opt.Standard.IsPositive() {
		return Result{}, &Error{CodeInvalidTxn, "STANDARD needs a positive standard unit cost"}
	}
	seq, err := orderTxns(txns)
	if err != nil {
		return Result{}, err
	}
	if err := validateMarks(seq); err != nil {
		return Result{}, err
	}
	e := &engine{opt: opt, res: Result{IssueCosts: map[string]decimal.Decimal{}}}
	if opt.Method != Standard {
		e.planMarks(seq)
	}
	switch opt.Method {
	case FIFO, LIFO, LIFODate:
		e.layered(seq)
	case WeightedAvgPeriodic:
		e.periodic(seq)
	default:
		e.running(seq)
	}
	return e.res, nil
}

// validateMarks checks that every mark is on an issue and names a receipt of
// the same run that is not dated after the issue.
func validateMarks(seq []Txn) error {
	receipts := map[string]Txn{}
	for _, t := range seq {
		if t.Kind == Receipt {
			receipts[t.ID] = t
		}
	}
	for _, t := range seq {
		if t.MarkedReceiptID == "" {
			continue
		}
		if t.Kind != Issue {
			return &Error{CodeInvalidTxn, "receipt " + t.ID + " cannot mark another receipt; only an issue carries a mark"}
		}
		r, ok := receipts[t.MarkedReceiptID]
		if !ok {
			return &Error{CodeInvalidTxn, "issue " + t.ID + " marks " + t.MarkedReceiptID + ", which is not a receipt of this run"}
		}
		if r.FinancialDate.After(t.FinancialDate) {
			return &Error{CodeInvalidTxn, "issue " + t.ID + " marks receipt " + r.ID + " dated after the issue"}
		}
	}
	return nil
}

func orderTxns(txns []Txn) ([]Txn, error) {
	seen := make(map[string]bool, len(txns))
	for _, t := range txns {
		if t.ID == "" || t.FinancialDate.IsZero() || (t.Kind != Receipt && t.Kind != Issue) || !t.Qty.IsPositive() {
			return nil, &Error{CodeInvalidTxn, "transaction needs an id, a financial date, a kind and a positive quantity"}
		}
		if t.Kind == Receipt && t.Value.IsNegative() {
			return nil, &Error{CodeInvalidTxn, "receipt " + t.ID + " has a negative value"}
		}
		if seen[t.ID] {
			return nil, &Error{CodeDuplicateTxn, "transaction " + t.ID + " appears twice"}
		}
		seen[t.ID] = true
	}
	out := slices.Clone(txns)
	sort.Slice(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if !a.FinancialDate.Equal(b.FinancialDate) {
			return a.FinancialDate.Before(b.FinancialDate)
		}
		if !a.PhysicalDate.Equal(b.PhysicalDate) {
			return a.PhysicalDate.Before(b.PhysicalDate)
		}
		return a.ID < b.ID
	})
	return out, nil
}

// periodKey is the fiscal-month bucket used by LIFO pooling and periodic averages.
func periodKey(t Txn) string { return t.FinancialDate.Format("2006-01") }

// shortage is the part of an issue that stock did not cover when it was
// costed, with the BR-20.3 fallback unit cost captured at that moment.
type shortage struct {
	issue Txn
	qty   decimal.Decimal
	unit  decimal.Decimal
}

type engine struct {
	opt Options
	res Result
	// backlog holds shortages in issue order. A later receipt in the same run
	// settles them first (BR-20.3); what is left at the end is an exception.
	backlog  []*shortage
	lastUnit decimal.Decimal
	// Marking (FR-20.8): marks maps a marked issue to the quantity it reserves
	// on its receipt, reserveQty is the total reserved per receipt, reserved
	// holds the part carved from a receipt once it enters the engine, and
	// waiting holds marked issues sequenced before their (same-day) receipt.
	marks      map[string]mark
	reserveQty map[string]decimal.Decimal
	reserved   map[string]*reservation
	waiting    map[string][]Txn
}

// mark is the part of a marked issue that its receipt covers.
type mark struct {
	receiptID string
	qty       decimal.Decimal
}

// reservation is the part of a receipt held for the issues that mark it.
type reservation struct {
	qty, value, unit decimal.Decimal
}

// planMarks reserves, in settlement order, each marked issue's quantity on its
// receipt, up to what the receipt holds. Marked pairs settle before any
// unmarked issue or earlier shortage can consume the receipt, whatever the
// method (FR-20.8); the part of a marked issue the receipt cannot cover is
// costed by the method like any other issue.
func (e *engine) planMarks(seq []Txn) {
	receiptQty := map[string]decimal.Decimal{}
	for _, t := range seq {
		if t.Kind == Receipt {
			receiptQty[t.ID] = t.Qty
		}
	}
	e.marks, e.reserveQty = map[string]mark{}, map[string]decimal.Decimal{}
	e.reserved, e.waiting = map[string]*reservation{}, map[string][]Txn{}
	for _, t := range seq {
		if t.Kind != Issue || t.MarkedReceiptID == "" {
			continue
		}
		free := receiptQty[t.MarkedReceiptID].Sub(e.reserveQty[t.MarkedReceiptID])
		if take := decimal.Min(t.Qty, free); take.IsPositive() {
			e.marks[t.ID] = mark{receiptID: t.MarkedReceiptID, qty: take}
			e.reserveQty[t.MarkedReceiptID] = e.reserveQty[t.MarkedReceiptID].Add(take)
		}
	}
}

// carve holds back the reserved part of a receipt as it enters the engine,
// settles any marked issue already waiting for it, and returns the free
// quantity and value left for the method.
func (e *engine) carve(t Txn) (decimal.Decimal, decimal.Decimal) {
	rq := e.reserveQty[t.ID]
	if !rq.IsPositive() {
		return t.Qty, t.Value
	}
	unit := t.Value.Div(t.Qty)
	rv := t.Value
	if !rq.Equal(t.Qty) {
		rv = e.round(rq.Mul(unit))
	}
	e.reserved[t.ID] = &reservation{qty: rq, value: rv, unit: unit}
	for _, issue := range e.waiting[t.ID] {
		e.takeReserved(issue, e.marks[issue.ID])
	}
	delete(e.waiting, t.ID)
	return t.Qty.Sub(rq), t.Value.Sub(rv)
}

// marked settles the marked part of an issue against its receipt's
// reservation, or queues it when the receipt has not entered the engine yet,
// and returns the quantity the method still has to cost.
func (e *engine) marked(t Txn) decimal.Decimal {
	m, ok := e.marks[t.ID]
	if !ok {
		return t.Qty
	}
	if _, in := e.reserved[m.receiptID]; in {
		e.takeReserved(t, m)
	} else {
		e.waiting[m.receiptID] = append(e.waiting[m.receiptID], t)
	}
	return t.Qty.Sub(m.qty)
}

// takeReserved settles one marked issue against its receipt's reservation. The
// last issue to take from a reservation carries its remaining value, so the
// settlements of a receipt still sum to its value (BR-20.4).
func (e *engine) takeReserved(issue Txn, m mark) {
	r := e.reserved[m.receiptID]
	value := r.value
	if !m.qty.Equal(r.qty) {
		value = e.round(m.qty.Mul(r.unit))
	}
	r.qty, r.value = r.qty.Sub(m.qty), r.value.Sub(value)
	e.settle(issue, m.receiptID, "", m.qty, value)
}

func (e *engine) round(d decimal.Decimal) decimal.Decimal { return d.RoundBank(e.opt.Scale) }

func (e *engine) settle(issue Txn, receiptID, pool string, qty, value decimal.Decimal) {
	e.res.Settlements = append(e.res.Settlements, Settlement{Seq: len(e.res.Settlements) + 1, IssueID: issue.ID,
		ReceiptID: receiptID, Pool: pool, Qty: qty, Value: value})
	e.res.IssueCosts[issue.ID] = e.res.IssueCosts[issue.ID].Add(value)
}

// fallbackUnit is the BR-20.3 unit cost for quantity that stock does not
// cover: the running average of the stock on hand before the issue, then the
// standard cost, then the last receipt cost.
func (e *engine) fallbackUnit(onHandQty, onHandValue decimal.Decimal) decimal.Decimal {
	switch e.opt.Fallback {
	case FallbackStandard:
		return e.opt.Standard
	case FallbackRunningAvg:
		if onHandQty.IsPositive() {
			return onHandValue.Div(onHandQty)
		}
		if e.opt.Standard.IsPositive() {
			return e.opt.Standard
		}
	}
	return e.lastUnit
}

// short records the uncovered quantity of an issue on the backlog.
func (e *engine) short(issue Txn, qty, unit decimal.Decimal) {
	if qty.IsPositive() {
		e.backlog = append(e.backlog, &shortage{issue: issue, qty: qty, unit: unit})
	}
}

// absorb settles the backlog, oldest shortage first, against newly available
// stock of qty and value, and returns the stock left over. The last row taken
// from the stock carries its remaining value, so nothing is lost to rounding.
func (e *engine) absorb(receiptID, pool string, qty, value decimal.Decimal) (decimal.Decimal, decimal.Decimal) {
	if len(e.backlog) == 0 || !qty.IsPositive() {
		return qty, value
	}
	unit := value.Div(qty)
	for len(e.backlog) > 0 && qty.IsPositive() {
		s := e.backlog[0]
		take := decimal.Min(s.qty, qty)
		v := value
		if !take.Equal(qty) {
			v = e.round(take.Mul(unit))
		}
		e.settle(s.issue, receiptID, pool, take, v)
		s.qty, qty, value = s.qty.Sub(take), qty.Sub(take), value.Sub(v)
		if !s.qty.IsPositive() {
			e.backlog = e.backlog[1:]
		}
	}
	return qty, value
}

// finish turns the shortages no receipt covered into exceptions valued at
// their fallback unit cost.
func (e *engine) finish() {
	for _, s := range e.backlog {
		value := e.round(s.qty.Mul(s.unit))
		e.res.Exceptions = append(e.res.Exceptions, Exception{IssueID: s.issue.ID, Qty: s.qty, Value: value, Fallback: e.opt.Fallback})
		e.res.IssueCosts[s.issue.ID] = e.res.IssueCosts[s.issue.ID].Add(value)
	}
}

// layer is one receipt; its unit cost is value/qty. The last unit consumed of
// a layer takes the remaining value, so the settlements of a receipt sum to its
// value exactly (BR-20.4).
type layer struct {
	txn   Txn
	qty   decimal.Decimal
	value decimal.Decimal
	unit  decimal.Decimal
}

// layered settles FIFO, LIFO_DATE and LIFO over receipt layers in one pass.
// FIFO consumes the oldest open layer, LIFO_DATE the newest layer received
// before the issue. LIFO opens every receipt of a period when the period
// starts, so an issue takes the newest receipt of its period (even one dated
// after it), then earlier periods newest first.
func (e *engine) layered(seq []Txn) {
	var all, open []*layer
	stockQty, stockVal := decimal.Zero, decimal.Zero
	push := func(t Txn) {
		l := &layer{txn: t, unit: t.Value.Div(t.Qty)}
		all = append(all, l)
		l.qty, l.value = e.carve(t)
		l.qty, l.value = e.absorb(t.ID, "", l.qty, l.value)
		if l.qty.IsPositive() {
			open = append(open, l)
			stockQty, stockVal = stockQty.Add(l.qty), stockVal.Add(l.value)
		}
	}
	period := ""
	for i, t := range seq {
		if e.opt.Method == LIFO {
			if k := periodKey(t); k != period {
				period = k
				for _, r := range seq[i:] {
					if periodKey(r) != k {
						break
					}
					if r.Kind == Receipt {
						push(r)
					}
				}
			}
		} else if t.Kind == Receipt {
			push(t)
		}
		if t.Kind == Receipt {
			e.lastUnit = t.Value.Div(t.Qty)
			continue
		}
		unit := e.fallbackUnit(stockQty, stockVal)
		need := e.marked(t)
		for need.IsPositive() && len(open) > 0 {
			idx := len(open) - 1
			if e.opt.Method == FIFO {
				idx = 0
			}
			l := open[idx]
			take := decimal.Min(need, l.qty)
			value := l.value
			if !take.Equal(l.qty) {
				value = e.round(take.Mul(l.unit))
			}
			l.qty, l.value = l.qty.Sub(take), l.value.Sub(value)
			stockQty, stockVal = stockQty.Sub(take), stockVal.Sub(value)
			e.settle(t, l.txn.ID, "", take, value)
			need = need.Sub(take)
			if !l.qty.IsPositive() {
				open = slices.Delete(open, idx, idx+1)
			}
		}
		e.short(t, need, unit)
	}
	e.finish()
	for _, l := range all {
		if l.qty.IsPositive() {
			e.res.RemainingQty = e.res.RemainingQty.Add(l.qty)
			e.res.RemainingValue = e.res.RemainingValue.Add(l.value)
			e.res.Layers = append(e.res.Layers, Layer{ReceiptID: l.txn.ID, ReceivedOn: l.txn.FinancialDate, Qty: l.qty, Value: l.value})
		}
	}
}

// running settles WEIGHTED_AVG_RUNNING and MOVING_AVG at the average on hand at
// the moment of each issue, and STANDARD at the fixed standard unit.
func (e *engine) running(seq []Txn) {
	fixed := e.opt.Method == Standard
	pool := "AVG:RUNNING"
	if fixed {
		pool = "STANDARD"
	}
	onQty, onVal := decimal.Zero, decimal.Zero
	for _, t := range seq {
		if t.Kind == Receipt {
			qty, value := t.Qty, t.Value
			if fixed {
				value = e.round(t.Qty.Mul(e.opt.Standard))
			}
			e.lastUnit = value.Div(t.Qty)
			if !fixed {
				qty, value = e.carve(t)
			}
			q, v := e.absorb(t.ID, pool, qty, value)
			onQty, onVal = onQty.Add(q), onVal.Add(v)
			continue
		}
		unit := e.fallbackUnit(onQty, onVal)
		need := e.marked(t)
		take := decimal.Min(need, onQty)
		if take.IsPositive() {
			var value decimal.Decimal
			switch {
			case take.Equal(onQty):
				value = onVal
			case fixed:
				value = e.round(take.Mul(e.opt.Standard))
			default:
				value = e.round(take.Mul(onVal.Div(onQty)))
			}
			onQty, onVal = onQty.Sub(take), onVal.Sub(value)
			e.settle(t, "", pool, take, value)
		}
		e.short(t, need.Sub(take), unit)
	}
	e.finish()
	e.res.RemainingQty, e.res.RemainingValue = onQty, onVal
}

// periodic settles WEIGHTED_AVG_PERIODIC. Each fiscal month has one average,
// (opening value + receipts value) / (opening qty + receipts qty), where the
// opening is what the previous month left on hand. Shortages carried from an
// earlier month settle first at the new month's average; issues in the month
// settle at that average wherever they fall in the month.
func (e *engine) periodic(seq []Txn) {
	curKey := ""
	availQty, availVal := decimal.Zero, decimal.Zero
	for i, t := range seq {
		if k := periodKey(t); k != curKey {
			curKey = k
			recQty, recVal := decimal.Zero, decimal.Zero
			for _, r := range seq[i:] {
				if periodKey(r) != k {
					break
				}
				if r.Kind == Receipt {
					q, v := e.carve(r)
					recQty, recVal = recQty.Add(q), recVal.Add(v)
				}
			}
			availQty, availVal = e.absorb("", "AVG:"+k, availQty.Add(recQty), availVal.Add(recVal))
		}
		if t.Kind == Receipt {
			e.lastUnit = t.Value.Div(t.Qty)
			continue
		}
		unit := e.fallbackUnit(availQty, availVal)
		need := e.marked(t)
		take := decimal.Min(need, availQty)
		if take.IsPositive() {
			value := availVal
			if !take.Equal(availQty) {
				value = e.round(take.Mul(availVal.Div(availQty)))
			}
			availQty, availVal = availQty.Sub(take), availVal.Sub(value)
			e.settle(t, "", "AVG:"+curKey, take, value)
		}
		e.short(t, need.Sub(take), unit)
	}
	e.finish()
	e.res.RemainingQty, e.res.RemainingValue = availQty, availVal
}
