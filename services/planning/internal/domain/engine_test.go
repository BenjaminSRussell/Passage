package domain

import (
	"errors"
	"testing"

	"github.com/shopspring/decimal"
	"pgregory.net/rapid"
)

func d(s string) decimal.Decimal { return decimal.RequireFromString(s) }

// week converts a week number of the BR-22.2 example to a day index: week n = day 7n.
func week(n int) int { return 7 * n }

// br222 is the 3-level worked example of BR-22.2, weekly buckets, today = week 1.
func br222() []Item {
	return []Item{
		{ID: "FG-100", Coverage: CoverageRequirement, OrderType: OrderProduction, LeadTimeDays: 7,
			OnHand: d("20"), SafetyStock: d("10"),
			Demand: []Requirement{
				{Ref: "SO-1", RefType: RefSales, Date: week(4), Qty: d("120"), Priority: PrioritySales},
				{Ref: "FC-1", RefType: RefForecast, Date: week(5), Qty: d("80"), Priority: PriorityForecast},
			},
			Components: []BOMLine{{ItemID: "SA-200", Qty: d("2")}, {ItemID: "RM-300", Qty: d("1")}}},
		{ID: "SA-200", Coverage: CoverageRequirement, OrderType: OrderProduction, LeadTimeDays: 7,
			OnHand: d("50"), MultipleQty: d("100"),
			Components: []BOMLine{{ItemID: "RM-300", Qty: d("3")}}},
		{ID: "RM-300", Coverage: CoverageRequirement, OrderType: OrderPurchase, LeadTimeDays: 14,
			OnHand: d("200"), MinOrderQty: d("500"),
			Receipts: []Requirement{{Ref: "PO-OPEN", RefType: "purchase-line", Date: week(2), Qty: d("300")}}},
	}
}

func findOrder(t *testing.T, r Result, item string, delivery int) PlannedOrder {
	t.Helper()
	for _, o := range r.Orders {
		if o.ItemID == item && o.DeliveryDate == delivery {
			return o
		}
	}
	t.Fatalf("no planned order for %s due %d in %+v", item, delivery, r.Orders)
	return PlannedOrder{}
}

func TestRun_BR222WorkedExample(t *testing.T) {
	r, err := Run(week(1), br222())
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Orders) != 6 {
		t.Fatalf("orders = %d; want 6 (PP-1..PP-4, PO-1, PO-2): %+v", len(r.Orders), r.Orders)
	}
	checks := []struct {
		item      string
		delivery  int
		qty       int
		orderDate int
	}{
		{"FG-100", week(4), 110, week(3)},
		{"FG-100", week(5), 80, week(4)},
		{"SA-200", week(3), 200, week(2)},
		{"SA-200", week(4), 200, week(3)},
		{"RM-300", week(2), 500, week(0)},
		{"RM-300", week(3), 500, week(1)},
	}
	for _, c := range checks {
		o := findOrder(t, r, c.item, c.delivery)
		if !o.Qty.Equal(d(itoa(c.qty))) || o.OrderDate != c.orderDate {
			t.Errorf("%s due %d: qty %s order %d; want %d / %d", c.item, c.delivery, o.Qty, o.OrderDate, c.qty, c.orderDate)
		}
	}
	po1 := findOrder(t, r, "RM-300", week(2))
	if po1.FuturesDate == nil || *po1.FuturesDate != week(3) || po1.DelayDays != 7 {
		t.Fatalf("PO-1 futures/delay = %v / %d; want week 3 and 7 days", po1.FuturesDate, po1.DelayDays)
	}
	var delays int
	for _, m := range r.Messages {
		if m.Action == ActionDelay && m.DelayDays == 7 && m.SupplyRef == po1.Number {
			delays++
		}
	}
	if delays != 1 {
		t.Fatalf("want one 7-day delay message for PO-1, got %d in %+v", delays, r.Messages)
	}
}

