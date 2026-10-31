package domain

import (
	"testing"
	"time"

	"github.com/shopspring/decimal"
	"pgregory.net/rapid"
)

// step is the finest quantity the property draws (3 decimals).
var step = decimal.New(1, -3)

// runningBalances returns, for every bucket, on-hand plus receipts up to it
// minus existing demand up to it minus extra placed up to it. It is a plain
// forward sum, independent of cumulative().
func runningBalances(periods []Period, extra []decimal.Decimal) []decimal.Decimal {
	out := make([]decimal.Decimal, len(periods))
	run := decimal.Zero
	for i, p := range periods {
		run = run.Add(p.Supply).Sub(p.Demand).Sub(extra[i])
		out[i] = run
	}
	return out
}

// fits reports whether x more on bucket j keeps every running balance from j on
// at or above zero, by simulation.
func fits(periods []Period, extra []decimal.Decimal, j int, x decimal.Decimal) bool {
	trial := append([]decimal.Decimal(nil), extra...)
	trial[j] = trial[j].Add(x)
	for _, b := range runningBalances(periods, trial)[j:] {
		if b.IsNegative() {
			return false
		}
	}
	return true
}

// genProjection draws a projection with uneven bucket gaps, on-hand stock,
// receipts and demand of up to 3 decimals. It is not forced feasible: a
// projection is often over-committed (a receipt slipped after orders were
// confirmed against it), which is when over-promising would hurt most.
func genProjection(t *rapid.T) []Period {
	n := rapid.IntRange(1, 9).Draw(t, "periods")
	periods := make([]Period, n)
	date := fixtureDay
	for i := range periods {
		if i > 0 {
			date = date.AddDate(0, 0, rapid.IntRange(1, 6).Draw(t, "gap"))
		}
		supply := decimal.Zero
		if i == 0 || rapid.Bool().Draw(t, "receipt") {
			supply = decimal.New(rapid.Int64Range(0, 150_000).Draw(t, "supply"), -3)
		}
		demand := decimal.New(rapid.Int64Range(0, 90_000).Draw(t, "demand"), -3)
		periods[i] = Period{Date: date, Supply: supply, Demand: demand}
	}
	return periods
}

// TestPromise_NeverOverPromisesOnHandOrReceipts is the AC-27.2 property over
// arbitrary (also over-committed) projections, fractional quantities and
// requests before, inside, between and after the buckets:
//   - nothing is lost: promised + backorder = requested;
//   - never over-promised: by every bucket date at or after the first line,
//     existing demand plus everything promised so far is covered by on-hand
//     plus receipts up to that date;
//   - never early: each line is dated max(bucket date, requested), on a bucket
//     from the one containing the request on, in bucket order;
//   - never under-promised: a complete-only promise uses the first bucket that
//     can take the whole quantity (or none can), and a partial backorder means
//     not one more unit-step fits on any bucket from the request on.
//
// Feasibility is simulated with plain running sums, not with cumulative().
func TestPromise_NeverOverPromisesOnHandOrReceipts(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		periods := genProjection(t)
		n := len(periods)
		last := periods[n-1].Date
		span := int(last.Sub(fixtureDay).Hours()/24) + 6
		requested := fixtureDay.AddDate(0, 0, rapid.IntRange(-3, span).Draw(t, "requested"))
		qty := decimal.New(rapid.Int64Range(1, 300_000).Draw(t, "qty"), -3)
		completeOnly := rapid.Bool().Draw(t, "completeOnly")

		p, err := PromiseDate(periods, requested, qty, completeOnly)
		if err != nil {
			t.Fatal(err)
		}
		start := 0
		for i, per := range periods {
			if !per.Date.After(requested) {
				start = i
			}
		}
		extra := make([]decimal.Decimal, n)
		for i := range extra {
			extra[i] = decimal.Zero
		}
		placed := decimal.Zero
		prev := -1
		for _, s := range p.Schedule {
			if s.Period < start || s.Period <= prev || s.Period >= n {
				t.Fatalf("line on bucket %d (start %d, previous %d)", s.Period, start, prev)
			}
			prev = s.Period
			want := periods[s.Period].Date
			if requested.After(want) {
				want = requested
			}
			if !s.Date.Equal(want) || s.Date.Before(requested) {
				t.Fatalf("line dated %s on bucket %s, requested %s", s.Date, periods[s.Period].Date, requested)
			}
			if !s.Quantity.IsPositive() {
				t.Fatalf("empty schedule line %+v", s)
			}
			extra[s.Period] = extra[s.Period].Add(s.Quantity)
			placed = placed.Add(s.Quantity)
		}
		if !placed.Add(p.Backorder).Equal(qty) || p.Backorder.IsNegative() {
			t.Fatalf("placed %s + backorder %s != requested %s", placed, p.Backorder, qty)
		}
		if p.Complete != p.Backorder.IsZero() {
			t.Fatalf("complete=%v with backorder %s", p.Complete, p.Backorder)
		}
		if len(p.Schedule) > 0 {
			bal := runningBalances(periods, extra)
			for m := p.Schedule[0].Period; m < n; m++ {
				if bal[m].IsNegative() {
					t.Fatalf("over-promised: balance on %s is %s after promising %v", periods[m].Date.Format(time.DateOnly), bal[m], p.Schedule)
				}
			}
			if !p.PromisedDate.Equal(p.Schedule[len(p.Schedule)-1].Date) {
				t.Fatalf("promised %s, last line %s", p.PromisedDate, p.Schedule[len(p.Schedule)-1].Date)
			}
		} else if !p.PromisedDate.IsZero() {
			t.Fatalf("promised date %s without a schedule line", p.PromisedDate)
		}

		none := make([]decimal.Decimal, n)
		for i := range none {
			none[i] = decimal.Zero
		}
		if completeOnly {
			if len(p.Schedule) > 1 {
				t.Fatalf("complete-only promise on %d buckets", len(p.Schedule))
			}
			end := n
			if len(p.Schedule) == 1 {
				end = p.Schedule[0].Period
			}
			for j := start; j < end; j++ {
				if fits(periods, none, j, qty) {
					t.Fatalf("bucket %d could take all %s but the promise is %+v", j, qty, p)
				}
			}
			return
		}
		if p.Backorder.IsPositive() {
			for j := start; j < n; j++ {
				if fits(periods, extra, j, step) {
					t.Fatalf("under-promised: bucket %d still takes %s with %s backordered", j, step, p.Backorder)
				}
			}
		}
	})
}
