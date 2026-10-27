package domain

import (
	"fmt"
	"time"

	"github.com/shopspring/decimal"
)

// Bucket is one inclusive range of calendar days since receipt (BR-20.15).
// ToDays < 0 marks the open-ended last bucket.
type Bucket struct {
	Label    string
	FromDays int
	ToDays   int
}

// DefaultBuckets are the default aging buckets: 0–30, 31–90, 91–180, 181–365, > 365.
func DefaultBuckets() []Bucket {
	b, _ := BucketsFromBounds([]int{30, 90, 180, 365})
	return b
}

// BucketsFromBounds builds buckets from ascending upper bounds in days. Bounds
// [30, 90] give 0–30, 31–90 and > 90.
func BucketsFromBounds(bounds []int) ([]Bucket, error) {
	if len(bounds) == 0 {
		return nil, &Error{CodeInvalidTxn, "at least one aging bucket bound is required"}
	}
	out := make([]Bucket, 0, len(bounds)+1)
	from := 0
	for _, b := range bounds {
		if b < from {
			return nil, &Error{CodeInvalidTxn, fmt.Sprintf("aging bucket bound %d must be non-negative and strictly ascending", b)}
		}
		out = append(out, Bucket{Label: fmt.Sprintf("%d-%d", from, b), FromDays: from, ToDays: b})
		from = b + 1
	}
	out = append(out, Bucket{Label: fmt.Sprintf("> %d", bounds[len(bounds)-1]), FromDays: from, ToDays: -1})
	return out, nil
}

// BucketTotal is the quantity and value of the layers that fall in one bucket.
type BucketTotal struct {
	Label string
	Qty   decimal.Decimal
	Value decimal.Decimal
}

// AgingReport is the aging of remaining cost layers as of one date (FR-20.17).
type AgingReport struct {
	Buckets    []BucketTotal
	TotalQty   decimal.Decimal
	TotalValue decimal.Decimal
}

// Aging buckets remaining layers by the calendar days between each layer's
// receipt date and asOf. A layer received after asOf is refused, because it
// cannot be part of that day's stock.
func Aging(layers []Layer, asOf time.Time, buckets []Bucket) (AgingReport, error) {
	if len(buckets) == 0 {
		return AgingReport{}, &Error{CodeInvalidTxn, "aging needs at least one bucket"}
	}
	rep := AgingReport{TotalQty: decimal.Zero, TotalValue: decimal.Zero}
	for _, b := range buckets {
		rep.Buckets = append(rep.Buckets, BucketTotal{Label: b.Label, Qty: decimal.Zero, Value: decimal.Zero})
	}
	day := dateOnly(asOf)
	for _, l := range layers {
		if dateOnly(l.ReceivedOn).After(day) {
			return AgingReport{}, &Error{CodeInvalidTxn, "layer " + l.ReceiptID + " was received after the as-of date"}
		}
		days := int(day.Sub(dateOnly(l.ReceivedOn)).Hours() / 24)
		idx := bucketIndex(buckets, days)
		rep.Buckets[idx].Qty = rep.Buckets[idx].Qty.Add(l.Qty)
		rep.Buckets[idx].Value = rep.Buckets[idx].Value.Add(l.Value)
		rep.TotalQty = rep.TotalQty.Add(l.Qty)
		rep.TotalValue = rep.TotalValue.Add(l.Value)
	}
	return rep, nil
}

func bucketIndex(buckets []Bucket, days int) int {
	for i, b := range buckets {
		if days >= b.FromDays && (b.ToDays < 0 || days <= b.ToDays) {
			return i
		}
	}
	// Buckets that do not cover the age (a gap in custom bounds) fall into the last bucket.
	return len(buckets) - 1
}

func dateOnly(t time.Time) time.Time {
	y, m, d := t.Date()
	return time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
}

// AgingLayers returns the remaining layers to age for one settlement run,
// with the run's Result. The layer methods age their own layers. For the
// average methods and STANDARD the layers are FIFO layers, which give the age,
// repriced to the average so that their total equals the value report (the
// FR-20.17 approximation). The last layer absorbs the rounding residual.
func AgingLayers(txns []Txn, opt Options) ([]Layer, Result, error) {
	res, err := Settle(txns, opt)
	if err != nil {
		return nil, Result{}, err
	}
	if opt.Method == FIFO || opt.Method == LIFO || opt.Method == LIFODate {
		return res.Layers, res, nil
	}
	fifoOpt := opt
	fifoOpt.Method = FIFO
	fifo, err := Settle(txns, fifoOpt)
	if err != nil {
		return nil, Result{}, err
	}
	if !fifo.RemainingQty.Equal(res.RemainingQty) {
		return nil, Result{}, &Error{CodeInvalidTxn, "FIFO layers do not match the remaining quantity"}
	}
	if len(fifo.Layers) == 0 {
		return nil, res, nil
	}
	unit := res.RemainingValue.Div(res.RemainingQty)
	out := make([]Layer, len(fifo.Layers))
	allocated := decimal.Zero
	for i, l := range fifo.Layers {
		out[i] = l
		if i == len(fifo.Layers)-1 {
			out[i].Value = res.RemainingValue.Sub(allocated)
			break
		}
		out[i].Value = l.Qty.Mul(unit).RoundBank(opt.Scale)
		allocated = allocated.Add(out[i].Value)
	}
	return out, res, nil
}

// TypeAging is the aging of one valuation type's remaining layers.
type TypeAging struct {
	ValuationType  string
	Report         AgingReport
	RemainingValue decimal.Decimal
}

// AgingByValuationType ages a product whose stock may be split by valuation
// type (FR-20.16, FR-20.17). Each type is settled and repriced on its own, so
// the average-method approximation uses that type's average; total is the
// aging of all types together and remaining is the summed remaining value it
// must tie to. perType is empty for a product that is not split.
func AgingByValuationType(txns []Txn, opt Options, defaultType string, asOf time.Time, buckets []Bucket) (total AgingReport, perType []TypeAging, remaining decimal.Decimal, err error) {
	types, parts, err := PartitionByValuationType(txns, defaultType)
	if err != nil {
		return AgingReport{}, nil, decimal.Zero, err
	}
	var all []Layer
	remaining = decimal.Zero
	for _, vt := range types {
		layers, res, err := AgingLayers(parts[vt], opt)
		if err != nil {
			return AgingReport{}, nil, decimal.Zero, err
		}
		all = append(all, layers...)
		remaining = remaining.Add(res.RemainingValue)
		if vt == "" {
			continue
		}
		rep, err := Aging(layers, asOf, buckets)
		if err != nil {
			return AgingReport{}, nil, decimal.Zero, err
		}
		perType = append(perType, TypeAging{ValuationType: vt, Report: rep, RemainingValue: res.RemainingValue})
	}
	total, err = Aging(all, asOf, buckets)
	if err != nil {
		return AgingReport{}, nil, decimal.Zero, err
	}
	return total, perType, remaining, nil
}