// BR-22.2 "Propagation": PO-1 is late, so PP-3 (pegged to PO-1) cannot finish
// before week 4, so PP-1 (pegged to PP-3) cannot finish before week 5, so the
// sales line SO-1 (requested week 4) gets a 7-day delay message (AC-22.1).
func TestRun_BR222DelayPropagatesToSalesLine(t *testing.T) {
	r, err := Run(week(1), br222())
	if err != nil {
		t.Fatal(err)
	}
	pp3 := findOrder(t, r, "SA-200", week(3))
	pp1 := findOrder(t, r, "FG-100", week(4))
	for _, c := range []struct {
		name    string
		o       PlannedOrder
		futures int
	}{{"PP-3", pp3, week(4)}, {"PP-1", pp1, week(5)}} {
		if c.o.FuturesDate == nil || *c.o.FuturesDate != c.futures || c.o.DelayDays != 7 {
			t.Errorf("%s futures/delay = %v / %d; want day %d and 7", c.name, c.o.FuturesDate, c.o.DelayDays, c.futures)
		}
	}
	// PP-2, PP-4 and PO-2 are on time.
	for _, o := range []PlannedOrder{findOrder(t, r, "FG-100", week(5)), findOrder(t, r, "SA-200", week(4)), findOrder(t, r, "RM-300", week(3))} {
		if o.FuturesDate != nil || o.DelayDays != 0 {
			t.Errorf("%s %s must not be delayed: %v / %d", o.ItemID, o.Number, o.FuturesDate, o.DelayDays)
		}
	}
	var onSales []Message
	for _, m := range r.Messages {
		if m.Action == ActionDelay && m.DemandRef == "SO-1" {
			onSales = append(onSales, m)
		}
	}
	if len(onSales) != 1 || onSales[0].DelayDays != 7 || onSales[0].SuggestedDate != week(5) || onSales[0].SupplyRef != pp1.Number {
		t.Fatalf("delay messages on SO-1 = %+v; want one, expected week 5, 7 days, from %s", onSales, pp1.Number)
	}
	for _, m := range r.Messages {
		if m.DemandRef == "FC-1" {
			t.Fatalf("forecast FC-1 is covered on time, got %+v", m)
		}
	}
}

func TestRun_BR222PeggedSupplyCoversAllDemand(t *testing.T) {
	r, err := Run(week(1), br222())
	if err != nil {
		t.Fatal(err)
	}
	// Every unit of RM-300 demand (600 + 600 dependent from SA-200, 110 + 80
	// from FG-100) is pegged to supply: 200 on hand, the 300 open PO and 890 of
	// the two planned purchases; the other 110 of them is surplus.
	var rmSupply decimal.Decimal
	for _, p := range r.Pegging {
		if p.ItemID == "RM-300" && p.DemandRefType != RefSurplus {
			rmSupply = rmSupply.Add(p.Qty)
		}
	}
	if !rmSupply.Equal(d("1390")) {
		t.Fatalf("RM-300 pegged supply = %s; want 1390", rmSupply)
	}
}

func TestLotSize(t *testing.T) {
	type lotCase struct {
		name string
		need string
		minQ string
		mult string
		max  string
		std  string
		want []string
		rule string
	}
	tests := []lotCase{
		{"lot for lot", "110", "0", "0", "0", "0", []string{"110"}, "lot-for-lot"},
		{"minimum", "100", "500", "0", "0", "0", []string{"500"}, "min"},
		{"multiple rounds up", "170", "0", "100", "0", "0", []string{"200"}, "multiple"},
		{"standard rounds up", "130", "0", "0", "0", "50", []string{"150"}, "standard"},
		{"max splits into ceil", "310", "0", "0", "100", "0", []string{"100", "100", "100", "10"}, "lot-for-lot+max"},
		{"max with multiple", "250", "0", "100", "200", "0", []string{"200", "100"}, "multiple+max"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, rule, err := LotSize(d(tc.need), d(tc.minQ), d(tc.mult), d(tc.max), d(tc.std))
			if err != nil {
				t.Fatal(err)
			}
			if rule != tc.rule || len(got) != len(tc.want) {
				t.Fatalf("got %v %q; want %v %q", got, rule, tc.want, tc.rule)
			}
			for i := range got {
				if !got[i].Equal(d(tc.want[i])) {
					t.Fatalf("lot %d = %s; want %s", i, got[i], tc.want[i])
				}
			}
		})
	}
}

