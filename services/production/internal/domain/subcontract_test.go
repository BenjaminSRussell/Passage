package domain

import (
	"testing"

	"github.com/shopspring/decimal"
	"pgregory.net/rapid"
)

func TestSubcontractRequests_OnlySubcontractOperationsInRouteOrder(t *testing.T) {
	tests := []struct {
		name string
		ops  []Operation
		want []int
	}{
		{name: "no operations", ops: nil, want: []int{}},
		{name: "in-house only", ops: []Operation{{OperNumber: 10}, {OperNumber: 20}}, want: []int{}},
		{name: "one subcontract", ops: []Operation{{OperNumber: 10}, {OperNumber: 20, Subcontract: true}}, want: []int{20}},
		{name: "two subcontracts keep route order", ops: []Operation{
			{OperNumber: 10, Subcontract: true}, {OperNumber: 20}, {OperNumber: 30, Subcontract: true},
		}, want: []int{10, 30}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := SubcontractRequests(tt.ops)
			if got == nil || len(got) != len(tt.want) {
				t.Fatalf("got %v, want operations %v", got, tt.want)
			}
			for i, req := range got {
				if req.OperNumber != tt.want[i] {
					t.Fatalf("request %d is operation %d, want %d", i, req.OperNumber, tt.want[i])
				}
			}
		})
	}
}

// The price on the operation is per unit of process quantity: the request
// carries it unchanged and the amount is price × quantity at cost scale.
func TestSubcontractRequests_UnitPriceAndAmount(t *testing.T) {
	ops := []Operation{{OperNumber: 20, ResourceID: "vendor-res", ProcessQty: d("102.040817"), SubcontractPrice: d("1.255"), Subcontract: true}}
	got := SubcontractRequests(ops)
	if len(got) != 1 {
		t.Fatalf("got %d requests, want 1", len(got))
	}
	req := got[0]
	if req.ResourceID != "vendor-res" || !req.Qty.Equal(d("102.040817")) || !req.UnitPrice.Equal(d("1.255")) {
		t.Fatalf("request = %+v", req)
	}
	// 102.040817 × 1.255 = 128.06122533… → 128.0612
	if !req.Amount.Equal(d("128.0612")) {
		t.Fatalf("amount = %s, want 128.0612", req.Amount)
	}
}

// Property: the requests' amounts are exactly the estimate's subcontract cost rows,
// so what purchasing is asked to buy is what the order planned (FR-21.3, FR-21.10).
func TestSubcontractRequests_MatchTheEstimate(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		n := rapid.IntRange(0, 6).Draw(t, "ops")
		ops := make([]Operation, n)
		for i := range ops {
			ops[i] = Operation{
				OperNumber:       (i + 1) * 10,
				ResourceID:       "res",
				ProcessQty:       decimal.New(int64(rapid.IntRange(1, 1_000_000).Draw(t, "qty")), -3),
				SubcontractPrice: decimal.New(int64(rapid.IntRange(0, 10_000_000).Draw(t, "price")), -4),
				Subcontract:      rapid.Bool().Draw(t, "subcontract"),
			}
		}
		planned := decimal.Zero
		for _, row := range Estimate(nil, ops) {
			if row.Group == GroupSubcontract {
				planned = planned.Add(row.Amount)
			}
		}
		requested := decimal.Zero
		for _, req := range SubcontractRequests(ops) {
			requested = requested.Add(req.Amount)
		}
		if !planned.Equal(requested) {
			t.Fatalf("estimate plans %s of subcontract, requests ask for %s", planned, requested)
		}
	})
}
