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
	e := &engine{opt: opt, res: Result{IssueCosts: map[string]decimal.Decimal{}}}
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
		l := &layer{txn: t, qty: t.Qty, value: t.Value, unit: t.Value.Div(t.Qty)}
		all = append(all, l)
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
		need := t.Qty
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
				if idx == 0 {
					open = open[1:]
				} else {
					open = open[:idx]
				}
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
			value := t.Value
			if fixed {
				value = e.round(t.Qty.Mul(e.opt.Standard))
			}
			e.lastUnit = value.Div(t.Qty)
			q, v := e.absorb(t.ID, pool, t.Qty, value)
			onQty, onVal = onQty.Add(q), onVal.Add(v)
			continue
		}
		unit := e.fallbackUnit(onQty, onVal)
		take := decimal.Min(t.Qty, onQty)
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
		e.short(t, t.Qty.Sub(take), unit)
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
	recQty, recVal := map[string]decimal.Decimal{}, map[string]decimal.Decimal{}
	for _, t := range seq {
		if t.Kind == Receipt {
			k := periodKey(t)
			recQty[k], recVal[k] = recQty[k].Add(t.Qty), recVal[k].Add(t.Value)
		}
	}
	curKey := ""
	availQty, availVal := decimal.Zero, decimal.Zero
	for _, t := range seq {
		if k := periodKey(t); k != curKey {
			curKey = k
			availQty, availVal = e.absorb("", "AVG:"+k, availQty.Add(recQty[k]), availVal.Add(recVal[k]))
		}
		if t.Kind == Receipt {
			e.lastUnit = t.Value.Div(t.Qty)
			continue
		}
		unit := e.fallbackUnit(availQty, availVal)
		take := decimal.Min(t.Qty, availQty)
		if take.IsPositive() {
			value := availVal
			if !take.Equal(availQty) {
				value = e.round(take.Mul(availVal.Div(availQty)))
			}
			availQty, availVal = availQty.Sub(take), availVal.Sub(value)
			e.settle(t, "", "AVG:"+curKey, take, value)
		}
		e.short(t, t.Qty.Sub(take), unit)
	}
	e.finish()
	e.res.RemainingQty, e.res.RemainingValue = availQty, availVal
}
