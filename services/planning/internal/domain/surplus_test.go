package domain

import (
	"slices"
	"testing"

	"github.com/shopspring/decimal"
)

// surplusMessages renders the cancel and decrease messages of a result.
func surplusMessages(res Result) []string {
	var out []string
	for _, m := range res.Messages {
		if m.Action == ActionCancel || m.Action == ActionDecrease {
			out = append(out, m.Action+":"+m.SupplyRef+":"+m.DemandRef+":"+m.Qty.String())
		}
	}
	return out
}

// TestRun_SurplusSupplyBeyondCoverageFence covers FR-22.7 Cancel and Decrease
// (BR-22.1 step 10): a receipt of 300 against a sales line of 100 leaves 200
// unpegged; beyond the coverage time fence that is a Decrease by 200, a
// receipt nothing pegs is a Cancel of all of it.
func TestRun_SurplusSupplyBeyondCoverageFence(t *testing.T) {
	sale := Requirement{Ref: "SO-1", RefType: RefSales, Date: 40, Qty: decimal.NewFromInt(100), Priority: PrioritySales}
	tests := []struct {
		name         string
		coverageDays int
		actionDays   int
		demand       []Requirement
		receiptDay   int
		want         []string
	}{
		{"partly pegged receipt beyond the fence decreases", 30, 0, []Requirement{sale}, 40, []string{"decrease:PO-1:SO-1:200"}},
		{"receipt on the fence day counts as beyond", 40, 0, []Requirement{sale}, 40, []string{"decrease:PO-1:SO-1:200"}},
		{"receipt inside the fence keeps its surplus", 41, 0, []Requirement{sale}, 40, nil},
		{"fence 0 raises nothing", 0, 0, []Requirement{sale}, 40, nil},
		{"unpegged receipt beyond the fence cancels", 30, 0, nil, 40, []string{"cancel:PO-1::300"}},
		{"outside the action fence nothing is raised", 30, 35, nil, 40, nil},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			res, err := Run(0, []Item{{
				ID: "A", Coverage: CoverageRequirement, OrderType: OrderPurchase, NegativeDays: 1,
				CoverageDays: tc.coverageDays, ActionDays: tc.actionDays, Demand: tc.demand,
				Receipts: []Requirement{{Ref: "PO-1", RefType: "purchase-line", Date: tc.receiptDay, Qty: decimal.NewFromInt(300)}},
			}})
			if err != nil {
				t.Fatal(err)
			}
			if got := surplusMessages(res); !slices.Equal(got, tc.want) {
				t.Fatalf("messages = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestRun_CancelCarriesReceiptDate checks the Cancel message names the receipt
// date and that a fully pegged receipt raises no surplus message.
func TestRun_CancelCarriesReceiptDate(t *testing.T) {
	res, err := Run(0, []Item{{
		ID: "A", Coverage: CoverageRequirement, OrderType: OrderPurchase, CoverageDays: 10,
		Demand: []Requirement{{Ref: "SO-1", RefType: RefSales, Date: 20, Qty: decimal.NewFromInt(50), Priority: PrioritySales}},
		Receipts: []Requirement{
			{Ref: "PO-1", RefType: "purchase-line", Date: 20, Qty: decimal.NewFromInt(50)},
			{Ref: "PO-2", RefType: "purchase-line", Date: 25, Qty: decimal.NewFromInt(70)},
		},
	}})
	if err != nil {
		t.Fatal(err)
	}
	var found bool
	for _, m := range res.Messages {
		switch {
		case m.SupplyRef == "PO-1" && (m.Action == ActionCancel || m.Action == ActionDecrease):
			t.Fatalf("fully pegged PO-1 got %+v", m)
		case m.SupplyRef == "PO-2":
			if m.Action != ActionCancel || m.SuggestedDate != 25 || !m.Qty.Equal(decimal.NewFromInt(70)) {
				t.Fatalf("PO-2 message = %+v, want cancel 70 on day 25", m)
			}
			found = true
		}
	}
	if !found {
		t.Fatalf("no cancel for PO-2 in %+v", res.Messages)
	}
}

func TestRun_NegativeCoverageFenceRejected(t *testing.T) {
	if _, err := Run(0, []Item{{ID: "A", Coverage: CoverageRequirement, OrderType: OrderPurchase, CoverageDays: -1}}); err == nil {
		t.Fatal("a negative coverage time fence must be rejected")
	}
}
