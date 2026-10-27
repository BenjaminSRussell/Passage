package domain

import (
	"testing"

	"github.com/shopspring/decimal"
	"pgregory.net/rapid"
)

// BR-22.4: time fences gate which planned orders and messages the engine emits.

func messagesOf(r Result, action string) []Message {
	var out []Message
	for _, m := range r.Messages {
		if m.Action == action {
			out = append(out, m)
		}
	}
	return out
}

func TestRun_FreezeFenceSuppressesOrders(t *testing.T) {
	demandAt := func(day, freeze int) Item {
		return Item{ID: "P", Coverage: CoverageRequirement, OrderType: OrderPurchase, LeadTimeDays: 2, FreezeDays: freeze,
			Demand: []Requirement{{Ref: "SO-1", RefType: RefSales, Date: day, Qty: d("10"), Priority: PrioritySales}}}
	}
	tests := []struct {
		name          string
		item          Item
		wantOrders    int
		wantShortages int
	}{
		{"no freeze plans the demand", demandAt(3, 0), 1, 0},
		{"demand inside the freeze gets no order and a shortage", demandAt(3, 5), 0, 1},
		{"demand on the fence boundary is outside the freeze", demandAt(5, 5), 1, 0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r, err := Run(0, []Item{tc.item})
			if err != nil {
				t.Fatal(err)
			}
			if len(r.Orders) != tc.wantOrders {
				t.Fatalf("orders = %d; want %d: %+v", len(r.Orders), tc.wantOrders, r.Orders)
			}
			short := messagesOf(r, ActionShortage)
			if len(short) != tc.wantShortages {
				t.Fatalf("shortages = %d; want %d: %+v", len(short), tc.wantShortages, r.Messages)
			}
			if len(short) == 1 && (short[0].DemandRef != "SO-1" || !short[0].Qty.Equal(d("10"))) {
				t.Fatalf("shortage = %+v; want SO-1 for 10", short[0])
			}
		})
	}
}

func TestRun_FreezeFenceUsesOnHandFirst(t *testing.T) {
	// On-hand 4 covers part of the frozen demand; only the rest is short.
	it := Item{ID: "P", Coverage: CoverageRequirement, OrderType: OrderPurchase, FreezeDays: 5, OnHand: d("4"),
		Demand: []Requirement{{Ref: "SO-1", RefType: RefSales, Date: 2, Qty: d("10"), Priority: PrioritySales}}}
	r, err := Run(0, []Item{it})
	if err != nil {
		t.Fatal(err)
	}
	short := messagesOf(r, ActionShortage)
	if len(r.Orders) != 0 || len(short) != 1 || !short[0].Qty.Equal(d("6")) {
		t.Fatalf("orders %+v shortages %+v; want no order and a shortage of 6", r.Orders, short)
	}
}

func TestRun_FuturesFenceGatesDelayMessage(t *testing.T) {
	// Demand on day 3 with lead time 5: the order would have to start on day
	// -2, so it is expected on day 5. The fence is measured on the
	// requirement date (day 3).
	base := Item{ID: "B", Coverage: CoverageRequirement, OrderType: OrderPurchase, LeadTimeDays: 5,
		Demand: []Requirement{{Ref: "SO-1", RefType: RefSales, Date: 3, Qty: d("10"), Priority: PrioritySales}}}
	tests := []struct {
		name        string
		futuresDays int
		wantDelays  int
	}{
		{"unlimited fence raises the delay", 0, 1},
		{"requirement inside the fence raises the delay", 4, 1},
		{"requirement on the fence boundary is outside", 3, 0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			it := base
			it.FuturesDays = tc.futuresDays
			r, err := Run(0, []Item{it})
			if err != nil {
				t.Fatal(err)
			}
			// The futures date is computed whatever the fence: only the message is gated.
			if len(r.Orders) != 1 || r.Orders[0].FuturesDate == nil || *r.Orders[0].FuturesDate != 5 {
				t.Fatalf("want one order expected on day 5: %+v", r.Orders)
			}
			if got := len(messagesOf(r, ActionDelay)); got != tc.wantDelays {
				t.Fatalf("delay messages = %d; want %d: %+v", got, tc.wantDelays, r.Messages)
			}
		})
	}
}

func TestRun_ActionFenceGatesPostpone(t *testing.T) {
	// The receipt on day 1 is 9 days before the demand on day 10, more than
	// the positive-days tolerance of 3, so it postpones. The action fence,
	// measured on the receipt date, decides whether that message is raised.
	base := Item{ID: "C", Coverage: CoverageRequirement, OrderType: OrderPurchase, PositiveDays: 3,
		Receipts: []Requirement{{Ref: "PO-EARLY", RefType: "purchase-line", Date: 1, Qty: d("10")}},
		Demand:   []Requirement{{Ref: "SO-9", RefType: RefSales, Date: 10, Qty: d("10"), Priority: PrioritySales}}}
	tests := []struct {
		name       string
		actionDays int
		wantPost   int
	}{
		{"unlimited action fence postpones", 0, 1},
		{"receipt inside a wider fence postpones", 2, 1},
		{"receipt outside the fence stays silent", 1, 0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			it := base
			it.ActionDays = tc.actionDays
			r, err := Run(0, []Item{it})
			if err != nil {
				t.Fatal(err)
			}
			if got := len(messagesOf(r, ActionPostpone)); got != tc.wantPost {
				t.Fatalf("postpone messages = %d; want %d: %+v", got, tc.wantPost, r.Messages)
			}
		})
	}
}

func TestRun_NegativeFenceRejected(t *testing.T) {
	for _, it := range []Item{
		{ID: "X", Coverage: CoverageRequirement, OrderType: OrderPurchase, FreezeDays: -1},
		{ID: "X", Coverage: CoverageRequirement, OrderType: OrderPurchase, ActionDays: -1},
		{ID: "X", Coverage: CoverageRequirement, OrderType: OrderPurchase, FuturesDays: -1},
	} {
		if _, err := Run(0, []Item{it}); err == nil {
			t.Fatalf("negative fence accepted: %+v", it)
		}
	}
}

// Property (4.22.4 invariant): with a freeze fence, every demand is pegged in
// full or the uncovered rest is reported as a shortage.
func TestRun_PropertyFreezeLeavesNoSilentShortage(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		items := genItems(t)
		freeze := rapid.IntRange(0, 10).Draw(t, "freeze")
		for i := range items {
			if items[i].Coverage == CoverageMinMax {
				items[i].Coverage = CoverageRequirement
			}
			items[i].FreezeDays = freeze
		}
		r, err := Run(0, items)
		if err != nil {
			t.Skip(err)
		}
		accounted := map[string]decimal.Decimal{}
		for _, p := range r.Pegging {
			accounted[p.DemandRef] = accounted[p.DemandRef].Add(p.Qty)
		}
		for _, m := range r.Messages {
			if m.Action == ActionShortage {
				accounted[m.DemandRef] = accounted[m.DemandRef].Add(m.Qty)
			}
		}
		for _, it := range items {
			for _, dm := range it.Demand {
				if !accounted[dm.Ref].Equal(dm.Qty) {
					t.Fatalf("%s demand %s qty %s: pegged plus short = %s", it.ID, dm.Ref, dm.Qty, accounted[dm.Ref])
				}
			}
		}
	})
}