func TestLotSize_SplitLimit(t *testing.T) {
	if _, _, err := LotSize(d("100001"), d("0"), d("0"), d("1"), d("0")); !errors.Is(err, ErrTooManySplits) {
		t.Fatalf("err = %v; want ErrTooManySplits", err)
	}
}

func TestRun_MinMaxOrdersUpToMax(t *testing.T) {
	// AC-22.4: min 100, max 400, PAB 90 -> one planned order of 310.
	items := []Item{{ID: "MM-1", Coverage: CoverageMinMax, OrderType: OrderPurchase, LeadTimeDays: 7,
		OnHand: d("90"), ReorderMin: d("100"), ReorderMax: d("400")}}
	r, err := Run(0, items)
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Orders) != 1 || !r.Orders[0].Qty.Equal(d("310")) {
		t.Fatalf("orders = %+v; want one order of 310", r.Orders)
	}
}

// Regression (mrp-textbook S38): BR-22.1 step 5 checks PAB_t after day t's
// requirements. On-hand 80 below min 100 with 30 due today is PAB 50, so one
// order of 300 - 50 = 250; checking before today's demand ordered 220.
func TestRun_MinMaxChecksTodayAfterTodaysDemand(t *testing.T) {
	items := []Item{{ID: "MM-2", Coverage: CoverageMinMax, OrderType: OrderPurchase, LeadTimeDays: 7,
		OnHand: d("80"), ReorderMin: d("100"), ReorderMax: d("300"),
		Demand: []Requirement{{Ref: "S-1", RefType: RefSales, Date: 0, Qty: d("30"), Priority: PrioritySales}}}}
	r, err := Run(0, items)
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Orders) != 1 || !r.Orders[0].Qty.Equal(d("250")) || r.Orders[0].DeliveryDate != 0 {
		t.Fatalf("orders = %+v; want one order of 250 due today", r.Orders)
	}
}

// Regression: an overdue receipt is part of today's stock level, so min 100
// with on-hand 50 and 100 overdue is PAB 150 and orders nothing.
func TestRun_MinMaxCountsOverdueReceiptToday(t *testing.T) {
	items := []Item{{ID: "MM-3", Coverage: CoverageMinMax, OrderType: OrderPurchase, LeadTimeDays: 7,
		OnHand: d("50"), ReorderMin: d("100"), ReorderMax: d("400"),
		Receipts: []Requirement{{Ref: "PO-LATE", RefType: "purchase-line", Date: -3, Qty: d("100")}}}}
	r, err := Run(0, items)
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Orders) != 0 {
		t.Fatalf("orders = %+v; want none (PAB 150 is above min 100)", r.Orders)
	}
}

// Regression (mrp-textbook S41): a period order covers the sum of the window's
// net requirements. Short 10 on day 7 and 20 on day 14, with 50 arriving on day
// 21 inside the 21-day window, is one order of 30; netting the late receipt
// against the whole window ordered 10 and then 20 a week later.
func TestRun_PeriodCoverageIgnoresSupplyAfterTheShortfall(t *testing.T) {
	items := []Item{{ID: "P-2", Coverage: CoveragePeriod, PeriodDays: 21, OrderType: OrderPurchase, LeadTimeDays: 7,
		Demand: []Requirement{
			{Ref: "S-1", RefType: RefSales, Date: 7, Qty: d("10"), Priority: PrioritySales},
			{Ref: "S-2", RefType: RefSales, Date: 14, Qty: d("20"), Priority: PrioritySales},
			{Ref: "S-3", RefType: RefSales, Date: 28, Qty: d("15"), Priority: PrioritySales},
		},
		Receipts: []Requirement{{Ref: "PO-1", RefType: "purchase-line", Date: 21, Qty: d("50")}}}}
	r, err := Run(0, items)
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Orders) != 1 || !r.Orders[0].Qty.Equal(d("30")) || r.Orders[0].DeliveryDate != 7 {
		t.Fatalf("orders = %+v; want one order of 30 due day 7", r.Orders)
	}
}

