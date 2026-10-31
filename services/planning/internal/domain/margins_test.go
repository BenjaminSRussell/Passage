package domain

import "testing"

// TestRun_IssueAndReorderMargins covers FR-22.5: the issue margin moves each
// demand of the item earlier, the reorder margin lengthens the lead time of its
// planned orders. Safety stock is not an issue and is not moved.
func TestRun_IssueAndReorderMargins(t *testing.T) {
	item := func(issue, reorder int) Item {
		return Item{ID: "M", Coverage: CoverageRequirement, OrderType: OrderPurchase, LeadTimeDays: 5,
			IssueMarginDays: issue, ReorderMarginDays: reorder,
			Demand: []Requirement{{Ref: "SO-1", RefType: RefSales, Date: 30, Qty: d("10"), Priority: PrioritySales}}}
	}
	base, err := Run(0, []Item{item(0, 0)})
	if err != nil || len(base.Orders) != 1 {
		t.Fatalf("base = %+v, %v", base.Orders, err)
	}
	if o := base.Orders[0]; o.DeliveryDate != 30 || o.OrderDate != 25 {
		t.Fatalf("base order = %+v; want delivery 30, order 25", o)
	}
	issue, err := Run(0, []Item{item(4, 0)})
	if err != nil {
		t.Fatal(err)
	}
	if o := issue.Orders[0]; o.DeliveryDate != 26 || o.OrderDate != 21 {
		t.Fatalf("issue margin 4 order = %+v; want delivery 26, order 21", o)
	}
	reorder, err := Run(0, []Item{item(0, 3)})
	if err != nil {
		t.Fatal(err)
	}
	if o := reorder.Orders[0]; o.DeliveryDate != 30 || o.OrderDate != 22 {
		t.Fatalf("reorder margin 3 order = %+v; want delivery 30, order 22", o)
	}
	// An order that cannot be released in the past is expected at today plus
	// the lead time and the reorder margin.
	late := item(0, 3)
	late.Demand[0].Date = 6
	res, err := Run(0, []Item{late})
	if err != nil {
		t.Fatal(err)
	}
	o := res.Orders[0]
	if o.FuturesDate == nil || *o.FuturesDate != 8 || o.DelayDays != 2 {
		t.Fatalf("late order = %+v; want futures day 8 and 2 days of delay", o)
	}
}

func TestRun_IssueMarginKeepsSafetyStockDate(t *testing.T) {
	it := Item{ID: "S", Coverage: CoverageRequirement, OrderType: OrderPurchase, LeadTimeDays: 2, SafetyStock: d("5"), IssueMarginDays: 10}
	res, err := Run(0, []Item{it})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Orders) != 1 || res.Orders[0].OrderDate != 0 || res.Orders[0].DeliveryDate != 2 {
		t.Fatalf("orders = %+v; want the safety order dated today", res.Orders)
	}
}

func TestRun_NegativeMarginsAreRefused(t *testing.T) {
	for _, it := range []Item{
		{ID: "A", Coverage: CoverageRequirement, OrderType: OrderPurchase, IssueMarginDays: -1},
		{ID: "A", Coverage: CoverageRequirement, OrderType: OrderPurchase, ReorderMarginDays: -1},
	} {
		if _, err := Run(0, []Item{it}); err == nil {
			t.Fatalf("margins %d/%d accepted", it.IssueMarginDays, it.ReorderMarginDays)
		}
	}
}

// TestRun_IssueMarginCountsFromTheNettedWorkingDay: a demand on a Sunday is
// netted on Monday, so an issue margin of one working day moves it to Friday,
// not two working days back to Thursday.
func TestRun_IssueMarginCountsFromTheNettedWorkingDay(t *testing.T) {
	it := Item{ID: "W", Coverage: CoverageRequirement, OrderType: OrderPurchase, IssueMarginDays: 1,
		Demand: []Requirement{{Ref: "SO-1", RefType: RefSales, Date: 13, Qty: d("1"), Priority: PrioritySales}}}
	res, err := RunWithCalendar(0, monFriCalendar(), []Item{it})
	if err != nil || len(res.Orders) != 1 {
		t.Fatalf("orders = %+v, %v", res.Orders, err)
	}
	if o := res.Orders[0]; o.DeliveryDate != 11 {
		t.Fatalf("order = %+v; want delivery on Friday day 11", o)
	}
}

// TestRun_FirstIssueSafetyStockFollowsTheIssueMargin: with the first-issue
// policy the safety stock is due when the first issue is netted, which the
// issue margin has moved earlier.
func TestRun_FirstIssueSafetyStockFollowsTheIssueMargin(t *testing.T) {
	it := Item{ID: "F", Coverage: CoverageRequirement, OrderType: OrderPurchase, SafetyStock: d("5"),
		SafetyPolicy: SafetyFirstIssue, IssueMarginDays: 4,
		Demand: []Requirement{{Ref: "SO-1", RefType: RefSales, Date: 20, Qty: d("10"), Priority: PrioritySales}}}
	res, err := Run(0, []Item{it})
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, p := range res.Pegging {
		if p.DemandRefType == RefSafety {
			found = true
			if p.DemandDate != 16 {
				t.Fatalf("safety peg = %+v; want day 16", p)
			}
		}
	}
	if !found {
		t.Fatalf("no safety-stock peg in %+v", res.Pegging)
	}
}

// TestRun_ComponentIssueMarginDelaysTheParent: the component is needed three
// working days before the parent starts. It arrives two days after that, so the
// parent starts and is delivered two days late; without the margin it would
// not be late at all.
func TestRun_ComponentIssueMarginDelaysTheParent(t *testing.T) {
	items := func(issue int) []Item {
		return []Item{
			{ID: "P", Coverage: CoverageRequirement, OrderType: OrderProduction, LeadTimeDays: 5,
				Demand:     []Requirement{{Ref: "SO-1", RefType: RefSales, Date: 10, Qty: d("1"), Priority: PrioritySales}},
				Components: []BOMLine{{ItemID: "C", Qty: d("1")}}},
			{ID: "C", Coverage: CoverageRequirement, OrderType: OrderPurchase, LeadTimeDays: 4, IssueMarginDays: issue},
		}
	}
	parentOf := func(res Result) PlannedOrder {
		t.Helper()
		for _, o := range res.Orders {
			if o.ItemID == "P" {
				return o
			}
		}
		t.Fatalf("no parent order in %+v", res.Orders)
		return PlannedOrder{}
	}
	res, err := Run(0, items(3))
	if err != nil {
		t.Fatal(err)
	}
	if p := parentOf(res); p.FuturesDate == nil || *p.FuturesDate != 12 || p.DelayDays != 2 {
		t.Fatalf("parent = %+v; want futures day 12 and 2 days of delay", p)
	}
	res, err = Run(0, items(0))
	if err != nil {
		t.Fatal(err)
	}
	if p := parentOf(res); p.FuturesDate != nil || p.DelayDays != 0 {
		t.Fatalf("parent without margin = %+v; want no delay", p)
	}
}
