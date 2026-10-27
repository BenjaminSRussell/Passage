package domain

import (
	"errors"
	"testing"

	"github.com/shopspring/decimal"
	"pgregory.net/rapid"
)

// monFriCalendar: day 0 is a Monday, Saturday and Sunday are closed.
func monFriCalendar(closedDays ...int) *Calendar {
	c := &Calendar{Weekday0: 1, ClosedWeekdays: [7]bool{true, false, false, false, false, false, true}}
	if len(closedDays) > 0 {
		c.ClosedDays = map[int]bool{}
		for _, d := range closedDays {
			c.ClosedDays[d] = true
		}
	}
	return c
}

func TestCalendar_BackAndForward(t *testing.T) {
	tests := []struct {
		name string
		cal  *Calendar
		fn   func(*Calendar) int
		want int
	}{
		{"open day back zero stays", monFriCalendar(), func(c *Calendar) int { return c.Back(2, 0) }, 2},
		{"saturday back zero moves to friday", monFriCalendar(), func(c *Calendar) int { return c.Back(5, 0) }, 4},
		{"sunday back one working day is thursday", monFriCalendar(), func(c *Calendar) int { return c.Back(6, 1) }, 3},
		{"monday back one working day skips the weekend", monFriCalendar(), func(c *Calendar) int { return c.Back(7, 1) }, 4},
		{"monday back two working days", monFriCalendar(), func(c *Calendar) int { return c.Back(7, 2) }, 3},
		{"saturday forward zero moves to monday", monFriCalendar(), func(c *Calendar) int { return c.Forward(5, 0) }, 7},
		{"friday forward one working day is monday", monFriCalendar(), func(c *Calendar) int { return c.Forward(4, 1) }, 7},
		{"closed date forward", monFriCalendar(2), func(c *Calendar) int { return c.Forward(2, 0) }, 3},
		{"closed date backward", monFriCalendar(2), func(c *Calendar) int { return c.Back(2, 0) }, 1},
		{"back over closed date", monFriCalendar(2), func(c *Calendar) int { return c.Back(3, 1) }, 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.fn(tt.cal); got != tt.want {
				t.Fatalf("got %d want %d", got, tt.want)
			}
		})
	}
}

func TestCalendar_Validate(t *testing.T) {
	all := Calendar{ClosedWeekdays: [7]bool{true, true, true, true, true, true, true}}
	if err := all.Validate(); !errors.Is(err, ErrInvalidCalendar) {
		t.Fatalf("all weekdays closed: got %v", err)
	}
	bad := Calendar{Weekday0: 7}
	if err := bad.Validate(); !errors.Is(err, ErrInvalidCalendar) {
		t.Fatalf("weekday out of range: got %v", err)
	}
	if err := monFriCalendar().Validate(); err != nil {
		t.Fatalf("valid calendar: %v", err)
	}
}

// TestRunWithCalendar_WorkingDayOffsets: a 2-working-day lead time on Monday demand
// is dated Friday with a calendar and 2 calendar days earlier without one; a
// Saturday demand moves to Monday (BR-22.8).
func TestRunWithCalendar_WorkingDayOffsets(t *testing.T) {
	item := func() []Item {
		return []Item{{
			ID: "FG", Coverage: CoverageRequirement, OrderType: OrderPurchase, LeadTimeDays: 2,
			Demand: []Requirement{{Ref: "SO-1", RefType: RefSales, Date: 5, Qty: decimal.NewFromInt(10), Priority: PrioritySales}},
		}}
	}
	withCal, err := RunWithCalendar(0, monFriCalendar(), item())
	if err != nil {
		t.Fatal(err)
	}
	if len(withCal.Orders) != 1 {
		t.Fatalf("orders %d want 1", len(withCal.Orders))
	}
	if o := withCal.Orders[0]; o.DeliveryDate != 7 || o.OrderDate != 3 {
		t.Fatalf("with calendar delivery %d order %d; want 7 and 3", o.DeliveryDate, o.OrderDate)
	}
	plain, err := Run(0, item())
	if err != nil {
		t.Fatal(err)
	}
	if o := plain.Orders[0]; o.DeliveryDate != 5 || o.OrderDate != 3 {
		t.Fatalf("calendar days delivery %d order %d; want 5 and 3", o.DeliveryDate, o.OrderDate)
	}
}

func TestRunWithCalendar_InvalidCalendarRejected(t *testing.T) {
	closedAll := &Calendar{ClosedWeekdays: [7]bool{true, true, true, true, true, true, true}}
	if _, err := RunWithCalendar(0, closedAll, nil); !errors.Is(err, ErrInvalidCalendar) {
		t.Fatalf("got %v want ErrInvalidCalendar", err)
	}
}