// A receipt waited for under negative days is netted once: the period window
// must not subtract it a second time. 30 short on day 5, PO-1 10 on day 6
// (inside negative days), 5 more due day 8: one order of 20 + 5 = 25.
func TestRun_PeriodCoverageNetsWaitedReceiptOnce(t *testing.T) {
	items := []Item{{ID: "P-3", Coverage: CoveragePeriod, PeriodDays: 14, NegativeDays: 2, OrderType: OrderPurchase,
		Demand: []Requirement{
			{Ref: "S-1", RefType: RefSales, Date: 5, Qty: d("30"), Priority: PrioritySales},
			{Ref: "S-2", RefType: RefSales, Date: 8, Qty: d("5"), Priority: PrioritySales},
		},
		Receipts: []Requirement{{Ref: "PO-1", RefType: "purchase-line", Date: 6, Qty: d("10")}}}}
	r, err := Run(0, items)
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Orders) != 1 || !r.Orders[0].Qty.Equal(d("25")) || r.Orders[0].DeliveryDate != 5 {
		t.Fatalf("orders = %+v; want one order of 25 due day 5", r.Orders)
	}
}

func TestRun_PeriodCoverageGroupsDemandInWindow(t *testing.T) {
	items := []Item{{ID: "P-1", Coverage: CoveragePeriod, PeriodDays: 7, OrderType: OrderPurchase, LeadTimeDays: 0,
		Demand: []Requirement{
			{Ref: "S-1", RefType: RefSales, Date: 2, Qty: d("10"), Priority: PrioritySales},
			{Ref: "S-2", RefType: RefSales, Date: 4, Qty: d("10"), Priority: PrioritySales},
			{Ref: "S-3", RefType: RefSales, Date: 20, Qty: d("5"), Priority: PrioritySales},
		}}}
	r, err := Run(0, items)
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Orders) != 2 || !r.Orders[0].Qty.Equal(d("20")) || !r.Orders[1].Qty.Equal(d("5")) {
		t.Fatalf("orders = %+v; want 20 covering the window then 5", r.Orders)
	}
}

func TestRun_ManualCoverageRaisesShortageOnly(t *testing.T) {
	items := []Item{{ID: "M-1", Coverage: CoverageManual, OrderType: OrderPurchase,
		Demand: []Requirement{{Ref: "S-1", RefType: RefSales, Date: 3, Qty: d("7"), Priority: PrioritySales}}}}
	r, err := Run(0, items)
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Orders) != 0 || len(r.Messages) != 1 || r.Messages[0].Action != ActionShortage {
		t.Fatalf("manual coverage: orders %+v messages %+v", r.Orders, r.Messages)
	}
}

func TestRun_SafetyStockDatedTodayPlusLeadTime(t *testing.T) {
	items := []Item{{ID: "SS-1", Coverage: CoverageRequirement, OrderType: OrderPurchase, LeadTimeDays: 5,
		OnHand: d("3"), SafetyStock: d("10")}}
	r, err := Run(20, items)
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Orders) != 1 || !r.Orders[0].Qty.Equal(d("7")) || r.Orders[0].DeliveryDate != 25 || r.Orders[0].OrderDate != 20 {
		t.Fatalf("safety order = %+v; want 7 delivered day 25 ordered day 20", r.Orders)
	}
	if len(r.Messages) != 0 {
		t.Fatalf("safety order must not be delayed: %+v", r.Messages)
	}
}

// Regression: the safety-stock order lands after a later demand. It must not be
// ordered twice (once for safety stock, again when the sales demand arrives).
func TestRun_SafetyStockIsNotOrderedTwice(t *testing.T) {
	items := []Item{{ID: "SS-2", Coverage: CoverageRequirement, OrderType: OrderPurchase, LeadTimeDays: 5, SafetyStock: d("10"),
		Demand: []Requirement{{Ref: "S-1", RefType: RefSales, Date: 2, Qty: d("5"), Priority: PrioritySales}}}}
	r, err := Run(0, items)
	if err != nil {
		t.Fatal(err)
	}
	total := decimal.Zero
	for _, o := range r.Orders {
		total = total.Add(o.Qty)
	}
	if !total.Equal(d("15")) {
		t.Fatalf("planned %s in %+v; want 15 (10 safety + 5 sales)", total, r.Orders)
	}
}

