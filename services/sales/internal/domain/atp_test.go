package domain

import (
	"testing"
	"time"

	"github.com/shopspring/decimal"
	"pgregory.net/rapid"
)

var fixtureDay = time.Date(2027, 3, 1, 0, 0, 0, 0, time.UTC)

func d(n int) decimal.Decimal { return decimal.NewFromInt(int64(n)) }

// brPeriods is the BR-27.1 fixture: on-hand 100 on 03-01, PO 50 on 03-04,
// PRD 80 on 03-08; demand SO-1001 60 on 03-02, SO-1002 40 on 03-05, SO-1003 70 on 03-09.
func brPeriods() []Period {
	return []Period{
		{Date: fixtureDay, Supply: d(100), Demand: d(60)},
		{Date: fixtureDay.AddDate(0, 0, 3), Supply: d(50), Demand: d(40)},
		{Date: fixtureDay.AddDate(0, 0, 7), Supply: d(80), Demand: d(70)},
	}
}

func eqDecs(t *testing.T, got, want []decimal.Decimal) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("len: got %d want %d", len(got), len(want))
	}
	for i := range got {
		if !got[i].Equal(want[i]) {
			t.Fatalf("index %d: got %s want %s (all %v)", i, got[i], want[i], got)
		}
	}
}

func TestCumulative_BR271Example(t *testing.T) {
	eqDecs(t, Cumulative(brPeriods()), []decimal.Decimal{d(40), d(50), d(60)})
}

func TestCumulative_NegativePeriodPropagatesBackwards(t *testing.T) {
	periods := brPeriods()
	// Confirming 45 on 03-04 leaves bucket 1 at -35 (10 - 45).
	periods[1].Demand = periods[1].Demand.Add(d(45))
	eqDecs(t, Cumulative(periods), []decimal.Decimal{d(5), d(5), d(15)})
}

func TestPromiseDate_TableDriven(t *testing.T) {
	day := func(n int) time.Time { return fixtureDay.AddDate(0, 0, n) }
	tests := []struct {
		name         string
		periods      []Period
		requested    time.Time
		qty          int
		completeOnly bool
		wantComplete bool
		wantDate     time.Time
		wantSource   string
		wantSchedule []ScheduleLine
		wantBackord  int
	}{
		{name: "BR-27.1 complete delivery of 45 moves to the first covering bucket", periods: brPeriods(), requested: fixtureDay, qty: 45, completeOnly: true, wantComplete: true, wantDate: day(3), wantSource: SourceATP, wantSchedule: []ScheduleLine{{Date: day(3), Quantity: d(45), Period: 1}}},
		{name: "BR-27.1 partial delivery splits 40 on 03-01 and 5 on 03-04", periods: brPeriods(), requested: fixtureDay, qty: 45, wantComplete: true, wantDate: day(3), wantSource: SourceATP, wantSchedule: []ScheduleLine{{Date: day(0), Quantity: d(40), Period: 0}, {Date: day(3), Quantity: d(5), Period: 1}}},
		{name: "complete delivery beyond total ATP is backordered in full with no date", periods: brPeriods(), requested: fixtureDay, qty: 61, completeOnly: true, wantComplete: false, wantSource: SourceATP, wantBackord: 61, wantSchedule: []ScheduleLine{}},
		{name: "partial delivery beyond total ATP backorders the excess", periods: brPeriods(), requested: fixtureDay, qty: 70, wantComplete: false, wantDate: day(7), wantSource: SourceATP, wantBackord: 10, wantSchedule: []ScheduleLine{{Date: day(0), Quantity: d(40), Period: 0}, {Date: day(3), Quantity: d(10), Period: 1}, {Date: day(7), Quantity: d(10), Period: 2}}},
		{name: "a request between buckets is served by the bucket that contains it", periods: brPeriods(), requested: day(1), qty: 30, completeOnly: true, wantComplete: true, wantDate: day(1), wantSource: SourceATP, wantSchedule: []ScheduleLine{{Date: day(1), Quantity: d(30), Period: 0}}},
		{name: "a request after the last bucket is never promised earlier than requested", periods: brPeriods(), requested: day(40), qty: 10, completeOnly: true, wantComplete: true, wantDate: day(40), wantSource: SourceATP, wantSchedule: []ScheduleLine{{Date: day(40), Quantity: d(10), Period: 2}}},
		{name: "partial schedule lines start on the requested date inside a bucket", periods: brPeriods(), requested: day(1), qty: 45, wantComplete: true, wantDate: day(3), wantSource: SourceATP, wantSchedule: []ScheduleLine{{Date: day(1), Quantity: d(40), Period: 0}, {Date: day(3), Quantity: d(5), Period: 1}}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			p, err := PromiseDate(tc.periods, tc.requested, d(tc.qty), tc.completeOnly)
			if err != nil {
				t.Fatal(err)
			}
			if p.Complete != tc.wantComplete || !p.PromisedDate.Equal(tc.wantDate) || p.Source != tc.wantSource {
				t.Fatalf("got complete=%v date=%s source=%s; want complete=%v date=%s source=%s",
					p.Complete, p.PromisedDate, p.Source, tc.wantComplete, tc.wantDate, tc.wantSource)
			}
			if !p.Backorder.Equal(d(tc.wantBackord)) {
				t.Fatalf("backorder %s want %d", p.Backorder, tc.wantBackord)
			}
			if tc.wantSchedule != nil {
				if len(p.Schedule) != len(tc.wantSchedule) {
					t.Fatalf("schedule %v want %v", p.Schedule, tc.wantSchedule)
				}
				for i, s := range tc.wantSchedule {
					if !p.Schedule[i].Date.Equal(s.Date) || !p.Schedule[i].Quantity.Equal(s.Quantity) || p.Schedule[i].Period != s.Period {
						t.Fatalf("schedule[%d] = %+v want %+v", i, p.Schedule[i], s)
					}
				}
			}
		})
	}
}

