package domain

import (
	"errors"
	"testing"

	"github.com/shopspring/decimal"
)

func scopeItems() []Item {
	d := decimal.RequireFromString
	return []Item{
		{ID: "FG", Coverage: CoverageRequirement, OrderType: OrderProduction, LeadTimeDays: 5,
			Demand:     []Requirement{{Ref: "SO-1", RefType: RefSales, Date: 10, Qty: d("100"), Priority: PrioritySales}, {Ref: "SO-2", RefType: RefSales, Date: 400, Qty: d("50"), Priority: PrioritySales}},
			Components: []BOMLine{{ItemID: "RM", Qty: d("2")}}},
		{ID: "RM", Coverage: CoverageRequirement, OrderType: OrderPurchase, LeadTimeDays: 3},
	}
}

func TestClipHorizon_DropsDemandBeyondTheHorizon(t *testing.T) {
	in := scopeItems()
	out := ClipHorizon(in, 365)
	if len(out[0].Demand) != 1 || out[0].Demand[0].Ref != "SO-1" {
		t.Fatalf("demand after clip = %+v; want only SO-1", out[0].Demand)
	}
	if len(in[0].Demand) != 2 {
		t.Fatal("the input snapshot was modified")
	}
	if got := ClipHorizon(in, 0); len(got[0].Demand) != 2 {
		t.Fatal("horizon 0 must keep everything")
	}
	if got := ClipHorizon(in, 400); len(got[0].Demand) != 2 {
		t.Fatal("demand on the horizon day is inside it")
	}
}

func TestApplyScope_ReportsOnlyItemsInside(t *testing.T) {
	items := scopeItems()
	res, err := Run(0, items)
	if err != nil {
		t.Fatal(err)
	}
	attrs := map[string]ItemAttrs{"FG": {Group: "FIN", Planner: "ANN"}, "RM": {Group: "RAW", Planner: "BOB"}}
	one := 1
	cases := []struct {
		name  string
		scope Scope
		want  string // the only item reported
	}{
		{"group", Scope{ItemGroups: []string{"RAW"}}, "RM"},
		{"planner", Scope{Planners: []string{"ANN"}}, "FG"},
		{"item id", Scope{ItemIDs: []string{"RM"}}, "RM"},
		{"llc min", Scope{LLCMin: &one}, "RM"},
		{"llc max", Scope{LLCMax: new(int)}, "FG"},
		{"group and planner", Scope{ItemGroups: []string{"RAW"}, Planners: []string{"BOB"}}, "RM"},
	}
	for _, c := range cases {
		got, n, err := ApplyScope(res, items, attrs, c.scope)
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		if n != 1 {
			t.Fatalf("%s: items in scope = %d; want 1", c.name, n)
		}
		for _, o := range got.Orders {
			if o.ItemID != c.want {
				t.Fatalf("%s: order of %s outside the scope", c.name, o.ItemID)
			}
		}
		for _, r := range got.Net {
			if r.ItemID != c.want {
				t.Fatalf("%s: net row of %s outside the scope", c.name, r.ItemID)
			}
		}
		if len(got.Orders) == 0 || len(got.Net) == 0 {
			t.Fatalf("%s: nothing reported for %s", c.name, c.want)
		}
		if got.Hash == res.Hash {
			t.Fatalf("%s: hash must be recomputed over the scoped result", c.name)
		}
	}
}

// The component's planned order is sized by the parent outside the scope: the
// whole snapshot is still exploded.
func TestApplyScope_KeepsDependentDemandFromOutsideTheScope(t *testing.T) {
	items := scopeItems()
	res, _ := Run(0, items)
	got, _, err := ApplyScope(res, items, nil, Scope{ItemIDs: []string{"RM"}})
	if err != nil {
		t.Fatal(err)
	}
	sum := decimal.Zero
	for _, o := range got.Orders {
		sum = sum.Add(o.Qty)
	}
	if len(got.Orders) != 2 || !sum.Equal(decimal.RequireFromString("300")) {
		t.Fatalf("RM orders = %+v; want two (one per demand) totalling 2 x (100 + 50)", got.Orders)
	}
}

func TestApplyScope_EmptyScopeIsTheWholeResult(t *testing.T) {
	items := scopeItems()
	res, _ := Run(0, items)
	got, n, err := ApplyScope(res, items, nil, Scope{})
	if err != nil || n != 2 || got.Hash != res.Hash {
		t.Fatalf("empty scope: n=%d hash equal=%v err=%v", n, got.Hash == res.Hash, err)
	}
}

func TestLowLevelCodes_RejectsUnknownComponent(t *testing.T) {
	items := scopeItems()[:1]
	if _, err := LowLevelCodes(items); !errors.Is(err, ErrInvalidItem) {
		t.Fatalf("err = %v; want ErrInvalidItem", err)
	}
}

// Supply dated beyond the horizon covers demand the run no longer sees, so it
// must not be reported as surplus (Cancel); surplus inside the horizon still is.
func TestRunWithinHorizon_NoCancelForSupplyBeyondTheHorizon(t *testing.T) {
	d := decimal.RequireFromString
	items := []Item{{ID: "RM", Coverage: CoverageRequirement, OrderType: OrderPurchase, LeadTimeDays: 3, CoverageDays: 100,
		Demand: []Requirement{{Ref: "SO-9", RefType: RefSales, Date: 400, Qty: d("60"), Priority: PrioritySales}},
		Receipts: []Requirement{
			{Ref: "PO-LATE", RefType: "purchase-line", Date: 400, Qty: d("50")},
			{Ref: "PO-SPARE", RefType: "purchase-line", Date: 200, Qty: d("10")},
		}}}
	whole, _, err := RunWithinHorizon(0, nil, items, 0)
	if err != nil {
		t.Fatal(err)
	}
	if cancels(whole, "PO-LATE")+cancels(whole, "PO-SPARE") != 0 {
		t.Fatalf("without a horizon both receipts cover SO-9: %+v", whole.Messages)
	}
	res, clipped, err := RunWithinHorizon(0, nil, items, 365)
	if err != nil {
		t.Fatal(err)
	}
	if len(clipped[0].Demand) != 0 {
		t.Fatalf("SO-9 is beyond the horizon: %+v", clipped[0].Demand)
	}
	if n := cancels(res, "PO-LATE"); n != 0 {
		t.Fatalf("PO-LATE is beyond the horizon and must raise no cancel: %+v", res.Messages)
	}
	if n := cancels(res, "PO-SPARE"); n != 1 {
		t.Fatalf("PO-SPARE is surplus inside the horizon and beyond the coverage fence: %+v", res.Messages)
	}
	if res.Hash != hashResult(res) {
		t.Fatal("hash must cover the messages kept")
	}
}

func cancels(r Result, supply string) int {
	n := 0
	for _, m := range r.Messages {
		if m.SupplyRef == supply && (m.Action == ActionCancel || m.Action == ActionDecrease) {
			n++
		}
	}
	return n
}

func TestScope_MatchesItemIDsWhateverTheCase(t *testing.T) {
	s := Scope{ItemIDs: []string{"00000000-0000-0000-0000-0000000000AB"}}
	if !s.Matches("00000000-0000-0000-0000-0000000000ab", ItemAttrs{}, 0) {
		t.Fatal("a UUID must match in either case")
	}
	if s.Matches("00000000-0000-0000-0000-0000000000ac", ItemAttrs{}, 0) {
		t.Fatal("another id must not match")
	}
}