// Regression: period coverage nets the supply already scheduled inside the period.
func TestRun_PeriodCoverageNetsScheduledReceipts(t *testing.T) {
	items := []Item{{ID: "P-2", Coverage: CoveragePeriod, PeriodDays: 7, OrderType: OrderPurchase,
		Demand: []Requirement{
			{Ref: "S-1", RefType: RefSales, Date: 0, Qty: d("100"), Priority: PrioritySales},
			{Ref: "S-2", RefType: RefSales, Date: 3, Qty: d("50"), Priority: PrioritySales},
		},
		Receipts: []Requirement{{Ref: "PO-1", RefType: "purchase-line", Date: 3, Qty: d("50")}}}}
	r, err := Run(0, items)
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Orders) != 1 || !r.Orders[0].Qty.Equal(d("100")) {
		t.Fatalf("orders = %+v; want one of 100 (150 demand in the period less the 50 PO)", r.Orders)
	}
}

// AC-22.5: a PO due 10 days before its earliest pegged demand with positive
// days 5 gets a Postpone message suggesting demand date - receipt margin.
func TestRun_EarlyReceiptRaisesPostpone(t *testing.T) {
	items := []Item{{ID: "R-1", Coverage: CoverageRequirement, OrderType: OrderPurchase, PositiveDays: 5, MarginDays: 1,
		Demand:   []Requirement{{Ref: "S-1", RefType: RefSales, Date: 20, Qty: d("40"), Priority: PrioritySales}},
		Receipts: []Requirement{{Ref: "PO-9", RefType: "purchase-line", Date: 10, Qty: d("40")}}}}
	r, err := Run(0, items)
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Orders) != 0 || len(r.Messages) != 1 {
		t.Fatalf("orders %+v messages %+v; want only one message", r.Orders, r.Messages)
	}
	if m := r.Messages[0]; m.Action != ActionPostpone || m.SupplyRef != "PO-9" || m.DemandRef != "S-1" || m.SuggestedDate != 19 {
		t.Fatalf("message = %+v; want postpone of PO-9 to day 19", m)
	}
}

func TestRun_EarlyReceiptWithinPositiveDaysOrUnlimitedIsQuiet(t *testing.T) {
	for _, positive := range []int{0, 15} {
		items := []Item{{ID: "R-2", Coverage: CoverageRequirement, OrderType: OrderPurchase, PositiveDays: positive,
			Demand:   []Requirement{{Ref: "S-1", RefType: RefSales, Date: 20, Qty: d("40"), Priority: PrioritySales}},
			Receipts: []Requirement{{Ref: "PO-9", RefType: "purchase-line", Date: 10, Qty: d("40")}}}}
		r, err := Run(0, items)
		if err != nil {
			t.Fatal(err)
		}
		if len(r.Messages) != 0 {
			t.Fatalf("positive days %d: messages %+v; want none (0 = unlimited)", positive, r.Messages)
		}
	}
}

// Negative days: a receipt due within negative days after the demand is waited
// for (no new planned order) and gets an Advance message to the demand date.
func TestRun_LateReceiptWithinNegativeDaysIsAdvanced(t *testing.T) {
	items := []Item{{ID: "R-3", Coverage: CoverageRequirement, OrderType: OrderPurchase, LeadTimeDays: 30, NegativeDays: 7,
		Demand:   []Requirement{{Ref: "S-1", RefType: RefSales, Date: 40, Qty: d("40"), Priority: PrioritySales}},
		Receipts: []Requirement{{Ref: "PO-9", RefType: "purchase-line", Date: 45, Qty: d("40")}}}}
	r, err := Run(0, items)
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Orders) != 0 {
		t.Fatalf("orders = %+v; the late PO is within negative days and must be used", r.Orders)
	}
	if len(r.Messages) != 1 || r.Messages[0].Action != ActionAdvance || r.Messages[0].SupplyRef != "PO-9" ||
		r.Messages[0].DelayDays != 5 || r.Messages[0].SuggestedDate != 40 {
		t.Fatalf("messages = %+v; want one advance of PO-9 by 5 days to day 40", r.Messages)
	}
}