func TestPromiseDate_RejectsEmptyInput(t *testing.T) {
	if _, err := PromiseDate(nil, fixtureDay, d(1), true); err != ErrNoPeriods {
		t.Fatalf("got %v want ErrNoPeriods", err)
	}
	if _, err := PromiseDate(brPeriods(), fixtureDay, d(0), true); err == nil {
		t.Fatal("zero quantity must be rejected")
	}
}

// genFeasible draws periods whose running ATP never goes negative, which is the
// state a projection is in before a promise is applied.
func genFeasible(t *rapid.T, n int) []Period {
	periods := make([]Period, n)
	carry := decimal.Zero
	for i := range periods {
		supply := d(rapid.IntRange(0, 100).Draw(t, "supply"))
		demand := d(rapid.IntRange(0, 100).Draw(t, "demand"))
		if carry.Add(supply).LessThan(demand) {
			demand = carry.Add(supply)
		}
		carry = carry.Add(supply).Sub(demand)
		periods[i] = Period{Date: fixtureDay.AddDate(0, 0, i), Supply: supply, Demand: demand}
	}
	return periods
}

// TestPromise_NeverOverPromises is AC-27.2: after applying any promise no
// cumulative ATP is negative and the total promised is within supply less demand.
func TestPromise_NeverOverPromises(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		n := rapid.IntRange(1, 8).Draw(t, "periods")
		periods := genFeasible(t, n)
		qty := d(rapid.IntRange(1, 150).Draw(t, "qty"))
		completeOnly := rapid.Bool().Draw(t, "complete")
		requested := fixtureDay.AddDate(0, 0, rapid.IntRange(0, n).Draw(t, "offset"))

		p, err := PromiseDate(periods, requested, qty, completeOnly)
		if err != nil {
			t.Fatal(err)
		}
		allocated := decimal.Zero
		for _, s := range p.Schedule {
			allocated = allocated.Add(s.Quantity)
		}
		if !allocated.Add(p.Backorder).Equal(qty) {
			t.Fatalf("allocated %s + backorder %s != requested %s", allocated, p.Backorder, qty)
		}
		applied := net(periods)
		for _, s := range p.Schedule {
			applied[s.Period] = applied[s.Period].Sub(s.Quantity)
		}
		for i, c := range cumulative(applied) {
			if c.IsNegative() {
				t.Fatalf("cumulative ATP %d negative after promise: %s", i, c)
			}
		}
		totalNet := decimal.Zero
		for _, v := range net(periods) {
			totalNet = totalNet.Add(v)
		}
		if allocated.GreaterThan(totalNet) {
			t.Fatalf("promised %s exceeds available %s", allocated, totalNet)
		}
		for _, s := range p.Schedule {
			if s.Date.Before(requested) {
				t.Fatalf("schedule line %s before requested %s", s.Date, requested)
			}
		}
		if completeOnly && p.Complete && len(p.Schedule) != 1 {
			t.Fatalf("complete promise must use one bucket, got %d", len(p.Schedule))
		}
	})
}
