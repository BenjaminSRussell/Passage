package domain

import "testing"

// TestRun_SafetyStockFirstIssue covers BR-22.5: with policy first-issue the
// safety stock is a demand on the item's first issue date, not at today, so
// the safety-stock order is dated that day and the first sale is covered from
// stock; with policy today it is dated today.
func TestRun_SafetyStockFirstIssue(t *testing.T) {
	sale := func(ref string, day int) Requirement {
		return Requirement{Ref: ref, RefType: RefSales, Date: day, Qty: d("10"), Priority: PrioritySales}
	}
	item := func(policy string) Item {
		return Item{ID: "FI", Coverage: CoverageRequirement, OrderType: OrderPurchase, LeadTimeDays: 5, OnHand: d("3"), SafetyStock: d("4"),
			SafetyPolicy: policy, Demand: []Requirement{sale("SO-2", 30), sale("SO-1", 20)}}
	}
	today, err := Run(0, []Item{item("")})
	if err != nil {
		t.Fatal(err)
	}
	first, err := Run(0, []Item{item(SafetyFirstIssue)})
	if err != nil {
		t.Fatal(err)
	}
	// today: 3 on hand is short of the 4 held back at day 0, so one unit is
	// ordered at once (delivered day 5).
	if len(today.Orders) == 0 || today.Orders[0].DeliveryDate != 5 || today.Orders[0].Qty.Cmp(d("1")) != 0 {
		t.Fatalf("policy today orders = %+v; want 1 delivered day 5 first", today.Orders)
	}
	// first-issue: nothing is demanded before day 20, where SO-1 (10) and the
	// safety stock (4) fall due against 3 on hand: 11 are ordered for day 20.
	total := d("0")
	for _, o := range first.Orders {
		if o.DeliveryDate < 20 && o.Qty.IsPositive() {
			t.Fatalf("first-issue ordered early: %+v", first.Orders)
		}
		if o.DeliveryDate == 20 {
			total = total.Add(o.Qty)
		}
	}
	if total.Cmp(d("11")) != 0 {
		t.Fatalf("first-issue orders for day 20 = %s (%+v); want 11", total, first.Orders)
	}
	// The net requirements view puts the safety stock where the netting did:
	// with first-issue no gross requirement at today and 14 (10 + 4) at day
	// 20, so PAB stays at the 3 on hand until then.
	gross := map[int]string{}
	for _, r := range first.Net {
		gross[r.Day] = r.Gross.String()
		if r.Day < 20 && (r.Gross.IsPositive() || !r.PAB.Equal(d("3"))) {
			t.Fatalf("first-issue net row before the first issue: %+v", r)
		}
	}
	if gross[20] != "14" {
		t.Fatalf("first-issue gross at day 20 = %q (%+v); want 14", gross[20], first.Net)
	}
	for _, r := range today.Net {
		if r.Day == 0 && !r.Gross.Equal(d("4")) {
			t.Fatalf("policy today gross at day 0 = %s; want the safety stock 4", r.Gross)
		}
	}
}

func TestSafetyDate(t *testing.T) {
	rs := &runState{today: 3}
	it := &Item{SafetyPolicy: SafetyFirstIssue, Demand: []Requirement{{Date: 12}, {Date: 8}, {Date: 1}}}
	if got := rs.safetyDate(it); got != 3 {
		t.Fatalf("past-due demand counts at today: got %d, want 3", got)
	}
	it.Demand = []Requirement{{Date: 12}, {Date: 8}}
	if got := rs.safetyDate(it); got != 8 {
		t.Fatalf("first-issue = %d, want 8", got)
	}
	it.Demand = nil
	if got := rs.safetyDate(it); got != 3 {
		t.Fatalf("no demand falls back to today: got %d", got)
	}
	it.SafetyPolicy = SafetyToday
	it.Demand = []Requirement{{Date: 12}}
	if got := rs.safetyDate(it); got != 3 {
		t.Fatalf("policy today = %d, want 3", got)
	}
}

// TestRun_SafetyStockFirstIssueCountsDependentDemand: a component's first
// issue is its first dependent (BOM) demand from the explosion, so a component
// with no independent demand nets its safety stock on the day its parent's
// planned order needs it (BR-22.5), not at today.
func TestRun_SafetyStockFirstIssueCountsDependentDemand(t *testing.T) {
	fg := Item{ID: "FG", Coverage: CoverageRequirement, OrderType: OrderProduction, LeadTimeDays: 5,
		Demand:     []Requirement{{Ref: "SO-1", RefType: RefSales, Date: 20, Qty: d("10"), Priority: PrioritySales}},
		Components: []BOMLine{{ItemID: "RM", Qty: d("1")}}}
	rm := Item{ID: "RM", Coverage: CoverageRequirement, OrderType: OrderPurchase, LeadTimeDays: 2, SafetyStock: d("4"), SafetyPolicy: SafetyFirstIssue}
	r, err := Run(0, []Item{fg, rm})
	if err != nil {
		t.Fatal(err)
	}
	total := d("0")
	for _, o := range r.Orders {
		if o.ItemID != "RM" {
			continue
		}
		if o.DeliveryDate != 15 {
			t.Fatalf("component order %+v; want every order delivered day 15 (the parent's order date)", o)
		}
		total = total.Add(o.Qty)
	}
	if !total.Equal(d("14")) {
		t.Fatalf("component ordered %s (%+v); want 14 (10 dependent + 4 safety)", total, r.Orders)
	}
}