// Beyond negative days the late receipt is not waited for: a planned order
// covers the demand and the receipt is left as surplus.
func TestRun_LateReceiptBeyondNegativeDaysIsReplanned(t *testing.T) {
	items := []Item{{ID: "R-4", Coverage: CoverageRequirement, OrderType: OrderPurchase, LeadTimeDays: 30, NegativeDays: 2,
		Demand:   []Requirement{{Ref: "S-1", RefType: RefSales, Date: 40, Qty: d("40"), Priority: PrioritySales}},
		Receipts: []Requirement{{Ref: "PO-9", RefType: "purchase-line", Date: 45, Qty: d("40")}}}}
	r, err := Run(0, items)
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Orders) != 1 || !r.Orders[0].Qty.Equal(d("40")) || r.Orders[0].DeliveryDate != 40 {
		t.Fatalf("orders = %+v; want one planned order of 40 due day 40", r.Orders)
	}
}

func TestRun_RejectsInvalidSnapshot(t *testing.T) {
	base := func() Item {
		return Item{ID: "V-1", Coverage: CoverageRequirement, OrderType: OrderPurchase}
	}
	cases := map[string]func(*Item){
		"unknown coverage":      func(it *Item) { it.Coverage = "lot-for-lot" },
		"unknown order type":    func(it *Item) { it.OrderType = "kanban" },
		"unknown safety policy": func(it *Item) { it.SafetyPolicy = "monthly" },
		"negative lead time":    func(it *Item) { it.LeadTimeDays = -1 },
		"negative on hand":      func(it *Item) { it.OnHand = d("-1") },
		"zero demand":           func(it *Item) { it.Demand = []Requirement{{Ref: "S", Date: 1, Qty: d("0")}} },
		"demand without ref":    func(it *Item) { it.Demand = []Requirement{{Date: 1, Qty: d("1")}} },
		"negative receipt":      func(it *Item) { it.Receipts = []Requirement{{Ref: "PO", Date: 1, Qty: d("-5")}} },
		"zero component qty":    func(it *Item) { it.Components = []BOMLine{{ItemID: "V-1", Qty: d("0")}} },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			it := base()
			mutate(&it)
			if _, err := Run(0, []Item{it}); !errors.Is(err, ErrInvalidItem) {
				t.Fatalf("err = %v; want ErrInvalidItem", err)
			}
		})
	}
	dup := []Item{base(), base()}
	if _, err := Run(0, dup); !errors.Is(err, ErrInvalidItem) {
		t.Fatalf("duplicate id: err = %v; want ErrInvalidItem", err)
	}
}

func TestRun_UnknownComponentIsRejected(t *testing.T) {
	items := []Item{{ID: "A", Coverage: CoverageRequirement, OrderType: OrderProduction, Components: []BOMLine{{ItemID: "Z", Qty: d("1")}}}}
	if _, err := Run(0, items); !errors.Is(err, ErrInvalidItem) {
		t.Fatalf("err = %v; want ErrInvalidItem", err)
	}
}

func TestRun_IsDeterministic(t *testing.T) {
	first, err := Run(week(1), br222())
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		again, err := Run(week(1), br222())
		if err != nil {
			t.Fatal(err)
		}
		if again.Hash != first.Hash {
			t.Fatalf("run %d hash %s != %s", i, again.Hash, first.Hash)
		}
	}
}

func TestRun_InputIsNotModified(t *testing.T) {
	items := br222()
	if _, err := Run(week(1), items); err != nil {
		t.Fatal(err)
	}
	if len(items[0].Demand) != 2 || len(items[1].Demand) != 0 {
		t.Fatalf("Run appended to the caller's demand slices: %d, %d", len(items[0].Demand), len(items[1].Demand))
	}
}

