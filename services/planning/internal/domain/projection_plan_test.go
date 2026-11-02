package domain

import (
	"errors"
	"testing"
	"time"

	"github.com/shopspring/decimal"
)

// planNet runs a purchased item with on-hand 5, an open PO of 10 on day 3 and
// a sales line of 30 on day 20, and returns the engine's net requirements.
func planNet(t *testing.T) []NetDay {
	t.Helper()
	res, err := Run(0, []Item{{
		ID: "i-1", Coverage: CoverageRequirement, OrderType: OrderPurchase, LeadTimeDays: 5,
		OnHand:   dec("5"),
		Receipts: []Requirement{{Ref: "PO-OPEN", RefType: "purchase-line", Date: 3, Qty: dec("10")}},
		Demand:   []Requirement{{Ref: "SO-1", RefType: RefSales, Date: 20, Qty: dec("30"), Priority: PrioritySales}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	var days []NetDay
	for _, n := range res.Net {
		days = append(days, NetDay{Day: n.Day, Gross: n.Gross, Scheduled: n.ScheduledReceipts, Planned: n.PlannedReceipts, PAB: n.PAB})
	}
	if len(days) == 0 {
		t.Fatal("fixture needs net rows")
	}
	return days
}

// TestProjectPlanned_ReadsOpeningAndFlowsFromNetRows: the opening on-hand is
// read back from the first row, so the closing balance of every bucket is the
// stored PAB of the last day at or before the bucket's end.
func TestProjectPlanned_ReadsOpeningAndFlowsFromNetRows(t *testing.T) {
	days := planNet(t)
	opening, p, err := ProjectPlanned(days, ProjectionInput{UnitCost: unitCost("2", "EUR"), HorizonDays: 28, Bucket: BucketWeek})
	if err != nil {
		t.Fatal(err)
	}
	if !opening.Equal(dec("5")) {
		t.Fatalf("opening = %s, want 5", opening)
	}
	if len(p.Periods) != 4 {
		t.Fatalf("periods = %d, want 4", len(p.Periods))
	}
	for _, row := range p.Periods {
		want := opening
		for _, d := range days {
			if d.Day <= row.End {
				want = d.PAB
			}
		}
		if !row.ClosingOnHand.Equal(want) {
			t.Errorf("days %d-%d: closing %s, stored PAB %s", row.Start, row.End, row.ClosingOnHand, want)
		}
		if row.Shortage {
			t.Errorf("days %d-%d: planned balance %s flagged short", row.Start, row.End, row.ClosingOnHand)
		}
	}
	if !p.TotalDemand.Equal(dec("30")) || !p.TotalSupply.Equal(dec("10")) {
		t.Errorf("totals demand %s supply %s; want 30 and 10", p.TotalDemand, p.TotalSupply)
	}
	if got := p.Periods[0].Value.Value; !got.Equal(dec("30")) {
		t.Errorf("value of week 1 = %s, want 15 on hand at 2", got)
	}
}

func TestProjectPlanned_ItemWithoutRowsProjectsNothingButZeros(t *testing.T) {
	opening, p, err := ProjectPlanned(nil, ProjectionInput{UnitCost: unitCost("0", "XXX"), HorizonDays: 14, Bucket: BucketWeek})
	if err != nil {
		t.Fatal(err)
	}
	if !opening.IsZero() || len(p.Periods) != 2 || !p.ClosingOnHand.IsZero() {
		t.Fatalf("opening %s periods %d closing %s", opening, len(p.Periods), p.ClosingOnHand)
	}
}

func TestProjectPlanned_NegativeBalanceIsAShortage(t *testing.T) {
	// A past-due demand of 40 against on-hand 10 leaves PAB -30 on day 0.
	days := []NetDay{{Day: 0, Gross: dec("40"), Scheduled: decimal.Zero, Planned: decimal.Zero, PAB: dec("-30")}}
	opening, p, err := ProjectPlanned(days, ProjectionInput{UnitCost: unitCost("0", "XXX"), HorizonDays: 7, Bucket: BucketDay})
	if err != nil {
		t.Fatal(err)
	}
	if !opening.Equal(dec("10")) || !p.Periods[0].Shortage || !p.Periods[0].ClosingOnHand.Equal(dec("-30")) {
		t.Fatalf("opening %s, first day %+v", opening, p.Periods[0])
	}
}

// TestProjectPlanned_FirstMovementPastTheHorizonKeepsTheOnHand: the first
// stored day can lie past the horizon; it still gives the opening on-hand,
// which then stays flat across the horizon and its demand is outside it.
func TestProjectPlanned_FirstMovementPastTheHorizonKeepsTheOnHand(t *testing.T) {
	days := []NetDay{{Day: 200, Gross: dec("30"), Scheduled: decimal.Zero, Planned: decimal.Zero, PAB: dec("-5")}}
	opening, p, err := ProjectPlanned(days, ProjectionInput{UnitCost: unitCost("0", "XXX"), HorizonDays: 14, Bucket: BucketWeek})
	if err != nil {
		t.Fatal(err)
	}
	if !opening.Equal(dec("25")) || !p.ClosingOnHand.Equal(dec("25")) || !p.TotalDemand.IsZero() || p.Periods[1].Shortage {
		t.Fatalf("opening %s closing %s demand %s", opening, p.ClosingOnHand, p.TotalDemand)
	}
	if p.Periods[1].DaysOfSupply != nil {
		t.Fatalf("no demand within 30 days of the horizon, days of supply %s", p.Periods[1].DaysOfSupply)
	}
}

// TestCalendarMonthEnds: month buckets of a dated plan end on the last day of
// each calendar month, the first starting at the as-of date and the last
// cut at the horizon by Project.
func TestCalendarMonthEnds(t *testing.T) {
	asOf := time.Date(2026, 11, 2, 0, 0, 0, 0, time.UTC)
	ends := CalendarMonthEnds(asOf, 84)
	// 30 Nov, 31 Dec, 31 Jan 2027 as offsets from 2 Nov.
	if len(ends) != 3 || ends[0] != 28 || ends[1] != 59 || ends[2] != 90 {
		t.Fatalf("ends %v", ends)
	}
	p, err := Project(ProjectionInput{
		UnitCost: unitCost("0", "XXX"), HorizonDays: 84, Bucket: BucketMonth, PeriodEnds: ends,
		Demand: []Flow{{Day: 28, Qty: dec("1")}, {Day: 29, Qty: dec("2")}, {Day: 83, Qty: dec("4")}, {Day: 84, Qty: dec("8")}},
	})
	if err != nil {
		t.Fatal(err)
	}
	type span struct{ start, end int }
	want := []span{{0, 28}, {29, 59}, {60, 83}}
	if len(p.Periods) != len(want) {
		t.Fatalf("periods %+v", p.Periods)
	}
	for i, w := range want {
		if p.Periods[i].Start != w.start || p.Periods[i].End != w.end {
			t.Errorf("period %d = %d-%d, want %d-%d", i, p.Periods[i].Start, p.Periods[i].End, w.start, w.end)
		}
	}
	if !p.Periods[0].Demand.Equal(dec("1")) || !p.Periods[1].Demand.Equal(dec("2")) || !p.Periods[2].Demand.Equal(dec("4")) {
		t.Errorf("demand per month %s %s %s", p.Periods[0].Demand, p.Periods[1].Demand, p.Periods[2].Demand)
	}
	// An as-of on the last day of a month gives a one-day first bucket; the
	// month that holds the horizon is the last end.
	if e := CalendarMonthEnds(time.Date(2027, 1, 31, 0, 0, 0, 0, time.UTC), 40); len(e) != 3 || e[0] != 0 || e[1] != 28 || e[2] != 59 {
		t.Fatalf("from 31 Jan: %v", e)
	}
	if _, err := Project(ProjectionInput{UnitCost: unitCost("0", "XXX"), HorizonDays: 10, Bucket: BucketMonth, PeriodEnds: []int{5, 5}}); !errors.Is(err, ErrInvalidPeriods) {
		t.Fatalf("repeated end: %v", err)
	}
}

// TestCalendarWeekEnds: week buckets of a dated plan are ISO weeks ending on
// Sunday; the first runs from the as-of date to the Sunday of its week.
func TestCalendarWeekEnds(t *testing.T) {
	cases := []struct {
		name    string
		asOf    time.Time
		horizon int
		want    []int
	}{
		{"monday", time.Date(2026, 11, 2, 0, 0, 0, 0, time.UTC), 21, []int{6, 13, 20}},
		{"wednesday", time.Date(2026, 11, 4, 0, 0, 0, 0, time.UTC), 21, []int{4, 11, 18, 25}},
		{"sunday", time.Date(2026, 11, 8, 0, 0, 0, 0, time.UTC), 10, []int{0, 7, 14}},
		{"saturday", time.Date(2026, 11, 7, 0, 0, 0, 0, time.UTC), 1, []int{1}},
		// ISO week 53 of 2026 runs from Monday 28 December to Sunday 3 January 2027.
		{"week 53 across the year end", time.Date(2026, 12, 30, 0, 0, 0, 0, time.UTC), 12, []int{4, 11}},
		{"new year's day in week 53", time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC), 3, []int{2}},
		// A time of day or zone does not move the week.
		{"late evening in a western zone", time.Date(2026, 11, 8, 23, 30, 0, 0, time.FixedZone("UTC-8", -8*3600)), 2, []int{0, 7}},
	}
	for _, c := range cases {
		got := CalendarWeekEnds(c.asOf, c.horizon)
		if len(got) != len(c.want) {
			t.Errorf("%s: ends %v, want %v", c.name, got, c.want)
			continue
		}
		for i := range got {
			if got[i] != c.want[i] {
				t.Errorf("%s: ends %v, want %v", c.name, got, c.want)
				break
			}
		}
	}
	// From Wednesday 4 November: 4 days, then full weeks, the last cut at the horizon.
	p, err := Project(ProjectionInput{
		UnitCost: unitCost("0", "XXX"), HorizonDays: 12, Bucket: BucketWeek,
		PeriodEnds: CalendarWeekEnds(time.Date(2026, 11, 4, 0, 0, 0, 0, time.UTC), 12),
		Demand:     []Flow{{Day: 4, Qty: dec("1")}, {Day: 5, Qty: dec("2")}, {Day: 11, Qty: dec("4")}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Periods) != 2 || p.Periods[0].End != 4 || p.Periods[1].Start != 5 || p.Periods[1].End != 11 {
		t.Fatalf("periods %+v", p.Periods)
	}
	if !p.Periods[0].Demand.Equal(dec("1")) || !p.Periods[1].Demand.Equal(dec("6")) {
		t.Errorf("demand per week %s %s", p.Periods[0].Demand, p.Periods[1].Demand)
	}
}
