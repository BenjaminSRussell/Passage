package domain

import (
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/shopspring/decimal"
)

// Layout is a bucket layout of the cash forecast (FR-36.9).
type Layout string

const (
	LayoutDaily   Layout = "daily"   // 30 days
	LayoutWeekly  Layout = "weekly"  // 13 weeks
	LayoutMonthly Layout = "monthly" // 12 months
)

// Buckets returns the number of buckets a layout holds.
func (l Layout) Buckets() (int, error) {
	switch l {
	case LayoutDaily:
		return 30, nil
	case LayoutWeekly:
		return 13, nil
	case LayoutMonthly:
		return 12, nil
	}
	return 0, fmt.Errorf("unknown forecast layout %q", l)
}

// Contribution is one signed cash flow that a source expects on a date.
// Inflows are positive, outflows negative.
type Contribution struct {
	SourceType   string // AP, AR, PO, SO, Recurring, Manual, Payroll, Project
	SourceID     string
	ExpectedDate time.Time // UTC midnight
	Amount       decimal.Decimal
	Currency     string
}

// Bucket totals the contributions of one currency that fall in one period.
// Overdue contributions (before asOf) fall into the first bucket.
type Bucket struct {
	Key        string          `json:"key"` // first day of the bucket, YYYY-MM-DD (monthly: YYYY-MM-01)
	Currency   string          `json:"currency"`
	Inflow     decimal.Decimal `json:"inflow"`
	Outflow    decimal.Decimal `json:"outflow"` // negative
	Net        decimal.Decimal `json:"net"`
	Cumulative decimal.Decimal `json:"cumulative"` // running net per currency, from the first bucket
}

// Forecast is the bucketed view. OutOfHorizon counts contributions after the last bucket.
type Forecast struct {
	Layout       Layout   `json:"layout"`
	AsOf         string   `json:"asOf"`
	Buckets      []Bucket `json:"buckets"`
	OutOfHorizon int      `json:"outOfHorizon"`
}

// Day returns the UTC midnight of a YYYY-MM-DD date.
func Day(s string) (time.Time, error) { return time.Parse("2006-01-02", s) }

// Build computes the forecast. Every bucket's Inflow, Outflow and Net equal the
// sums of the contributions that map to it, so Σ buckets = Σ contributions
// inside the horizon, per currency (property-tested).
func Build(asOf time.Time, layout Layout, contributions []Contribution) (Forecast, error) {
	n, err := layout.Buckets()
	if err != nil {
		return Forecast{}, err
	}
	asOf = dayOf(asOf)
	type key struct {
		idx int
		cur string
	}
	acc := map[key]*Bucket{}
	out := Forecast{Layout: layout, AsOf: asOf.Format("2006-01-02")}
	for _, c := range contributions {
		if c.Currency == "" {
			return Forecast{}, errors.New("contribution currency is required")
		}
		idx, ok := bucketIndex(asOf, dayOf(c.ExpectedDate), layout)
		if !ok || idx >= n {
			out.OutOfHorizon++
			continue
		}
		k := key{idx, c.Currency}
		b := acc[k]
		if b == nil {
			b = &Bucket{Key: bucketKey(asOf, idx, layout), Currency: c.Currency, Inflow: decimal.Zero, Outflow: decimal.Zero, Net: decimal.Zero}
			acc[k] = b
		}
		if c.Amount.IsPositive() {
			b.Inflow = b.Inflow.Add(c.Amount)
		} else {
			b.Outflow = b.Outflow.Add(c.Amount)
		}
		b.Net = b.Net.Add(c.Amount)
	}
	keys := make([]key, 0, len(acc))
	for k := range acc {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i].idx != keys[j].idx {
			return keys[i].idx < keys[j].idx
		}
		return keys[i].cur < keys[j].cur
	})
	running := map[string]decimal.Decimal{}
	for _, k := range keys {
		b := *acc[k]
		running[k.cur] = running[k.cur].Add(b.Net)
		b.Cumulative = running[k.cur]
		out.Buckets = append(out.Buckets, b)
	}
	return out, nil
}

func dayOf(t time.Time) time.Time {
	y, m, d := t.UTC().Date()
	return time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
}

// bucketIndex maps a date to its bucket; dates before asOf map to bucket 0.
func bucketIndex(asOf, date time.Time, layout Layout) (int, bool) {
	switch layout {
	case LayoutDaily:
		return clampIdx(int(date.Sub(asOf) / (24 * time.Hour))), true
	case LayoutWeekly:
		return clampIdx(int(date.Sub(asOf)/(24*time.Hour)) / 7), true
	case LayoutMonthly:
		months := (date.Year()-asOf.Year())*12 + int(date.Month()-asOf.Month())
		return clampIdx(months), true
	}
	return 0, false
}

func clampIdx(i int) int {
	if i < 0 {
		return 0
	}
	return i
}

func bucketKey(asOf time.Time, idx int, layout Layout) string {
	switch layout {
	case LayoutDaily:
		return asOf.AddDate(0, 0, idx).Format("2006-01-02")
	case LayoutWeekly:
		return asOf.AddDate(0, 0, 7*idx).Format("2006-01-02")
	default:
		first := time.Date(asOf.Year(), asOf.Month()+time.Month(idx), 1, 0, 0, 0, 0, time.UTC)
		return first.Format("2006-01-02")
	}
}