// Property: every planned order is fully explained by its pegs, and no demand is
// pegged beyond its quantity (FR-22.6, AC-22.2, AC-3 of the ticket).
func TestPropertyPeggingExplainsEveryOrder(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		n := rapid.IntRange(0, 12).Draw(t, "demands")
		var demand []Requirement
		for i := 0; i < n; i++ {
			demand = append(demand, Requirement{
				Ref:      "D-" + itoa(i),
				RefType:  RefSales,
				Date:     rapid.IntRange(0, 40).Draw(t, "date"),
				Qty:      decimal.NewFromInt(int64(rapid.IntRange(1, 100).Draw(t, "qty"))),
				Priority: PrioritySales,
			})
		}
		item := Item{
			ID:           "X",
			Coverage:     rapid.SampledFrom([]string{CoverageRequirement, CoveragePeriod, CoverageMinMax}).Draw(t, "coverage"),
			PeriodDays:   rapid.IntRange(0, 10).Draw(t, "period"),
			OrderType:    OrderPurchase,
			LeadTimeDays: rapid.IntRange(0, 6).Draw(t, "lt"),
			OnHand:       decimal.NewFromInt(int64(rapid.IntRange(0, 50).Draw(t, "onhand"))),
			MultipleQty:  decimal.NewFromInt(int64(rapid.IntRange(0, 20).Draw(t, "mult"))),
			ReorderMin:   decimal.NewFromInt(int64(rapid.IntRange(0, 30).Draw(t, "min"))),
			ReorderMax:   decimal.NewFromInt(int64(rapid.IntRange(30, 80).Draw(t, "max"))),
			Demand:       demand,
		}
		r, err := Run(0, []Item{item})
		if err != nil {
			t.Fatal(err)
		}
		supplied := map[string]decimal.Decimal{}
		demanded := map[string]decimal.Decimal{}
		for _, p := range r.Pegging {
			supplied[p.SupplyRef] = supplied[p.SupplyRef].Add(p.Qty)
			if p.DemandRefType != RefSurplus {
				demanded[p.DemandRef] = demanded[p.DemandRef].Add(p.Qty)
			}
		}
		for _, o := range r.Orders {
			if !supplied[o.Number].Equal(o.Qty) {
				t.Fatalf("order %s qty %s but pegged %s", o.Number, o.Qty, supplied[o.Number])
			}
		}
		for _, dm := range demand {
			if demanded[dm.Ref].GreaterThan(dm.Qty) {
				t.Fatalf("demand %s qty %s over-pegged to %s", dm.Ref, dm.Qty, demanded[dm.Ref])
			}
		}
		again, _ := Run(0, []Item{item})
		if again.Hash != r.Hash {
			t.Fatalf("non-deterministic hash")
		}
	})
}

func TestBuildTree_ExplainsPlannedOrderToSalesLine(t *testing.T) {
	r, err := Run(week(1), br222())
	if err != nil {
		t.Fatal(err)
	}
	po1 := findOrder(t, r, "RM-300", week(2))
	pegsOf := func(ref string) []PegLine {
		var out []PegLine
		for _, p := range r.Pegging {
			if p.SupplyRef == ref {
				out = append(out, PegLine{DemandRef: p.DemandRef, DemandRefType: p.DemandRefType, Qty: p.Qty})
			}
		}
		return out
	}
	tree := BuildTree(po1.Number, 20, pegsOf)
	total := decimal.Zero
	for _, c := range tree.Children {
		total = total.Add(c.Qty)
	}
	if !total.Equal(po1.Qty) {
		t.Fatalf("tree explains %s of %s", total, po1.Qty)
	}
	if !containsLeaf(tree, "SO-1") {
		t.Fatalf("tree does not reach sales line SO-1: %+v", tree)
	}
}

func containsLeaf(n TreeNode, ref string) bool {
	if n.Ref == ref {
		return true
	}
	for _, c := range n.Children {
		if containsLeaf(c, ref) {
			return true
		}
	}
	return false
}

func itoa(n int) string { return decimal.NewFromInt(int64(n)).String() }