// TestRunWithCalendar_FuturesCountWorkingDays: a demand on Tuesday with a
// lead time of 2 working days should have been ordered the Friday before
// today (Monday), so the order is expected today + 2 working days, Wednesday,
// one day late (BR-22.1 step 7 with BR-22.8).
func TestRunWithCalendar_FuturesCountWorkingDays(t *testing.T) {
	r, err := RunWithCalendar(0, monFriCalendar(), []Item{{
		ID: "FG", Coverage: CoverageRequirement, OrderType: OrderPurchase, LeadTimeDays: 2,
		Demand: []Requirement{{Ref: "SO-1", RefType: RefSales, Date: 1, Qty: d("10"), Priority: PrioritySales}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	o := r.Orders[0]
	if o.DeliveryDate != 1 || o.OrderDate != -3 || o.FuturesDate == nil || *o.FuturesDate != 2 || o.DelayDays != 1 {
		t.Fatalf("order %+v futures %v; want delivery 1, order -3 (Friday), futures 2, delay 1", o, o.FuturesDate)
	}
}

// TestRunWithCalendar_ReceiptOnClosedDayMovesBackward: an open PO due on
// Sunday counts from Friday (BR-22.8, supply moves backward), so it covers a
// Friday demand and no planned order is needed.
func TestRunWithCalendar_ReceiptOnClosedDayMovesBackward(t *testing.T) {
	items := []Item{{
		ID: "RM", Coverage: CoverageRequirement, OrderType: OrderPurchase, LeadTimeDays: 1,
		Demand:   []Requirement{{Ref: "SO-1", RefType: RefSales, Date: 4, Qty: d("10"), Priority: PrioritySales}},
		Receipts: []Requirement{{Ref: "PO-1", RefType: "purchase-line", Date: 6, Qty: d("10")}},
	}}
	withCal, err := RunWithCalendar(0, monFriCalendar(), items)
	if err != nil {
		t.Fatal(err)
	}
	if len(withCal.Orders) != 0 {
		t.Fatalf("orders %+v; the Sunday PO lands Friday and covers the demand", withCal.Orders)
	}
	if rows := rowsOf(withCal.Net, "RM"); len(rows) != 1 || rows[0].Day != 4 || !rows[0].ScheduledReceipts.Equal(d("10")) {
		t.Fatalf("net rows %+v; want the receipt on Friday (day 4)", rows)
	}
	plain, err := Run(0, items)
	if err != nil {
		t.Fatal(err)
	}
	if len(plain.Orders) != 1 {
		t.Fatalf("without a calendar the PO is two days late: orders %+v", plain.Orders)
	}
}

// TestRunWithCalendar_AllOpenEqualsCalendarDays: a calendar with no closed day
// plans exactly like no calendar.
func TestRunWithCalendar_AllOpenEqualsCalendarDays(t *testing.T) {
	plain, err := Run(week(1), br222())
	if err != nil {
		t.Fatal(err)
	}
	open, err := RunWithCalendar(week(1), &Calendar{Weekday0: 1}, br222())
	if err != nil {
		t.Fatal(err)
	}
	if plain.Hash != open.Hash {
		t.Fatalf("hash %s with an all-open calendar, %s without", open.Hash, plain.Hash)
	}
}

// TestRunWithCalendar_PropertyDatesAreWorkingDays: with any calendar that has
// an open weekday, every planned order is ordered and delivered on a working
// day, and pegging still explains every planned order in full (AC-22.2).
func TestRunWithCalendar_PropertyDatesAreWorkingDays(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		items := genItems(t)
		cal := &Calendar{Weekday0: rapid.IntRange(0, 6).Draw(t, "weekday0"), ClosedDays: map[int]bool{}}
		for wd := range cal.ClosedWeekdays {
			cal.ClosedWeekdays[wd] = rapid.Float64Range(0, 1).Draw(t, "closedWeekday") < 0.3
		}
		cal.ClosedWeekdays[rapid.IntRange(0, 6).Draw(t, "openWeekday")] = false
		for _, day := range rapid.SliceOfN(rapid.IntRange(-10, 40), 0, 8).Draw(t, "closedDays") {
			cal.ClosedDays[day] = true
		}
		r, err := RunWithCalendar(0, cal, items)
		if err != nil {
			t.Skip(err)
		}
		pegged := map[string]decimal.Decimal{}
		for _, p := range r.Pegging {
			pegged[p.SupplyRef] = pegged[p.SupplyRef].Add(p.Qty)
		}
		for _, o := range r.Orders {
			if !cal.IsOpen(o.OrderDate) || !cal.IsOpen(o.DeliveryDate) {
				t.Fatalf("order %s dated %d to %d on a closed day", o.Number, o.OrderDate, o.DeliveryDate)
			}
			if o.FuturesDate != nil && !cal.IsOpen(*o.FuturesDate) {
				t.Fatalf("order %s expected on closed day %d", o.Number, *o.FuturesDate)
			}
			if !pegged[o.Number].Equal(o.Qty) {
				t.Fatalf("order %s qty %s pegged %s", o.Number, o.Qty, pegged[o.Number])
			}
		}
	})
}
