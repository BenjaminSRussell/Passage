// Package domain holds the pure sales rules: cumulative ATP and promising
// (BR-27.1), credit rules (BR-27.3), hold precedence and line arithmetic.
// Nothing here performs I/O.
package domain

import (
	"errors"
	"time"

	"github.com/shopspring/decimal"
)

// SourceATP marks a promise placed against cumulative ATP buckets.
const SourceATP = "ATP"

// ErrNoPeriods is returned when a promise is requested without any ATP bucket.
var ErrNoPeriods = errors.New("sales: no ATP periods")

// Period is one ATP bucket: the supply and demand that fall on Date. The first
// period carries on-hand stock as supply.
type Period struct {
	Date   time.Time       `json:"date"`
	Supply decimal.Decimal `json:"supply"`
	Demand decimal.Decimal `json:"demand"`
}

// ScheduleLine is one slice of a promise: the quantity placed on period Period.
type ScheduleLine struct {
	Date     time.Time       `json:"date"`
	Quantity decimal.Decimal `json:"quantity"`
	Period   int             `json:"period"`
}

// Promise is the outcome of placing a requested quantity against the periods.
type Promise struct {
	Requested    decimal.Decimal `json:"requested"`
	Complete     bool            `json:"complete"`
	PromisedDate time.Time       `json:"promisedDate"`
	Source       string          `json:"source"`
	Schedule     []ScheduleLine  `json:"schedule"`
	Backorder    decimal.Decimal `json:"backorder"`
}

// net returns the period ATP (supply minus demand) of every bucket.
func net(periods []Period) []decimal.Decimal {
	out := make([]decimal.Decimal, len(periods))
	for i, p := range periods {
		out[i] = p.Supply.Sub(p.Demand)
	}
	return out
}

// cumulative computes cum[n] = min over k >= n of the running sum of net[0..k].
// Negative periods therefore propagate backwards (BR-27.1).
func cumulative(net []decimal.Decimal) []decimal.Decimal {
	n := len(net)
	prefix := make([]decimal.Decimal, n)
	run := decimal.Zero
	for i, v := range net {
		run = run.Add(v)
		prefix[i] = run
	}
	cum := make([]decimal.Decimal, n)
	low := decimal.Zero
	for i := n - 1; i >= 0; i-- {
		if i == n-1 || prefix[i].LessThan(low) {
			low = prefix[i]
		}
		cum[i] = low
	}
	return cum
}

// Cumulative returns the cumulative ATP of each period (BR-27.1).
func Cumulative(periods []Period) []decimal.Decimal { return cumulative(net(periods)) }

// laterOf returns the later of two instants.
func laterOf(a, b time.Time) time.Time {
	if a.After(b) {
		return a
	}
	return b
}

// PromiseDate places qty against the periods. A bucket runs from its date up to
// the next bucket's date, so the search starts at the bucket that contains the
// requested date (the first bucket when the request precedes every bucket), and
// no line is ever promised before the requested date. With completeOnly the
// whole quantity goes on the first bucket whose cumulative ATP covers it, or is
// backordered in full; otherwise quantity is placed greedily over the buckets
// and the remainder is a backorder. Each placement reduces every later
// cumulative value, so a promise never drives any cumulative ATP negative.
func PromiseDate(periods []Period, requested time.Time, qty decimal.Decimal, completeOnly bool) (Promise, error) {
	if len(periods) == 0 {
		return Promise{}, ErrNoPeriods
	}
	if !qty.IsPositive() {
		return Promise{}, errors.New("sales: promised quantity must be positive")
	}
	p := Promise{Requested: qty, Source: SourceATP, Backorder: decimal.Zero, Schedule: []ScheduleLine{}}
	start := 0
	for i, per := range periods {
		if per.Date.After(requested) {
			break
		}
		start = i
	}
	work := net(periods)
	if completeOnly {
		cum := cumulative(work)
		for i := start; i < len(periods); i++ {
			if cum[i].GreaterThanOrEqual(qty) {
				date := laterOf(periods[i].Date, requested)
				p.Complete = true
				p.PromisedDate = date
				p.Schedule = []ScheduleLine{{Date: date, Quantity: qty, Period: i}}
				return p, nil
			}
		}
		p.Backorder = qty
		return p, nil
	}
	remaining := qty
	for i := start; i < len(periods) && remaining.IsPositive(); i++ {
		avail := cumulative(work)[i]
		if !avail.IsPositive() {
			continue
		}
		take := decimal.Min(remaining, avail)
		work[i] = work[i].Sub(take)
		remaining = remaining.Sub(take)
		p.Schedule = append(p.Schedule, ScheduleLine{Date: laterOf(periods[i].Date, requested), Quantity: take, Period: i})
	}
	p.Backorder = remaining
	p.Complete = remaining.IsZero()
	if n := len(p.Schedule); n > 0 {
		p.PromisedDate = p.Schedule[n-1].Date
	}
	return p, nil
}
